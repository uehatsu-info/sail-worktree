package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var composePortRe = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*PORT):-?(\d+)\}`)

// PortVar is a port environment variable detected in compose.yml.
type PortVar struct {
	Name    string `json:"name"`
	Default int    `json:"default"`
}

// detectPortVars detects the port variables, in order of appearance, from the ports entries
// of compose.yml (e.g. - '${APP_PORT:-80}:80').
func detectPortVars(composeYAML string) []PortVar {
	var vars []PortVar
	seen := map[string]bool{}
	for _, line := range strings.Split(composeYAML, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "-") || strings.HasPrefix(t, "#") {
			continue
		}
		for _, m := range composePortRe.FindAllStringSubmatch(t, -1) {
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			var d int
			for _, c := range m[2] {
				d = d*10 + int(c-'0')
			}
			vars = append(vars, PortVar{Name: m[1], Default: d})
		}
	}
	return vars
}

// envFile is a .env file that can be read and written as KEY=VALUE while keeping its line structure.
type envFile struct {
	lines []string
}

func readEnv(path string) (*envFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseEnv(f)
}

// readEnvIfRegular reads path only if it is a regular file (used for stop's warning).
// Links, FIFOs and devices are not read and ok is false. So that open does not block even if the path is swapped
// for a FIFO between Lstat and open, it opens with O_NONBLOCK (which does not affect reading a regular file) and
// Stats the opened fd again. At most 1MiB is read.
func readEnvIfRegular(path string) (*envFile, bool) {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNonBlock|openNoFollow, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	e, err := parseEnv(io.LimitReader(f, 1<<20))
	return e, err == nil
}

func parseEnv(r io.Reader) (*envFile, error) {
	e := &envFile{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		e.lines = append(e.lines, sc.Text())
	}
	return e, sc.Err()
}

func keyOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	if rest, ok := strings.CutPrefix(t, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		t = strings.TrimSpace(rest)
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(k), true
}

func (e *envFile) Get(key string) (string, bool) {
	for _, l := range e.lines {
		if k, ok := keyOf(l); ok && k == key {
			_, v, _ := strings.Cut(l, "=")
			return strings.Trim(strings.TrimSpace(v), `"'`), true
		}
	}
	return "", false
}

// Set replaces the first line of the key and removes later duplicates (Sail uses the last value and Laravel's
// Dotenv the first, so leftover duplicates would make them disagree). If the key is missing it is appended.
func (e *envFile) Set(key, value string) {
	out := e.lines[:0:0]
	found := false
	for _, l := range e.lines {
		if k, ok := keyOf(l); ok && k == key {
			if found {
				continue
			}
			found = true
			l = key + "=" + value
		}
		out = append(out, l)
	}
	if !found {
		out = append(out, key+"="+value)
	}
	e.lines = out
}

// Write writes the .env file, after checkOwnEnv. A new file is created with mode 0600 (it holds APP_KEY and
// DB_PASSWORD); the mode of an existing file is left unchanged.
func (e *envFile) Write(path string) error {
	if err := checkOwnEnv(path); err != nil {
		return err
	}
	s := strings.Join(e.lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	// O_TRUNC would truncate before the check, so check after opening and then Truncate.
	// O_NOFOLLOW (unix) refuses a symlink even if the path is swapped for one between Lstat and open.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || hasMultipleLinks(fi)) {
		err = fmt.Errorf("%s is not a regular file or is hard-linked to another file", path)
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteString(s)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkOwnEnv checks that the worktree's own .env may be read and written.
// It refuses a symbolic link (which would rewrite the target, e.g. the main worktree's .env) and a hard link.
// A missing file is fine (it will be created).
func checkOwnEnv(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link; replace it with a real file so that the link target is not rewritten", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if hasMultipleLinks(fi) {
		return fmt.Errorf("%s is hard-linked to another file; replace it with a real file so that the other file (e.g. the main worktree's .env) is not rewritten", path)
	}
	return nil
}

// Keys that, when set in .env, can point at another compose file or project. The commands refuse different sets
// (COMPOSE_* in the process environment are all removed by cleanEnv, which is a separate mechanism).
var (
	// Keys up refuses: with them compose would not read the ports and project name up writes to .env, or would use
	// another compose file. COMPOSE_PROFILES is allowed: it only starts more services and does not point elsewhere.
	upOverrideKeys = []string{"COMPOSE_FILE", "COMPOSE_ENV_FILES", "SAIL_FILES"}
	// Keys rm refuses: rm cannot be undone, so it also refuses COMPOSE_PROFILES, which can change the set of services.
	rmOverrideKeys = append(append([]string{}, upOverrideKeys...), "COMPOSE_PROFILES")
)

// overrideKey returns the first of keys that is set in .env.
func (e *envFile) overrideKey(keys []string) (string, bool) {
	for _, k := range keys {
		if _, ok := e.Get(k); ok {
			return k, true
		}
	}
	return "", false
}
