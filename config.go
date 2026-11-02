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
	"strings"
	"time"
	"unicode/utf8"
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
	if err := validatePortVars(c.PortVars); err != nil {
		return nil, err
	}
	return &c, nil
}

// validatePortVars refuses entries that up cannot write as NAME=port: a name with "=", a newline or a space would add
// or change other lines of .env, which Sail sources as shell code, and a default outside 1-65535 would start the port
// search at 1 or bind :0. Names are held to shellName, not to the detection rule, because a hand-added variable may be
// any name the compose file uses. A repeated name is refused too: up would assign two ports and Set would keep only
// the first line, leaving the other port recorded but unused. Names are compared case-insensitively, as for a reserved
// name. Detected variables always pass: detection yields only names ending in PORT, and no reserved name does.
func validatePortVars(vars []PortVar) error {
	first := map[string]int{}
	for i, v := range vars {
		if !shellName.MatchString(v.Name) {
			return fmt.Errorf("port_vars[%d] (%q): name in %s must be a shell variable name (letters, digits and _, not starting with a digit)", i, v.Name, configName)
		}
		// %+q for an untrusted name; after shellName it prints as %q would.
		if why, ok := reservedPortVar(v.Name); ok {
			return fmt.Errorf("port_vars[%d] (%+q): name in %s is reserved: %s; rename the variable in the compose file and here", i, v.Name, configName, why)
		}
		key := strings.ToUpper(v.Name)
		if j, ok := first[key]; ok {
			return fmt.Errorf("port_vars[%d] (%+q): name in %s repeats port_vars[%d] (%+q) (names are compared case-insensitively); list each variable once", i, v.Name, configName, j, vars[j].Name)
		}
		first[key] = i
		if !validPort(v.Default) {
			return fmt.Errorf("port_vars[%d] (%q): default (%d) in %s must be 1 to 65535 (a missing default is 0)", i, v.Name, v.Default, configName)
		}
	}
	return nil
}

// reservedPortVar reports why name cannot be a port variable: up writes NAME=port into .env, Sail sources it, and
// cleanEnv drops every port variable name from the environment of sail and docker. Names are compared
// case-insensitively, as cleanEnv drops them.
func reservedPortVar(name string) (string, bool) {
	why, ok := reservedNames[strings.ToUpper(name)]
	return why, ok
}

const (
	upWrites       = "up writes it"
	overridesUp    = "it would override the compose files or project that up uses"
	composeSetting = "compose reads it as its own setting"
	dockerSetting  = "docker reads it from the environment"
	shellNeeds     = "the shell and sail need it"
	sailUses       = "Sail's script uses it"
)

// reservedNames holds, upper-case, the keys up writes itself (keep it in step with up), SAIL_FILES, the shell and Sail
// names whose loss breaks sail, and the COMPOSE_* and DOCKER_* settings that docker compose and the docker CLI
// document (checked 2026-11): https://docs.docker.com/compose/how-tos/environment-variables/envvars/ (with the names
// it lists as unsupported in Compose V2, which an older docker-compose still reads) and
// https://docs.docker.com/reference/cli/docker/#environment-variables (plus DOCKER_CONTENT_TRUST*, which older CLIs
// read). Other COMPOSE_* and DOCKER_* names, such as DOCKER_WEB_PORT, are allowed: v0.8.1 handled them (#74). None ends
// in PORT, so detection never yields one; adding one makes detectConfig's refusal and teardownConfig reachable again,
// which TestNoReservedNameIsDetected points out.
var reservedNames = map[string]string{
	"COMPOSE_PROJECT_NAME": upWrites,
	"SESSION_COOKIE":       upWrites,
	"APP_URL":              upWrites,
	sanctumKey:             upWrites,
	"SAIL_FILES":           overridesUp,
	"PATH":                 shellNeeds,
	"HOME":                 shellNeeds,
	"SHELL":                shellNeeds,
	"USER":                 shellNeeds,
	"PWD":                  shellNeeds,
	"IFS":                  shellNeeds,
	// Sail's script reads these; APP_ENV also chooses the .env file it sources.
	"APP_SERVICE": sailUses,
	"WWWUSER":     sailUses,
	"WWWGROUP":    sailUses,
	"APP_ENV":     sailUses,

	"COMPOSE_FILE":                  overridesUp,
	"COMPOSE_ENV_FILES":             overridesUp,
	"COMPOSE_PROFILES":              overridesUp,
	"COMPOSE_PATH_SEPARATOR":        overridesUp,
	"COMPOSE_DISABLE_ENV_FILE":      overridesUp,
	"COMPOSE_ANSI":                  composeSetting,
	"COMPOSE_API_VERSION":           composeSetting,
	"COMPOSE_BAKE":                  composeSetting,
	"COMPOSE_CONVERT_WINDOWS_PATHS": composeSetting,
	"COMPOSE_DOCKER_CLI_BUILD":      composeSetting,
	"COMPOSE_EXPERIMENTAL":          composeSetting,
	"COMPOSE_FORCE_WINDOWS_HOST":    composeSetting,
	"COMPOSE_HTTP_TIMEOUT":          composeSetting,
	"COMPOSE_IGNORE_ORPHANS":        composeSetting,
	"COMPOSE_INTERACTIVE_NO_CLI":    composeSetting,
	"COMPOSE_MENU":                  composeSetting,
	"COMPOSE_PARALLEL_LIMIT":        composeSetting,
	"COMPOSE_PROGRESS":              composeSetting,
	"COMPOSE_REMOVE_ORPHANS":        composeSetting,
	"COMPOSE_STATUS_STDOUT":         composeSetting,
	"COMPOSE_TLS_VERSION":           composeSetting,
	// DOCKER_HOST, DOCKER_CONTEXT, DOCKER_CONFIG and the TLS names choose the daemon docker talks to (rm runs
	// "down -v").
	"DOCKER_API_VERSION":          dockerSetting,
	"DOCKER_BUILDKIT":             dockerSetting,
	"DOCKER_CERT_PATH":            dockerSetting,
	"DOCKER_CONFIG":               dockerSetting,
	"DOCKER_CONTENT_TRUST":        dockerSetting,
	"DOCKER_CONTENT_TRUST_SERVER": dockerSetting,
	"DOCKER_CONTEXT":              dockerSetting,
	"DOCKER_CUSTOM_HEADERS":       dockerSetting,
	"DOCKER_DEFAULT_PLATFORM":     dockerSetting,
	"DOCKER_HIDE_LEGACY_COMMANDS": dockerSetting,
	"DOCKER_HOST":                 dockerSetting,
	"DOCKER_TLS":                  dockerSetting,
	"DOCKER_TLS_VERIFY":           dockerSetting,
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
	cfg := &Config{Compose: compose, PortVars: detectPortVars(string(b))}
	for _, v := range cfg.PortVars {
		// Not reached today (see reservedNames); it keeps init from writing a file that loadConfig refuses.
		if why, ok := reservedPortVar(v.Name); ok {
			return nil, &reservedPortVarError{
				msg:      fmt.Sprintf("port variable %+q in %q cannot be used: %s; rename it in the compose file", v.Name, compose, why),
				detected: cfg,
			}
		}
	}
	return cfg, nil
}

// reservedPortVarError is detectConfig's refusal of a reserved name. It carries the detected configuration for stop
// and rm (ctx.teardownConfig), which do not write port variables. No reserved name ends in PORT, so it is a guard for
// a future *PORT entry (see reservedNames). Letting stop and rm through is safe only because detection yields names
// ending in PORT, never one such as DOCKER_HOST or PATH.
type reservedPortVarError struct {
	msg      string
	detected *Config
}

func (e *reservedPortVarError) Error() string { return e.msg }

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
	// Mains records the main worktrees, which up never assigns ports to, keyed by their project directory, so that
	// their ports are kept free too. It is a cache: an older version drops it when it saves, and up and check record
	// it again.
	Mains map[string]MainRecord `json:"mains,omitempty"`
}

// MainRecord is what the registry knows about a main worktree: the ports it uses, the compose defaults of its port
// variables (used when its .env no longer sets one) and its linked worktrees that have a port record.
type MainRecord struct {
	Ports     map[string]int `json:"ports"`
	Defaults  map[string]int `json:"defaults"`
	Worktrees []string       `json:"worktrees,omitempty"`
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
	r := &Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{}}
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
	if r.Mains == nil {
		r.Mains = map[string]MainRecord{}
	}
	return r, nil
}

// recordMain stores the ports of the main worktree main, read for the port variables of the run's configuration
// (defaults' keys). It reports whether the record changed. worktree, when not empty, is added to its linked worktrees.
//   - ok=false (its .env could not be read, or recordMainRead found a newer record) keeps the ports and defaults of an
//     existing record, also for inMain; a new record then reserves nothing.
//   - inMain (check runs in the main worktree itself) replaces them: the main's own configuration decides, so a
//     variable that left it is dropped here.
//   - Otherwise the run may be on a branch whose configuration lacks a variable the main still uses, so a variable of
//     the old record's defaults that the run does not know keeps its recorded default and port. They are not re-read
//     here: recordMain reads no .env, and check drops its pre-lock read of the current main. A check run outside the
//     repository's project directory re-reads them (refreshMains), and one run in the main worktree re-records or
//     drops them.
func (r *Registry) recordMain(main, worktree string, ports, defaults map[string]int, ok, inMain bool) bool {
	if r.Mains == nil {
		r.Mains = map[string]MainRecord{}
	}
	old, had := r.Mains[main]
	rec := MainRecord{Ports: old.Ports, Defaults: old.Defaults, Worktrees: append([]string(nil), old.Worktrees...)}
	replaced := ok || !had
	if replaced {
		rec.Ports, rec.Defaults = maps.Clone(ports), maps.Clone(defaults)
	}
	if rec.Ports == nil {
		rec.Ports = map[string]int{}
	}
	if rec.Defaults == nil {
		rec.Defaults = map[string]int{}
	}
	if replaced && !inMain {
		for name, def := range old.Defaults {
			if _, known := defaults[name]; known {
				continue
			}
			rec.Defaults[name] = def
			if p, recorded := old.Ports[name]; recorded {
				rec.Ports[name] = p
			}
		}
	}
	if worktree != "" && !slices.Contains(rec.Worktrees, worktree) {
		rec.Worktrees = append(rec.Worktrees, worktree)
		sort.Strings(rec.Worktrees)
	}
	r.Mains[main] = rec
	return !had || !maps.Equal(old.Ports, rec.Ports) || !maps.Equal(old.Defaults, rec.Defaults) ||
		!slices.Equal(old.Worktrees, rec.Worktrees)
}

// forgetWorktree removes worktree from the linked worktrees of main. The main's record itself stays: its ports still
// have to be kept free.
func (r *Registry) forgetWorktree(main, worktree string) {
	rec, ok := r.Mains[main]
	if !ok {
		return
	}
	rec.Worktrees = slices.DeleteFunc(append([]string(nil), rec.Worktrees...), func(w string) bool { return w == worktree })
	r.Mains[main] = rec
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

// renameRetries is how often a replace is retried (20 ms apart, so about a second) on Windows, where it fails while
// another process (ps, ports or status reading the registry) has the target open.
var renameRetries = 50

// writeFileAtomic replaces path with data so that a reader sees the old or the new content, never a partial file: it
// writes a temporary file next to the target and renames it over. A symbolic link at path is resolved first, so the
// link keeps pointing at the file that is replaced; a hard link is broken. The mode of an existing file is kept
// (0644 for a new one, whatever the umask).
func writeFileAtomic(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			path = r
		} else if t, err := os.Readlink(path); err == nil { // a dangling link: create what it points at
			if !filepath.IsAbs(t) {
				t = filepath.Join(filepath.Dir(path), t)
			}
			path = t
		}
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
	// Windows has no Unix modes (Chmod only toggles the read-only attribute), so its error is not a failure.
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

// validKey reports whether a registry key may be used as a directory: an absolute, clean path, and on Windows a path
// that starts with a drive letter and whose links all lead to drive-letter paths (keyLinksOK; this part reads the
// filesystem). Keys are untrusted, and on Windows looking at a UNC or device path (\\host\share, \\?\, \??\, \\.\) can
// open a network connection that sends the user's credentials, also when a link in the middle of a key leads there.
// The string checks come first, so a UNC key is never walked.
func validKey(k string) bool {
	return k != "" && filepath.IsAbs(k) && filepath.Clean(k) == k && windowsDriveKey(k, runtime.GOOS) && keyLinksOK(k)
}

// keyLinksOK is replaced by tests, so that every system can check how a refused key is treated.
var keyLinksOK = func(k string) bool {
	return runtime.GOOS != "windows" || linksStayLocal(k, osLinkFS, driveTarget)
}

// driveTarget is the rule for a link target on Windows: a drive-letter path, like a key.
func driveTarget(p string) bool { return filepath.IsAbs(p) && windowsDriveKey(p, "windows") }

// linkFS is what linksStayLocal reads; tests replace it.
type linkFS struct {
	lstat    func(string) (os.FileInfo, error)
	readlink func(string) (string, error)
}

var osLinkFS = linkFS{lstat: os.Lstat, readlink: os.Readlink}

// maxKeyLinks bounds the links followed for one key, as EvalSymlinks does, so that a loop ends.
const maxKeyLinks = 255

// linksStayLocal reports whether every link on the way to k, and on the way through what the links lead to, has a
// target that ok accepts. It walks from the root with Lstat, so a link is read before anything goes through it, and
// stops at the first element that does not exist (nothing further can be followed). Windows follows a junction when
// a path is opened, Lstat'ed or used as git's cwd, so junctions are checked here although EvalSymlinks (realPath) has
// not followed them since Go 1.23: Go reports them as ModeIrregular without ModeDir. A ModeDir|ModeIrregular element is
// a reparse point that does not redirect the path (a cloud placeholder folder, say) and is walked like a directory.
// An element that cannot be looked at for another reason, or a link that cannot be read, refuses the key. A target is
// walked element by element from where the link is, as EvalSymlinks does, never cleaned first: a cleaned x\..\y
// would skip x, which may itself be a link; a ".." after a name refuses the key (dotDotAfterName). An absolute target
// must pass ok, and the walk goes on from its root.
// Limits: a link created or swapped after the walk is not caught, and a drive letter that is itself mapped to a network
// share is the user's own setup. os.Readlink returns a target such as \\?\C:\x as it is, so it is refused.
func linksStayLocal(k string, fs linkFS, ok func(string) bool) bool {
	vol := filepath.VolumeName(k)
	cur, rest, links := vol+string(filepath.Separator), splitPath(k[len(vol):]), 0
	for len(rest) > 0 {
		e := rest[0]
		rest = rest[1:]
		switch e {
		case ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, e)
		fi, err := fs.lstat(next)
		if os.IsNotExist(err) {
			return true
		}
		if err != nil {
			return false
		}
		m := fi.Mode()
		if m&os.ModeSymlink == 0 && (m&os.ModeIrregular == 0 || m.IsDir()) {
			cur = next
			continue
		}
		if links++; links > maxKeyLinks {
			return false
		}
		t, err := fs.readlink(next)
		if err != nil || t == "" {
			return false
		}
		if tv := filepath.VolumeName(t); tv != "" || os.IsPathSeparator(t[0]) {
			if !ok(t) {
				return false
			}
			cur, t = tv+string(filepath.Separator), t[len(tv):]
		}
		te := splitPath(t)
		if dotDotAfterName(te) {
			return false
		}
		rest = append(te, rest...)
	}
	return true
}

// splitPath splits p at its separators and drops empty elements.
func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool {
		// A rune is not a byte: U+015C would pass as '\\' if it were cut to uint8.
		return r < utf8.RuneSelf && os.IsPathSeparator(uint8(r))
	})
}

// dotDotAfterName reports whether a ".." follows a name in a link target. EvalSymlinks (and so linksStayLocal) goes
// through the name first, while Windows drops it lexically, so the two would resolve different paths.
func dotDotAfterName(elems []string) bool {
	named := false
	for _, e := range elems {
		switch {
		case e == "..":
			if named {
				return true
			}
		case e != ".":
			named = true
		}
	}
	return false
}

// windowsDriveKey reports whether k starts with a drive letter, a colon and a separator, which is the only form
// accepted on Windows (an allow-list: Go parses several device prefixes, so refusing known forms would miss some).
// On other systems it is always true.
func windowsDriveKey(k, goos string) bool {
	if goos != "windows" {
		return true
	}
	return len(k) >= 3 && isASCIILetter(k[0]) && k[1] == ':' && (k[2] == '\\' || k[2] == '/')
}

func isASCIILetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }

// usableDir reports whether a registry key can be used as a local directory: validKey first, so that an invalid key
// (on Windows a UNC or device path) is never looked at, then isDir (a link as its last element is not followed).
func usableDir(k string) bool { return validKey(k) && isDir(k) }

// migrate merges keys that are aliases of root (recorded by an older version under a symlinked path) into root.
// It only changes memory; the caller saves on success. An existing entry for root wins; otherwise the entry of the
// alias with the smallest key (lexicographically) is adopted. The ports of dropped aliases are not left in used.
// Keys whose path cannot be resolved (it no longer exists) and keys that fail validKey are left alone. Aliases of root
// in the worktree list of main, the only record root can be listed in, are replaced by root as well (a list that holds
// none is left as it is); other mains' lists are not looked at, since each entry may be resolved under the lock.
func (r *Registry) migrate(root, main string) {
	var aliases []string
	for k := range r.Worktrees {
		if k == root || !validKey(k) {
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
	if rec, ok := r.Mains[main]; ok {
		ws := make([]string, 0, len(rec.Worktrees))
		changed, seen := false, false
		for _, w := range rec.Worktrees {
			if w != root && foldKey(w) == root {
				w, changed = root, true
			}
			if w == root {
				if seen {
					continue
				}
				seen = true
			}
			ws = append(ws, w)
		}
		if changed {
			sort.Strings(ws)
			rec.Worktrees = ws
			r.Mains[main] = rec
		}
	}
}

// used returns the set of ports assigned to worktrees other than except, and the ports of every main worktree.
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
	for _, rec := range r.Mains {
		for _, p := range rec.Ports {
			m[p] = true
		}
	}
	return m
}

// replacedMain returns the record that replaces the record of main worktree k, and why, or "" when none does. Main
// keys are never folded, so a record that a move left behind would otherwise conflict with the new one for ever. Only
// a live record replaces one (an alias whose real path has its own record, or a directory that is no longer a project
// with a project's record below it), so a main without a second record keeps its ports. usableDir comes first, so
// validKey does too.
func (r *Registry) replacedMain(k string) (by, why string) {
	if !usableDir(k) {
		return "", ""
	}
	// A failure keeps the record: keeping a stale record only reserves ports, removing a live one frees them.
	rp, err := realPath(k)
	if err != nil {
		return "", ""
	}
	if rp != k {
		if _, ok := r.Mains[rp]; ok && usableDir(rp) {
			return rp, fmt.Sprintf("its real path %q has its own record", rp)
		}
		return "", ""
	}
	if projectMarkers(k) {
		return "", ""
	}
	for _, m := range slices.Sorted(maps.Keys(r.Mains)) {
		if m == k || !within(k, m) || !usableDir(m) {
			continue
		}
		if mp, err := realPath(m); err == nil && mp == m && projectMarkers(m) {
			return m, fmt.Sprintf("it is no longer a project directory and main worktree %q below it is recorded", m)
		}
	}
	return "", ""
}

// projectMarkers reports whether dir holds .sail-worktree.json, or artisan and a compose file, the files that make a
// project directory (hasConfig, isLaravelProject). Unlike those it uses Lstat: a marker that is a link counts and is
// not followed, because validKey walks only the key and on Windows a link here could lead to a network path. An error
// other than not-exist counts as present, so a record is kept when in doubt.
func projectMarkers(dir string) bool {
	present := func(name string) bool {
		_, err := os.Lstat(filepath.Join(dir, name))
		return !os.IsNotExist(err)
	}
	if present(configName) {
		return true
	}
	return present(artisanName) && slices.ContainsFunc(composeNames, present)
}
