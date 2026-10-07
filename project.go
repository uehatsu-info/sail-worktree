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

// The project directory is found from the cwd up to the worktree root, so the Laravel project can live in a
// subdirectory of the repository: the nearest directory with .sail-worktree.json wins, and without one anywhere, the
// nearest directory with artisan and a compose file (lookupProject). init writes into the nearest directory of either
// kind.

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// candidate is a directory the project may be in. rel is its path below the worktree root, "/"-separated and without
// a trailing "/" ("" for the root). marker is the marker name findProject found there.
type candidate struct {
	dir, rel, marker string
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
			out = append(out, candidate{dir: dir, rel: rel})
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

// matcher reports whether dir is a project directory and which marker file made it one. It looks only at files
// directly in dir. findProject stops on an error; the hints ignore errors.
type matcher func(dir string) (marker string, ok bool, err error)

// markerIn matches a directory that holds one of names (see findMarker).
func markerIn(names []string) matcher {
	return func(dir string) (string, bool, error) { return findMarker(dir, names) }
}

// findProject returns the real path of the nearest candidate that match accepts, and the candidate itself.
// The candidate is nil when none matches.
func findProject(wtTop, prefix string, match matcher) (string, *candidate, error) {
	cands, err := projectCandidates(wtTop, prefix)
	if err != nil {
		return "", nil, err
	}
	for i := range cands {
		name, ok, err := match(cands[i].dir)
		if err != nil {
			return "", nil, err
		}
		if !ok {
			continue
		}
		cands[i].marker = name
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

// subdirsWith lists the direct subdirectories of the cwd's directory and of wtTop that match accepts, relative to
// wtTop, for the hint in a not-found error. Only plain directories are entered (no links, no Windows junctions);
// vendor, node_modules and .git are skipped. It is best effort: errors only shorten the list.
func subdirsWith(wtTop, prefix string, match matcher) []string {
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
			if _, ok, err := match(sub); err == nil && ok {
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

// hasConfig matches a directory with .sail-worktree.json.
var hasConfig = markerIn([]string{configName})

const artisanName = "artisan"

// isLaravelProject matches a directory with artisan and a compose file; the marker is the compose file's name.
// artisan is checked first, so a broken compose file in an unrelated directory (docker/, .devcontainer/) does not
// stop the lookup, and an artisan that is not a regular file just means "not a project".
func isLaravelProject(dir string) (string, bool, error) {
	fi, err := os.Stat(filepath.Join(dir, artisanName))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, nil
	}
	return findMarker(dir, composeNames)
}

// configOrLaravel is init's rule: the nearest directory of either kind.
func configOrLaravel(dir string) (string, bool, error) {
	if m, ok, err := hasConfig(dir); err != nil || ok {
		return m, ok, err
	}
	return isLaravelProject(dir)
}

// lookupProject finds the project directory for up, stop and rm. detected is true when there is no
// .sail-worktree.json and the configuration has to be detected from cand.marker, the compose file.
func lookupProject(wtTop, prefix string) (root string, cand *candidate, detected bool, err error) {
	if root, cand, err = findProject(wtTop, prefix, hasConfig); err != nil || cand != nil {
		if cand != nil {
			warnIgnoredNearerProject(wtTop, prefix, root, cand)
		}
		return root, cand, false, err
	}
	if root, cand, err = findProject(wtTop, prefix, isLaravelProject); err != nil || cand != nil {
		return root, cand, true, err
	}
	return "", nil, false, projectNotFoundError(wtTop, prefix)
}

// warnIgnoredNearerProject warns when a .sail-worktree.json further up wins over a nearer Laravel project. It is best
// effort: errors only mean no warning.
func warnIgnoredNearerProject(wtTop, prefix, root string, chosen *candidate) {
	cands, err := projectCandidates(wtTop, prefix)
	if err != nil {
		return
	}
	for _, c := range cands {
		if c.rel == chosen.rel {
			return
		}
		if _, ok, err := isLaravelProject(c.dir); err == nil && ok {
			fmt.Fprintf(stderr, "warning: using %s in %q; the nearer Laravel project %q is ignored (run `sail-worktree init` in it to use it)\n", configName, root, c.dir)
			return
		}
	}
}

// projectNotFoundError tells where to run the commands. The hints name subdirectories that would be found, and
// directories on the way up that have only half of a Laravel project.
func projectNotFoundError(wtTop, prefix string) error {
	msg := fmt.Sprintf("no Laravel project found in this directory or its parents up to the worktree root %q\n"+
		"looked for %s, or a compose file next to %s; run this in your Laravel project's directory", wtTop, configName, artisanName)
	if dirs := subdirsWith(wtTop, prefix, configOrLaravel); len(dirs) > 0 {
		msg += fmt.Sprintf("\na project found in: %s", quoteList(dirs))
	}
	if cands, err := projectCandidates(wtTop, prefix); err == nil {
		for _, c := range cands {
			artisan, aerr := os.Stat(filepath.Join(c.dir, artisanName))
			hasArtisan := aerr == nil && artisan.Mode().IsRegular()
			compose, ok, cerr := findMarker(c.dir, composeNames)
			switch {
			case cerr == nil && ok && !hasArtisan:
				msg += fmt.Sprintf("\nfound %q, but no %s next to it", filepath.Join(c.dir, compose), artisanName)
			case cerr == nil && !ok && hasArtisan:
				msg += fmt.Sprintf("\nfound %s in %q, but no compose file (%s) next to it", artisanName, c.dir, strings.Join(composeNames, ", "))
			}
		}
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
