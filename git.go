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

// worktreeRoot は dir を含むワークツリーのルートを返す。
func worktreeRoot(dir string) (string, error) {
	return gitOut(dir, "rev-parse", "--show-toplevel")
}

// mainWorktree は最初に列挙されるワークツリー(メイン)のパスを返す。
func mainWorktree(dir string) (string, error) {
	out, err := gitOut(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			return filepath.Clean(p), nil
		}
	}
	return "", fmt.Errorf("メインワークツリーを特定できません")
}
