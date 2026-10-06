package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// worktreeRoot returns the root of the worktree that contains dir.
// Symlinks are resolved so that the hash in the project name does not depend on the path used to get here.
func worktreeRoot(dir string) (string, error) {
	out, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return realPath(out)
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
