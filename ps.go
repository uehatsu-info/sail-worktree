package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// ps, status and ports only read: they never save the registry, never write .env or .sail-worktree.json and never
// source .env. They share the registry view below.

// entry is one registry entry as ps shows it. The JSON field names are a public contract.
type entry struct {
	Dir      string         `json:"dir"`      // project directory (the registry key)
	Worktree string         `json:"worktree"` // name of the worktree root
	Subdir   string         `json:"subdir"`   // project directory below the worktree root, "." at the root
	Branch   string         `json:"branch"`
	Ports    map[string]int `json:"ports"`
	Name     string         `json:"name"`  // compose project name
	State    string         `json:"state"` // one of the state constants below
}

// States of an entry. stateNone means that nothing was checked for it.
const (
	stateNone         = "-"
	stateStale        = "stale"        // the worktree or directory is gone, or the key is invalid
	stateUnattributed = "unattributed" // the directory exists but git does not place it in a listed worktree
	// docker's view of the compose project.
	stateRunning = "running" // at least one container runs
	stateStopped = "stopped" // containers exist, none runs
	stateDown    = "down"    // docker knows no such project (never started, or removed)
	stateUnknown = "unknown" // docker could not be asked
)

// worktreeInfo is a record of `git worktree list --porcelain`.
type worktreeInfo struct {
	Path     string
	Branch   string // without refs/heads/
	Detached bool
	Bare     bool
	Prunable bool
}

// parseWorktreeList parses the porcelain output: records are separated by a blank line and start with "worktree
// <path>". The main worktree is first. A record whose path git C-quoted (it starts with a double quote) is skipped,
// because the path cannot be recovered without -z; if that is the main worktree's record the whole listing is
// dropped (nil), since every index and the project name depend on it. Paths are made real as far as they exist.
func parseWorktreeList(out string) []worktreeInfo {
	var list []worktreeInfo
	var cur *worktreeInfo
	skip, first, bad := false, true, false
	flush := func() {
		if cur != nil {
			if skip && first {
				bad = true
			} else if !skip {
				list = append(list, *cur)
			}
			first = false
		}
		cur, skip = nil, false
	}
	for _, line := range strings.Split(out, "\n") {
		if runtime.GOOS == "windows" {
			line = strings.TrimSuffix(line, "\r")
		}
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &worktreeInfo{}
			if val == "" || strings.HasPrefix(val, `"`) {
				skip = true
				continue
			}
			cur.Path = looseRealPath(filepath.Clean(val))
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	if bad {
		return nil
	}
	return list
}

// looseRealPath resolves links in the part of p that exists and keeps the rest, so that a worktree whose directory
// is gone still compares equal to the real-path key the registry recorded for it.
func looseRealPath(p string) string {
	if r, err := realPath(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if dir == "" || filepath.Clean(dir) == p {
		return p
	}
	return filepath.Join(looseRealPath(filepath.Clean(dir)), base)
}

// gitListEnv is the environment of the git calls that run in directories taken from the registry: GIT_DIR and the
// like would make every directory resolve to the same repository.
func gitListEnv() []string {
	drop := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true}
	environ := os.Environ()
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[strings.ToUpper(k)] {
			out = append(out, kv)
		}
	}
	return out
}

// listWorktrees lists the worktrees of the repository that contains dir (the first one is the main worktree). Only
// `worktree list` is used, with fsmonitor off, so that a repository's own configuration cannot start a program.
func listWorktrees(dir string) ([]worktreeInfo, error) {
	out, err := runOutput(dir, gitListEnv(), 10*time.Second, "git",
		"-c", "core.fsmonitor=false", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	list := parseWorktreeList(string(out))
	if len(list) == 0 {
		return nil, fmt.Errorf("git worktree list: cannot read the main worktree's path")
	}
	return list, nil
}

// validKey reports whether a registry key is an absolute, clean path. Anything else is never used as a directory.
func validKey(k string) bool {
	return k != "" && filepath.IsAbs(k) && filepath.Clean(k) == k
}

// foldRegistry returns the registry as a map from real project directory to ports. Keys that an older version
// recorded under a symlinked path are folded into their real path the way Registry.migrate does (an entry under the
// real path wins, otherwise the smallest alias), but only in this copy: it is never saved.
func foldRegistry(r *Registry) map[string]map[string]int {
	groups := map[string][]string{}
	for k := range r.Worktrees {
		rp := k
		if validKey(k) {
			if x, err := realPath(k); err == nil {
				rp = x
			}
		}
		groups[rp] = append(groups[rp], k)
	}
	out := make(map[string]map[string]int, len(groups))
	for rp, keys := range groups {
		sort.Strings(keys)
		pick := keys[0]
		for _, k := range keys {
			if k == rp {
				pick = k
				break
			}
		}
		out[rp] = r.Worktrees[pick]
	}
	return out
}

// attribute finds the worktree that contains key: the longest match, compared per path element, so that a linked
// worktree inside the main one wins. repos[i][0] is the main worktree of repository i.
func attribute(key string, repos [][]worktreeInfo) (repo, wt int, ok bool) {
	if !validKey(key) {
		return 0, 0, false
	}
	best := -1
	for i, l := range repos {
		for j, w := range l {
			if len(w.Path) > best && within(w.Path, key) {
				best, repo, wt, ok = len(w.Path), i, j, true
			}
		}
	}
	return
}

func buildEntry(key string, ports map[string]int, l []worktreeInfo, wt int) entry {
	w := l[wt]
	rel, err := filepath.Rel(w.Path, key)
	if err != nil {
		rel = "?"
	}
	branch := w.Branch
	if w.Detached {
		branch = "(detached)"
	}
	e := entry{
		Dir: key, Worktree: filepath.Base(w.Path), Subdir: filepath.ToSlash(rel), Branch: branch,
		Ports: ports, Name: projectName(l[0].Path, w.Path, key), State: stateNone,
	}
	// Nothing is recorded for the main worktree (up refuses it), so an entry there is not valid either.
	if wt == 0 || w.Prunable || !pathExists(w.Path) || !pathExists(key) {
		e.State = stateStale
	}
	return e
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// collectEntries lists the registry entries of the current repository, or of every repository with all. hidden
// counts entries that are gone or invalid and cannot be attributed to the current repository (only without all).
// They still hold their ports, so the caller says so.
func collectEntries(all bool) (entries []entry, hidden int, err error) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, 0, err
	}
	view := foldRegistry(reg)
	var repos [][]worktreeInfo
	repoErr := fmt.Errorf("not in a git repository")
	if dir, err := cwd(); err != nil {
		repoErr = err
	} else if top, _, err := worktreeRootAndPrefix(dir); err != nil {
		repoErr = err
	} else if l, err := listWorktrees(top); err != nil {
		repoErr = err
	} else {
		repos = append(repos, l)
	}
	if len(repos) == 0 && !all {
		return nil, 0, fmt.Errorf("run this inside a git repository, or use --all to list every project in the registry: %w", repoErr)
	}
	keys := make([]string, 0, len(view))
	for k := range view {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries = []entry{}
	for _, key := range keys {
		ports := view[key]
		if ports == nil {
			ports = map[string]int{}
		}
		ri, wi, ok := attribute(key, repos)
		if !ok && all && validKey(key) {
			// Another repository: ask git in the entry's own directory (only if it still exists).
			if fi, err := os.Stat(key); err == nil && fi.IsDir() {
				if l, err := listWorktrees(key); err == nil && len(l) > 0 {
					repos = append(repos, l)
					ri, wi, ok = attribute(key, repos)
				}
			}
		}
		// Without --all only the current repository is shown. An entry of another repository is skipped on purpose;
		// one that is gone or invalid cannot be told apart, so it is counted for the note.
		switch {
		case ok && (all || ri == 0):
			entries = append(entries, buildEntry(key, ports, repos[ri], wi))
		case ok:
		case all && validKey(key) && pathExists(key):
			entries = append(entries, entry{Dir: key, Ports: ports, State: stateUnattributed})
		case all:
			entries = append(entries, entry{Dir: key, Ports: ports, State: stateStale})
		case !validKey(key) || !pathExists(key):
			hidden++
		}
	}
	return entries, hidden, nil
}

// jsonEscape writes the runes that strconv.IsPrint rejects (DEL, format characters such as U+202E, ...) as \uXXXX
// escapes, which encoding/json leaves raw; the result is still valid JSON (non-BMP runes become surrogate pairs).
func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == utf8.RuneError || strconv.IsPrint(r):
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// cell makes a value safe for a table: control characters are escaped (escapeControl) and a newline, which it keeps
// for layout, is written out. An empty value is "-".
func cell(s string) string {
	s = strings.ReplaceAll(escapeControl(s), "\n", `\n`)
	if s == "" {
		return "-"
	}
	return s
}

func portsCell(ports map[string]int) string {
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + strconv.Itoa(ports[n])
	}
	return strings.Join(parts, " ")
}

// dockerTimeout bounds the docker query, so a hanging daemon delays ps by this long at most.
const dockerTimeout = 10 * time.Second

// dockerProjects asks docker once for all compose projects, including stopped ones. A project is running when its
// status lists a running container ("running(1), exited(1)" counts). docker's own text is only compared, never shown.
func dockerProjects() (map[string]string, error) {
	// The directory is the system temp directory, so that nothing in the current project can influence docker.
	out, err := output(os.TempDir(), cleanEnv(nil), dockerTimeout, "docker", "compose", "ls", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	var list []struct{ Name, Status string }
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	if list == nil {
		return nil, fmt.Errorf("docker compose ls: unexpected output")
	}
	m := make(map[string]string, len(list))
	for _, p := range list {
		if p.Name == "" { // a changed format must not read as "no such project"
			return nil, fmt.Errorf("docker compose ls: unexpected output")
		}
		if strings.Contains(strings.ToLower(p.Status), "running") {
			m[p.Name] = stateRunning
		} else if m[p.Name] != stateRunning {
			m[p.Name] = stateStopped
		}
	}
	return m, nil
}

// dockerState is the state of the compose project name in the result of dockerProjects (err: docker failed).
func dockerState(projects map[string]string, err error, name string) string {
	if err != nil {
		return stateUnknown
	}
	if st, ok := projects[name]; ok {
		return st
	}
	return stateDown
}

// fillDockerStates sets the docker state of every entry that was not marked otherwise. docker is asked once, and
// only when there is an entry to ask about; a docker failure makes the states unknown, never an error.
func fillDockerStates(entries []entry) {
	var projects map[string]string
	var err error
	asked := false
	for i := range entries {
		e := &entries[i]
		if e.State != stateNone {
			continue
		}
		if e.Name == "" { // not reachable for a valid entry; never report "-" as if docker had been skipped
			e.State = stateUnknown
			continue
		}
		if !asked {
			projects, err = dockerProjects()
			asked = true
		}
		e.State = dockerState(projects, err, e.Name)
	}
}

func renderPs(w io.Writer, entries []entry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "no registry entries")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKTREE\tSUBDIR\tBRANCH\tPORTS\tSTATE\tDIR")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", cell(e.Worktree), cell(e.Subdir), cell(e.Branch),
			cell(portsCell(e.Ports)), cell(e.State), cell(e.Dir))
	}
	tw.Flush()
}

func cmdPs(args []string) error {
	var all, asJSON, noDocker bool
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		case "--json":
			asJSON = true
		case "--no-docker":
			noDocker = true
		default:
			return fmt.Errorf("unknown argument: %s", a)
		}
	}
	entries, hidden, err := collectEntries(all)
	if err != nil {
		return err
	}
	if !noDocker {
		fillDockerStates(entries)
	}
	if hidden > 0 {
		fmt.Fprintf(stderr, "note: registry entries not listed: %d (gone or invalid, they still hold their ports); use --all\n", hidden)
	}
	if asJSON {
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, jsonEscape(string(b)))
		return nil
	}
	renderPs(stdout, entries)
	return nil
}
