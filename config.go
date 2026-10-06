package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const configName = ".sail-worktree.json"

// Config is the project-level setting. It is committed to the repository and shared by all worktrees.
type Config struct {
	Compose  string    `json:"compose"`
	PortVars []PortVar `json:"port_vars"`
}

func loadConfig(root string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(root, configName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found; run `sail-worktree init` first", configName)
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configName, err)
	}
	if unsafeComposePath(c.Compose) {
		return nil, fmt.Errorf("compose (%q) in %s must be a relative path inside the project directory", c.Compose, configName)
	}
	return &c, nil
}

// unsafeComposePath reports whether the compose value is not a relative path inside the project directory (the one
// with .sail-worktree.json). It is passed to rm's -f, so besides empty, absolute and ".." paths it also rejects forms
// that point at a drive or a server on Windows ("C:x", "\\srv\x") and rooted paths without a drive letter ("/x",
// "\x": filepath.IsAbs is false for them).
// It only reads the string; composeInsideProject checks where the file really is.
func unsafeComposePath(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true
	}
	cl := filepath.Clean(p)
	return cl == "." || cl == ".." || strings.HasPrefix(cl, ".."+string(filepath.Separator))
}

// composeInsideProject resolves the compose file under root (the project directory, a real path) through every link
// and returns the real path that rm passes to -f: down -v cannot be undone, so a link that leaves the project directory
// must not choose the file.
// Only rm calls it; up, stop and init do not run docker with -f and keep findMarker's os.Stat.
// Limits: EvalSymlinks does not follow Windows junctions (Go 1.23+), so they are not detected, and a link swapped
// after the check is not caught (best effort).
func composeInsideProject(root, rel string) (string, error) {
	joined := filepath.Join(root, rel)
	resolved, err := realPath(joined)
	if err != nil {
		return "", fmt.Errorf("compose file not found: %+q: %w", joined, err)
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("compose file %+q resolves outside the project directory (%+q); replace the link with a real file or a link whose target is inside the project directory", rel, resolved)
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("compose file %+q is not a regular file (%+q); point compose at a regular file inside the project directory", rel, resolved)
	}
	return resolved, nil
}

// Registry records the ports assigned to each worktree (shared by all projects of the user). The keys are project
// directories; the JSON name "worktrees" is kept for files written by older versions.
type Registry struct {
	Worktrees map[string]map[string]int `json:"worktrees"`
}

func registryPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sail-worktree", "registry.json"), nil
}

func loadRegistry() (*Registry, error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	r := &Registry{Worktrees: map[string]map[string]int{}}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", p, err)
	}
	if r.Worktrees == nil {
		r.Worktrees = map[string]map[string]int{}
	}
	return r, nil
}

func (r *Registry) save() error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// migrate merges keys that are aliases of root (recorded by an older version under a symlinked path) into root.
// It only changes memory; the caller saves on success. An existing entry for root wins; otherwise the entry of the
// alias with the smallest key (lexicographically) is adopted. The ports of dropped aliases are not left in used.
// Keys whose path cannot be resolved (it no longer exists) are left alone.
func (r *Registry) migrate(root string) {
	var aliases []string
	for k := range r.Worktrees {
		if k == root {
			continue
		}
		if rp, err := realPath(k); err == nil && rp == root {
			aliases = append(aliases, k)
		}
	}
	sort.Strings(aliases)
	for _, k := range aliases {
		if _, ok := r.Worktrees[root]; !ok {
			r.Worktrees[root] = r.Worktrees[k]
		}
		delete(r.Worktrees, k)
	}
}

// used returns the set of ports assigned to worktrees other than except.
func (r *Registry) used(except string) map[int]bool {
	m := map[int]bool{}
	for wt, ports := range r.Worktrees {
		if wt == except {
			continue
		}
		for _, p := range ports {
			m[p] = true
		}
	}
	return m
}
