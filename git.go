package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// gitRaw returns git's stdout untouched (a path may start or end with a space).
func gitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// worktreeRootAndPrefix returns the real path of the worktree root and the directory dir relative to it, both from
// one git call. git derives the prefix from the real cwd, so it does not depend on how the user typed the path (case
// on macOS and Windows, links), which a walk over os.Getwd would.
func worktreeRootAndPrefix(dir string) (top, prefix string, err error) {
	out, err := gitRaw(dir, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		return "", "", err
	}
	top, prefix, err = parseTopAndPrefix(out, runtime.GOOS == "windows")
	if err != nil {
		return "", "", err
	}
	top, err = realPath(top)
	return top, prefix, err
}

// parseTopAndPrefix splits the output of `git rev-parse --show-toplevel --show-prefix`. A name with a newline makes
// the line count wrong and is refused; \r is removed only on Windows, where a Unix name may legitimately end in one.
func parseTopAndPrefix(out string, windows bool) (top, prefix string, err error) {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		return "", "", fmt.Errorf("unexpected output of git rev-parse: %q", out)
	}
	if windows {
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
	}
	if !filepath.IsAbs(lines[0]) {
		return "", "", fmt.Errorf("git rev-parse returned a worktree root that is not absolute: %q", lines[0])
	}
	return lines[0], lines[1], nil
}

func realPath(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(r), nil
}

// mainWorktree returns the path of the first listed worktree (the main one), and whether that entry is a bare
// repository. In a bare layout every checkout is a linked worktree, so there is no main worktree checkout: the bare
// directory still names the repository (the project name's slug), but must never be read as a project directory.
func mainWorktree(dir string) (path string, bare bool, err error) {
	out, z, err := runWorktreeList(func(args ...string) ([]byte, error) {
		out, err := gitRaw(dir, args...) // wraps the *exec.ExitError with %w
		return []byte(out), err
	})
	if err != nil {
		return "", false, err
	}
	p, bare, err := parseMainWorktree(out, z, runtime.GOOS == "windows")
	if err != nil {
		return "", false, err
	}
	// If the real path cannot be resolved (e.g. the main directory is gone and prunable), continue with the cleaned path.
	if r, err := realPath(p); err == nil {
		return r, bare, nil
	}
	return filepath.Clean(p), bare, nil
}

// runWorktreeList runs `worktree list --porcelain -z` through run (which adds the caller's git options, environment
// and timeout) and returns its output and whether it is NUL-separated. Only git 2.36 and later have -z; an older one
// refuses it as an unknown switch, and then the listing is asked once more without it. That is the only case in which
// one listing runs git twice in a directory.
func runWorktreeList(run func(args ...string) ([]byte, error)) (out string, z bool, err error) {
	b, err := run("worktree", "list", "--porcelain", "-z")
	if err == nil {
		return string(b), true, nil
	}
	if !usageRefused(err) {
		return "", false, err
	}
	b, err = run("worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}
	return string(b), false, nil
}

// usageRefused tells whether git exited with 129, its status for a command line it does not accept. A repository
// cannot cause it (a broken configuration exits with 128, an alias cannot replace a builtin, a timeout kill is -1 or
// 1), so it cannot force the line format.
func usageRefused(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 129
}

// parseMainWorktree reads the first record of `git worktree list --porcelain` (with -z when z is set): a path that is
// not absolute (an empty one included) is an error, any other text is returned as git printed it.
func parseMainWorktree(out string, z, windows bool) (path string, bare bool, err error) {
	list := parseWorktreeRecords(out, z, windows)
	if len(list) == 0 || !filepath.IsAbs(list[0].Path) {
		return "", false, fmt.Errorf("cannot determine the main worktree")
	}
	return list[0].Path, list[0].Bare, nil
}

// parseWorktreeRecords splits the output of `git worktree list --porcelain` into records, the main worktree first,
// for mainWorktree and ps (parseWorktreeList). A record starts at a "worktree <path>" field and ends at an empty field
// or the next "worktree" field; fields before the first one are ignored. With z the fields end with NUL (-z), so a
// path or a reason with a newline stays whole; otherwise they are lines, and since git does not quote paths, a path
// with a newline is misread (git before 2.36). Path is git's text untouched (nothing here looks at the filesystem), so
// each caller decides what it accepts. Without z, \r is removed only on Windows, where git may end lines with it;
// elsewhere it may be part of a name. The listing of another repository goes through parseWorktreeList, which
// filters a path before anything looks at it. worktreeInfo is declared in ps.go.
func parseWorktreeRecords(out string, z, windows bool) []worktreeInfo {
	sep := "\n"
	if z {
		sep = "\x00"
	}
	var list []worktreeInfo
	in := false // a record is open, so its keys count
	for _, line := range strings.Split(out, sep) {
		if windows && !z {
			line = strings.TrimSuffix(line, "\r")
		}
		if line == "" {
			in = false
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		if key == "worktree" {
			list = append(list, worktreeInfo{Path: val})
			in = true
			continue
		}
		if !in {
			continue
		}
		cur := &list[len(list)-1]
		switch key {
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "prunable":
			cur.Prunable = true
		}
	}
	return list
}
