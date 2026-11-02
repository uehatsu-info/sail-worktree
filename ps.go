package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	Kind     string         `json:"kind"`     // kindWorktree or kindMain
	Dir      string         `json:"dir"`      // project directory (the registry key)
	Worktree string         `json:"worktree"` // name of the worktree root
	Subdir   string         `json:"subdir"`   // project directory below the worktree root, "." at the root
	Branch   string         `json:"branch"`
	Ports    map[string]int `json:"ports"`
	Name     string         `json:"name"`  // compose project name
	State    string         `json:"state"` // one of the state constants below
}

// Kinds of an entry: a worktree record (registry "worktrees") or a main worktree record ("mains").
const (
	kindWorktree = "worktree"
	kindMain     = "main"
)

// States of an entry. stateNone means that nothing was checked for it.
const (
	stateNone         = "-"
	stateStale        = "stale"        // the worktree or directory is gone, or the key is invalid
	stateUnattributed = "unattributed" // the directory exists but no listed worktree holds it
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
// because the path cannot be recovered without -z, and so is a record whose path usable refuses, before anything
// looks at that path; if that is the main worktree's record the whole listing is dropped (nil), since every index and
// the project name depend on it. Paths are made real as far as they exist.
func parseWorktreeList(out string, usable func(string) bool) []worktreeInfo {
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
			p := filepath.Clean(val)
			if val == "" || strings.HasPrefix(val, `"`) || !usable(p) {
				skip = true
				continue
			}
			cur.Path = looseRealPath(p)
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

// anyPath accepts every worktree path: those of the repository the user runs in are the user's own.
func anyPath(string) bool { return true }

// registryPathOK decides which worktree paths of a repository found through the registry may be used as paths: they
// come from that repository's metadata, as untrusted as the registry (replaceable in tests). Not reached on Windows,
// where gitInRegistryDirs is off; kept for the other systems.
var registryPathOK = validKey

// gitInRegistryDirs tells whether --all (ps and ports) runs git in the directories of other repositories. Not on
// Windows: git itself looks at paths from that repository's metadata, and a UNC or device path there can open a network
// connection that sends the user's credentials (replaceable in tests).
var gitInRegistryDirs = runtime.GOOS != "windows"

// listWorktrees lists the worktrees of the repository that contains dir (the first one is the main worktree), keeping
// only the paths usable accepts. Only `worktree list` is used, with fsmonitor off, so that a repository's own
// configuration cannot start a program.
func listWorktrees(dir string, usable func(string) bool) ([]worktreeInfo, error) {
	out, err := runOutput(dir, gitListEnv(), 10*time.Second, "git",
		"-c", "core.fsmonitor=false", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	list := parseWorktreeList(string(out), usable)
	if len(list) == 0 {
		return nil, fmt.Errorf("git worktree list: cannot read the main worktree's path")
	}
	return list, nil
}

// foldRegistry returns the registry as a map from real project directory to ports. Keys that an older version
// recorded under a symlinked path are folded into their real path the way Registry.migrate does (an entry under the
// real path wins, otherwise the smallest alias), but only in this copy: it is never saved.
func foldRegistry(r *Registry) map[string]map[string]int {
	groups := map[string][]string{}
	for k := range r.Worktrees {
		rp := foldKey(k)
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

// foldKey returns the real path of a registry key, or the key itself when it fails validKey (never resolved, see
// validKey) or cannot be resolved.
func foldKey(k string) string {
	if validKey(k) {
		if rp, err := realPath(k); err == nil {
			return rp
		}
	}
	return k
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

// placeIn fills the columns that the listed worktree w gives an entry for key.
func placeIn(e *entry, w worktreeInfo) {
	rel, err := filepath.Rel(w.Path, e.Dir)
	if err != nil {
		rel = "?"
	}
	e.Worktree, e.Subdir, e.Branch = filepath.Base(w.Path), filepath.ToSlash(rel), w.Branch
	if w.Detached {
		e.Branch = "(detached)"
	}
}

func buildEntry(key string, ports map[string]int, l []worktreeInfo, wt int) entry {
	w := l[wt]
	e := entry{Kind: kindWorktree, Dir: key, Ports: ports, Name: projectName(l[0].Path, w.Path, key), State: stateNone}
	placeIn(&e, w)
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

// registryView attributes registry records to listed worktrees for one command. collectEntries makes a new one on
// every call; repos[0] is the current repository's listing when there is one.
type registryView struct {
	all   bool
	repos [][]worktreeInfo
	tried map[string]bool // directories git has listed in (or failed in) for this view
}

// worktreeLister runs `git worktree list` for registryView.list (replaceable in tests, to count the runs).
var worktreeLister = listWorktrees

// list runs git in dir at most once per view, since each run can take up to its timeout, and adds the listing unless
// the same repository (the same main worktree path) is already listed: attribute keeps the first of equal matches,
// so a second copy would change nothing. It reports whether the repositories may have changed.
func (v *registryView) list(dir string) bool {
	if v.tried[dir] {
		return false
	}
	if v.tried == nil {
		v.tried = map[string]bool{}
	}
	v.tried[dir] = true
	l, err := worktreeLister(dir, registryPathOK)
	if err != nil || len(l) == 0 {
		return false
	}
	for _, r := range v.repos { // listWorktrees never returns an empty listing without an error
		if r[0].Path == l[0].Path {
			return false
		}
	}
	v.repos = append(v.repos, l)
	return true
}

// placement is what a list shows for a registry record.
type placement int

const (
	placeListed       placement = iota // attributed to a listing that is shown
	placeSkipped                       // another repository without all, or a usable record not attributed
	placeUnattributed                  // all: the directory exists but no listed worktree holds it
	placeStale                         // all: the directory is gone or the key is invalid
	placeHidden                        // without all: gone or invalid, counted for the note
)

// place attributes key with find. With all, a record of another repository that is listable makes git list that
// repository in key's directory (only where gitInRegistryDirs allows it). present tells whether the record's
// directory is still there; without all a record that is not present cannot be told apart from another repository's,
// so it is hidden and counted. present and listable are called only when an unattributed record needs them, and must
// check validKey before they look at key.
func (v *registryView) place(key string, present, listable func() bool,
	find func(string, [][]worktreeInfo) (int, int, bool)) (ri, wi int, p placement) {
	ri, wi, ok := find(key, v.repos)
	if !ok && v.all && gitInRegistryDirs && listable() {
		if v.list(key) {
			ri, wi, ok = find(key, v.repos)
		}
	}
	switch {
	case ok && (v.all || ri == 0):
		return ri, wi, placeListed
	case ok:
		return ri, wi, placeSkipped
	case v.all && present():
		return ri, wi, placeUnattributed
	case v.all:
		return ri, wi, placeStale
	case !present():
		return ri, wi, placeHidden
	}
	return ri, wi, placeSkipped
}

// unlisted is the entry of a record that no shown listing places (placeUnattributed or placeStale).
func unlisted(kind, key string, ports map[string]int, p placement) entry {
	st := stateStale
	if p == placeUnattributed {
		st = stateUnattributed
	}
	return entry{Kind: kind, Dir: key, Ports: ports, State: st}
}

// noteHidden tells, for ps and ports, how many records collectEntries left out without all.
func noteHidden(hidden int) {
	if hidden > 0 {
		fmt.Fprintf(stderr, "note: registry entries not listed: %d (gone or invalid, they still hold their ports); "+
			"use --all\n", hidden)
	}
}

// collectEntries lists the registry entries of the current repository, or of every repository with all. hidden
// counts entries that are gone or invalid and cannot be attributed to the current repository (only without all).
// They still hold their ports, so the caller says so (noteHidden).
func collectEntries(all bool) (entries []entry, hidden int, err error) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, 0, err
	}
	view := foldRegistry(reg)
	v := &registryView{all: all}
	repoErr := fmt.Errorf("not in a git repository")
	if dir, err := cwd(); err != nil {
		repoErr = err
	} else if top, _, err := worktreeRootAndPrefix(dir); err != nil {
		repoErr = err
	} else if l, err := listWorktrees(top, anyPath); err != nil {
		repoErr = err
	} else {
		v.repos = append(v.repos, l)
	}
	if len(v.repos) == 0 && !all {
		return nil, 0, fmt.Errorf("run this inside a git repository, or use --all to list every project in the registry: %w", repoErr)
	}
	entries = []entry{}
	for _, key := range slices.Sorted(maps.Keys(view)) {
		ports := view[key]
		if ports == nil {
			ports = map[string]int{}
		}
		present := func() bool { return validKey(key) && pathExists(key) }
		// git runs only in a directory that still exists. The key is folded, so a link as its last element (which
		// usableDir does not follow) is left only where the link cannot be resolved.
		listable := func() bool { return usableDir(key) }
		ri, wi, p := v.place(key, present, listable, attribute)
		switch p {
		case placeListed:
			entries = append(entries, buildEntry(key, ports, v.repos[ri], wi))
		case placeUnattributed, placeStale:
			entries = append(entries, unlisted(kindWorktree, key, ports, p))
		case placeHidden:
			hidden++
		}
	}
	mains, mainHidden := mainEntries(reg, v)
	// Mains first; never nil, so that an empty list is [] in JSON.
	return append(append(make([]entry, 0, len(mains)+len(entries)), mains...), entries...), hidden + mainHidden, nil
}

// mainEntries lists the main worktree records: the current repository's main without all, every record with all.
// They show the recorded ports, which up avoids, and never read the main's .env. The compose project name and the
// docker state are left out: the name comes from that .env or the directory, so it would only be a guess.
func mainEntries(reg *Registry, v *registryView) (entries []entry, hidden int) {
	for _, key := range slices.Sorted(maps.Keys(reg.Mains)) {
		ports := reg.Mains[key].Ports
		if ports == nil {
			ports = map[string]int{}
		}
		// Lstat, as check does: a main key that is a link is not followed.
		usable := usableDir(key)
		isUsable := func() bool { return usable }
		ri, _, p := v.place(key, isUsable, isUsable, func(k string, repos [][]worktreeInfo) (int, int, bool) {
			ri, ok := attributeMain(k, repos)
			return ri, 0, ok
		})
		switch p {
		case placeListed:
			entries = append(entries, buildMainEntry(key, ports, v.repos[ri][0], usable))
		case placeUnattributed, placeStale:
			entries = append(entries, unlisted(kindMain, key, ports, p))
		case placeHidden:
			hidden++
		}
	}
	return entries, hidden
}

// attributeMain finds the repository whose main worktree holds key, which must not be inside one of its linked
// worktrees (such a record would not be a main's).
func attributeMain(key string, repos [][]worktreeInfo) (repo int, ok bool) {
	ri, wi, ok := attribute(key, repos)
	return ri, ok && wi == 0
}

// buildMainEntry is the entry of a main record that a listing places; it has no compose project name (see mainEntries).
func buildMainEntry(key string, ports map[string]int, w worktreeInfo, usable bool) entry {
	e := entry{Kind: kindMain, Dir: key, Ports: ports, State: stateNone}
	placeIn(&e, w)
	if !usable {
		e.State = stateStale
	}
	return e
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

// worktreeCell marks a main worktree's row, so the tables need no new column.
func worktreeCell(kind, name string) string {
	if kind != kindMain {
		return cell(name)
	}
	if name == "" {
		return "(main)"
	}
	return cell(name) + " (main)"
}

// portsCell prints recorded ports as they are (check's and prune's messages).
func portsCell(ports map[string]int) string { return portsText(ports, false) }

// psPortsCell marks a value that is not a port number, which up replaces and ports, check and status ignore. The JSON
// keeps the plain value.
func psPortsCell(ports map[string]int) string { return portsText(ports, true) }

func portsText(ports map[string]int, markInvalid bool) string {
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + strconv.Itoa(ports[n])
		if markInvalid && !validPort(ports[n]) {
			parts[i] += "(invalid)"
		}
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
		if e.State != stateNone || e.Kind == kindMain { // docker is not asked about a main: its name is unknown
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", worktreeCell(e.Kind, e.Worktree), cell(e.Subdir), cell(e.Branch),
			cell(psPortsCell(e.Ports)), cell(e.State), cell(e.Dir))
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
	noteHidden(hidden)
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
