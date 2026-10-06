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

// mainWorktree returns the path of the first listed worktree (the main one).
func mainWorktree(dir string) (string, error) {
	out, err := gitOut(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			// If the real path cannot be resolved (e.g. the main directory is gone and prunable), continue with the cleaned path.
			if r, err := realPath(p); err == nil {
				return r, nil
			}
			return filepath.Clean(p), nil
		}
	}
	return "", fmt.Errorf("cannot determine the main worktree")
}
