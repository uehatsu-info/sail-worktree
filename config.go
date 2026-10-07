package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const configName = ".sail-worktree.json"

// Config is the project-level setting: read from .sail-worktree.json (optional, usually committed and shared by all
// worktrees) or detected from the compose file.
type Config struct {
	Compose  string    `json:"compose"`
	PortVars []PortVar `json:"port_vars"`
}

func loadConfig(root string) (*Config, error) {
	b, err := readSmallFile(filepath.Join(root, configName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s disappeared from %q", configName, root)
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

// maxSmallFile bounds what readSmallFile reads from a compose file or .sail-worktree.json.
const maxSmallFile = 1 << 20

// readSmallFile reads a regular file of at most maxSmallFile bytes. Links are followed (a linked compose file works for
// up and stop as before; rm checks where it leads), but the open does not block on a FIFO swapped in after the lookup,
// and a larger file is an error rather than being cut, which could drop port variables silently.
func readSmallFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openNonBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSmallFile {
		return nil, fmt.Errorf("%q is larger than %d bytes", path, maxSmallFile)
	}
	return b, nil
}

// detectConfig builds the configuration from the compose file name (relative to root) when there is no
// .sail-worktree.json. It returns plain errors; callers add advice.
func detectConfig(root, compose string) (*Config, error) {
	b, err := readSmallFile(filepath.Join(root, compose))
	if err != nil {
		return nil, err
	}
	return &Config{Compose: compose, PortVars: detectPortVars(string(b))}, nil
}

// unsafeComposePath reports whether the compose value is not a relative path inside the project directory. It is
// passed to rm's -f, so besides empty, absolute and ".." paths it also rejects forms that point at a drive or a server
// on Windows ("C:x", "\\srv\x") and rooted paths without a drive letter ("/x", "\x": filepath.IsAbs is false for
// them).
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
// Only rm calls it: up, stop and init do not run docker with -f (they stat and read the compose file to find the
// project and detect port variables, following links).
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
		return "", fmt.Errorf("compose file %+q is not a regular file (%+q); use a regular file inside the project directory", rel, resolved)
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
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, append(b, '\n'))
}

// renameRetries is how often a replace is retried on Windows, where it fails while another process (ps, ports or
// status reading the registry) has the target open.
var renameRetries = 50

// writeFileAtomic replaces path with data so that a reader sees the old or the new content, never a partial file: it
// writes a temporary file next to the target and renames it over. A symbolic link at path is resolved first, so the
// link keeps pointing at the file that is replaced; a hard link is broken. The mode of an existing file is kept
// (0644 for a new one).
func writeFileAtomic(path string, data []byte) error {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".registry-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	err = os.Rename(tmp, path)
	for i := 0; err != nil && runtime.GOOS == "windows" && i < renameRetries; i++ {
		time.Sleep(20 * time.Millisecond)
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
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
