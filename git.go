package main

import (
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
	out, err := gitRaw(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}
	p, bare, err := parseMainWorktree(out, runtime.GOOS == "windows")
	if err != nil {
		return "", false, err
	}
	// If the real path cannot be resolved (e.g. the main directory is gone and prunable), continue with the cleaned path.
	if r, err := realPath(p); err == nil {
		return r, bare, nil
	}
	return filepath.Clean(p), bare, nil
}

// parseMainWorktree reads the first record of `git worktree list --porcelain`: an empty path is an error, any other
// text is returned as git printed it.
func parseMainWorktree(out string, windows bool) (path string, bare bool, err error) {
	list := parseWorktreeRecords(out, windows)
	if len(list) == 0 || list[0].Path == "" {
		return "", false, fmt.Errorf("cannot determine the main worktree")
	}
	return list[0].Path, list[0].Bare, nil
}

// parseWorktreeRecords splits the output of `git worktree list --porcelain` into records, the main worktree first,
// for mainWorktree and ps (parseWorktreeList). A record starts at a "worktree <path>" line and ends at a blank line or
// the next "worktree" line; lines before the first one are ignored. Path is git's text untouched (nothing here looks
// at the filesystem), so each caller decides what it accepts. git does not quote it, so a path with a newline is
// misread; -z would fix that, but only git 2.36 and later have it. \r is removed only on Windows, where git may end
// lines with it; elsewhere it may be part of a name. The listing of another repository goes through parseWorktreeList,
// which filters a path before anything looks at it. worktreeInfo is declared in ps.go.
func parseWorktreeRecords(out string, windows bool) []worktreeInfo {
	var list []worktreeInfo
	in := false // a record is open, so its keys count
	for _, line := range strings.Split(out, "\n") {
		if windows {
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
