package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The project directory is the nearest directory, from the cwd up to the worktree root, that holds a marker file:
// .sail-worktree.json for up, stop and rm, a compose file for init. This lets the Laravel project live in a
// subdirectory of the repository.

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// candidate is a directory the project may be in. rel is its path below the worktree root, "/"-separated and without
// a trailing "/" ("" for the root).
type candidate struct {
	dir, rel string
}

// within reports whether p is base or below it. Rel is lexical, and "..foo" is a legitimate name, so compare with ".."
// and ".."+separator only.
func within(base, p string) bool {
	r, err := filepath.Rel(base, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// skippedDir reports whether a directory is never a project: packages under vendor and node_modules may ship their
// own compose file or .sail-worktree.json.
func skippedDir(name string) bool {
	return strings.EqualFold(name, "vendor") || strings.EqualFold(name, "node_modules")
}

// projectCandidates returns the directories from wtTop/prefix up to wtTop, most specific first. Only the elements of
// prefix are checked for vendor and node_modules, so a worktree that itself lives under a "vendor" directory works.
// Every candidate is checked to be inside wtTop before anything looks at it.
func projectCandidates(wtTop, prefix string) ([]candidate, error) {
	if strings.HasPrefix(prefix, "/") {
		return nil, fmt.Errorf("unexpected path from git: %q", prefix)
	}
	var elems []string
	if p := strings.TrimSuffix(prefix, "/"); p != "" {
		elems = strings.Split(p, "/")
	}
	for _, e := range elems {
		if e == "" || e == "." || e == ".." {
			return nil, fmt.Errorf("unexpected path from git: %q", prefix)
		}
	}
	var out []candidate
	for n := len(elems); n >= 0; n-- {
		rel := strings.Join(elems[:n], "/")
		dir := filepath.Join(wtTop, filepath.FromSlash(rel))
		if !within(wtTop, dir) {
			return nil, fmt.Errorf("unexpected path from git: %q", prefix)
		}
		skip := false
		for _, e := range elems[:n] {
			skip = skip || skippedDir(e)
		}
		if !skip {
			out = append(out, candidate{dir, rel})
		}
	}
	return out, nil
}

// findMarker looks for the first of names that exists in dir. The first existing name decides: a regular file is
// found, anything else is an error, so a broken marker never makes the search fall back to a parent's.
func findMarker(dir string, names []string) (string, bool, error) {
	for _, n := range names {
		p := filepath.Join(dir, n)
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if !fi.Mode().IsRegular() {
			return "", false, fmt.Errorf("%q is not a regular file", p)
		}
		return n, true, nil
	}
	return "", false, nil
}

// findProject returns the real path of the nearest candidate that holds one of names, and the candidate itself.
// The candidate is nil when none has one.
func findProject(wtTop, prefix string, names []string) (string, *candidate, error) {
	cands, err := projectCandidates(wtTop, prefix)
	if err != nil {
		return "", nil, err
	}
	for i := range cands {
		_, ok, err := findMarker(cands[i].dir, names)
		if err != nil {
			return "", nil, err
		}
		if !ok {
			continue
		}
		// The candidate is built from real paths already; resolving it again normalizes what git may leave as typed
		// (case and short names on Windows), so the hash in the project name is stable.
		root, err := realPath(cands[i].dir)
		if err != nil {
			return "", nil, err
		}
		if !within(wtTop, root) {
			return "", nil, fmt.Errorf("the project directory %q resolves outside the worktree %q", cands[i].dir, wtTop)
		}
		return root, &cands[i], nil
	}
	return "", nil, nil
}

// subdirsWith lists the direct subdirectories of the cwd's directory and of wtTop that hold one of names, relative to
// wtTop, for the hint in a not-found error. Only plain directories are entered (no links, no Windows junctions);
// vendor, node_modules and .git are skipped. A marker that is itself a link is still followed, as when reading it. It
// is best effort: errors only shorten the list.
func subdirsWith(wtTop, prefix string, names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, base := range []string{filepath.Join(wtTop, filepath.FromSlash(strings.TrimSuffix(prefix, "/"))), wtTop} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Type() != fs.ModeDir || skippedDir(e.Name()) || e.Name() == ".git" {
				continue
			}
			sub := filepath.Join(base, e.Name())
			rel, err := filepath.Rel(wtTop, sub)
			if err != nil || seen[rel] {
				continue
			}
			seen[rel] = true
			if _, ok, err := findMarker(sub, names); err == nil && ok {
				out = append(out, filepath.ToSlash(rel))
			}
		}
	}
	sort.Strings(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// configNotFoundError tells where to run the command, naming the subdirectories that look like the project.
func configNotFoundError(wtTop, prefix string) error {
	msg := fmt.Sprintf("%s not found in this directory or its parents up to the worktree root %q; "+
		"run this in your Laravel project's directory (the one with %s), or run `sail-worktree init` there first",
		configName, wtTop, configName)
	if dirs := subdirsWith(wtTop, prefix, []string{configName}); len(dirs) > 0 {
		msg += fmt.Sprintf("\n%s found in: %s", configName, quoteList(dirs))
	} else if dirs := subdirsWith(wtTop, prefix, composeNames); len(dirs) > 0 {
		msg += fmt.Sprintf("\na compose file found in: %s (run `sail-worktree init` there first)", quoteList(dirs))
	}
	return errors.New(msg)
}

// composeNotFoundError is init's not-found error.
func composeNotFoundError(wtTop, prefix string) error {
	msg := fmt.Sprintf("no compose file (%s) found in this directory or its parents up to the worktree root %q; "+
		"run init in your Laravel project's directory (the one with compose.yaml)", strings.Join(composeNames, ", "), wtTop)
	if dirs := subdirsWith(wtTop, prefix, composeNames); len(dirs) > 0 {
		msg += fmt.Sprintf("\na compose file found in: %s", quoteList(dirs))
	}
	return errors.New(msg)
}

// counterpart returns the main worktree's directory at the same relative path as the project directory. It may not
// exist (the main worktree can be on a branch without it), but it must not resolve outside the main worktree: up
// copies .env from there and Sail sources it.
func counterpart(mainTop, rel string) (string, error) {
	if rel == "" {
		return mainTop, nil
	}
	p := filepath.Join(mainTop, filepath.FromSlash(rel))
	r, err := realPath(p)
	if os.IsNotExist(err) {
		r, err = filepath.Clean(p), nil
	}
	if err != nil {
		return "", err
	}
	if !within(mainTop, r) {
		return "", fmt.Errorf("the main worktree's project directory %q resolves outside the main worktree %q", p, mainTop)
	}
	return r, nil
}
