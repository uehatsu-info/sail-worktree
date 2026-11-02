package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicReplacesAndKeepsMode(t *testing.T) {
	dir := realTempDir(t)
	p := filepath.Join(dir, "registry.json")
	if err := writeFileAtomic(p, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("new file mode = %v", fi.Mode().Perm())
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFileAtomic(p, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two\n" {
		t.Errorf("content = %q", b)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("existing mode was not kept: %v", fi.Mode().Perm())
		}
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "registry.json" {
		t.Errorf("directory holds %v", names)
	}
}

func TestWriteFileAtomicFailureLeavesTargetAndNoTempFile(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "registry.json")
	writeFile(t, filepath.Join(target, "keep"), "x") // a non-empty directory cannot be replaced by a file
	old := renameRetries
	renameRetries = 2
	t.Cleanup(func() { renameRetries = old })
	if err := writeFileAtomic(target, []byte("data")); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "registry.json" {
		t.Errorf("directory holds %v", names)
	}
	if b, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(b) != "x" {
		t.Errorf("the target directory was damaged: %q, %v", b, err)
	}
}

func TestWriteFileAtomicWritesThroughASymlink(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "dotfiles", "registry.json")
	writeFile(t, dest, "old\n")
	link := filepath.Join(dir, "registry.json")
	symlinkOrSkip(t, dest, link)
	if err := writeFileAtomic(link, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new\n" {
		t.Errorf("the link target holds %q", b)
	}
	if names := dirNames(t, filepath.Join(dir, "dotfiles")); len(names) != 1 {
		t.Errorf("target directory holds %v", names)
	}
}

func TestWriteFileAtomicCreatesWhatADanglingLinkPointsAt(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "dotfiles", "registry.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "registry.json")
	symlinkOrSkip(t, filepath.Join("dotfiles", "registry.json"), link) // relative, and not there yet
	if err := writeFileAtomic(link, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new\n" {
		t.Errorf("the link target holds %q", b)
	}
}

func TestRegistrySaveRoundTrip(t *testing.T) {
	setupWorktreeRepo(t)
	r := &Registry{Worktrees: map[string]map[string]int{"/a": {"APP_PORT": 81}, "/b": {"APP_PORT": 82}}}
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	got, err := loadRegistry()
	if err != nil || len(got.Worktrees) != 2 || got.Worktrees["/b"]["APP_PORT"] != 82 {
		t.Errorf("loaded %+v, %v", got, err)
	}
	if names := dirNames(t, filepath.Dir(mustRegistryPath(t))); len(names) != 1 {
		t.Errorf("config directory holds %v", names)
	}
}

// A race smoke test: a reader never sees a partial file while a writer replaces it (with the old os.WriteFile the
// truncate-then-write window shows up as a parse error). Only outcomes are asserted, and the loop is short so that
// Windows CI stays fast; there a read may also fail because the file is being replaced, which is not a parse error.
func TestRegistrySaveIsNeverSeenPartially(t *testing.T) {
	setupWorktreeRepo(t)
	filler := strings.Repeat("x", 200)
	save := func(i int) error {
		r := &Registry{Worktrees: map[string]map[string]int{}}
		for k := 0; k < 30; k++ {
			r.Worktrees["/"+filler+strconv.Itoa(i)+"/"+strconv.Itoa(k)] = map[string]int{"APP_PORT": 81 + k}
		}
		return r.save()
	}
	if err := save(0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	parseErrors := make(chan error, 1000)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				time.Sleep(time.Millisecond) // do not starve the writer, which Windows CI would turn into a failed replace
				if _, err := loadRegistry(); err != nil && strings.Contains(err.Error(), "failed to parse") {
					select {
					case parseErrors <- err:
					default:
					}
				}
			}
		}()
	}
	for i := 1; i <= 60; i++ {
		if err := save(i); err != nil {
			t.Errorf("save %d: %v", i, err)
			break
		}
	}
	close(done)
	wg.Wait()
	close(parseErrors)
	if err, ok := <-parseErrors; ok {
		t.Errorf("a reader saw a partial registry: %v", err)
	}
}

func TestRegistryMainsLoadAndSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	p, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	// A file of an older version has no mains; a hand-written null record must not break anything.
	for _, content := range []string{`{"worktrees":{}}`, `{"worktrees":{},"mains":null}`, `{"worktrees":{},"mains":{"/m":null}}`} {
		writeFile(t, p, content)
		reg, err := loadRegistry()
		if err != nil {
			t.Fatalf("%s: %v", content, err)
		}
		if reg.Mains == nil {
			t.Fatalf("%s: Mains is nil", content)
		}
		reg.used("") // must not panic on a null record
		reg.forgetWorktree("/m", "/wt")
		reg.recordMain("/m", "/wt", nil, nil, false)
		if rec := reg.Mains["/m"]; rec.Ports == nil || rec.Defaults == nil || len(rec.Worktrees) != 1 {
			t.Errorf("%s: record = %+v", content, rec)
		}
	}
	reg := &Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{
		"/m": {Ports: map[string]int{"APP_PORT": 8080}, Defaults: map[string]int{"APP_PORT": 80}, Worktrees: []string{"/wt"}},
	}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	got, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if rec := got.Mains["/m"]; rec.Ports["APP_PORT"] != 8080 || rec.Defaults["APP_PORT"] != 80 || len(rec.Worktrees) != 1 {
		t.Errorf("mains not kept by save: %+v", got.Mains)
	}
	if !got.used("")[8080] {
		t.Error("a main worktree's port is not counted as used")
	}
}

func TestRecordMainAndForgetWorktree(t *testing.T) {
	reg := &Registry{Worktrees: map[string]map[string]int{}}
	ports, defaults := map[string]int{"APP_PORT": 81}, map[string]int{"APP_PORT": 80}
	if !reg.recordMain("/m", "/wt/b", ports, defaults, true) {
		t.Error("a new record is not reported as a change")
	}
	reg.recordMain("/m", "/wt/a", ports, defaults, true)
	if reg.recordMain("/m", "/wt/a", ports, defaults, true) {
		t.Error("an identical record is reported as a change")
	}
	if got := reg.Mains["/m"].Worktrees; len(got) != 2 || got[0] != "/wt/a" || got[1] != "/wt/b" {
		t.Errorf("worktrees = %v", got)
	}
	// .env could not be read: the recorded ports stay.
	if reg.recordMain("/m", "", nil, defaults, false) || reg.Mains["/m"].Ports["APP_PORT"] != 81 {
		t.Errorf("ports of an unreadable .env replaced the record: %+v", reg.Mains["/m"])
	}
	// A new record from an unreadable .env reserves nothing.
	reg.recordMain("/n", "", nil, defaults, false)
	if rec := reg.Mains["/n"]; len(rec.Ports) != 0 {
		t.Errorf("new record = %+v", rec)
	}
	reg.forgetWorktree("/m", "/wt/a")
	if rec := reg.Mains["/m"]; len(rec.Worktrees) != 1 || rec.Worktrees[0] != "/wt/b" || rec.Ports["APP_PORT"] != 81 {
		t.Errorf("after forgetWorktree: %+v", rec)
	}
}

func TestPortsFromEnv(t *testing.T) {
	defaults := map[string]int{"APP_PORT": 80, "FORWARD_DB_PORT": 3306, "VITE_PORT": 5173}
	e, err := parseEnv(strings.NewReader("APP_PORT=8080\nFORWARD_DB_PORT=abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := portsFromEnv(e, defaults)
	// A valid value is used, an invalid one reserves nothing, an absent key falls back to the compose default.
	if len(got) != 2 || got["APP_PORT"] != 8080 || got["VITE_PORT"] != 5173 {
		t.Errorf("with .env: %v", got)
	}
	if got := portsFromEnv(nil, defaults); len(got) != 3 || got["FORWARD_DB_PORT"] != 3306 {
		t.Errorf("without .env: %v", got)
	}
}

func TestPortsFromEnvReadsLikeCompose(t *testing.T) {
	cases := map[string]int{ // 0: nothing reserved
		"3307 # mine":     3307,
		"3307\t# mine":    3307,
		" 3307 ":          3307,
		`"3307" # quoted`: 3307,
		`'3307'  # x`:     3307,
		`'3307'#x`:        3307, // bash reads 3307#x: the value compose uses is reserved
		"":                3306, // empty counts as absent
		`""`:              3306,
		" # comment":      3306,
		"#3307":           0, // a value, not a comment, right after "="
		"3307#x":          0, // a "#" inside a word is not a comment
		`"3307`:           0, // no closing quote
		`"33"07`:          0, // bash reads 3307, not 33: such a value reserves nothing, as before
		`"3307" x`:        0,
		"abc # x":         0,
	}
	defaults := map[string]int{"FORWARD_DB_PORT": 3306}
	for value, want := range cases {
		for _, prefix := range []string{"", "export "} {
			e, err := parseEnv(strings.NewReader(prefix + "FORWARD_DB_PORT=" + value + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			got := portsFromEnv(e, defaults)
			if p, ok := got["FORWARD_DB_PORT"]; (want == 0 && ok) || (want != 0 && p != want) {
				t.Errorf("%sFORWARD_DB_PORT=%q: got %v, want %d", prefix, value, got, want)
			}
		}
	}
	// Get is unchanged: up writes what it returns back to .env.
	e, err := parseEnv(strings.NewReader("APP_URL=http://localhost # mine\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get("APP_URL"); v != "http://localhost # mine" {
		t.Errorf("Get = %q", v)
	}
}

func TestWindowsDriveKey(t *testing.T) {
	refused := []string{`\??\UNC\h\s\x`, `\??\C:\x`, `\\?\C:\x`, `\\?\UNC\h\s\x`, `\\.\pipe\x`, `\\h\s\x`,
		`//h/s/x`, `\/h\s\x`, `\x`, `C:x`, `1:\x`, `C:`, ""}
	for _, k := range refused {
		if windowsDriveKey(k, "windows") {
			t.Errorf("windows accepts %q", k)
		}
		if !windowsDriveKey(k, "linux") {
			t.Errorf("linux refuses %q", k)
		}
	}
	for _, k := range []string{`C:\x`, `c:\x`, `C:/x`, `z:\`} {
		if !windowsDriveKey(k, "windows") {
			t.Errorf("windows refuses %q", k)
		}
	}
}

func TestValidKeyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the drive-letter rule applies only on Windows")
	}
	for _, k := range []string{`\??\UNC\h\s\x`, `\??\C:\x`, `\\?\C:\x`, `\\?\UNC\h\s\x`, `\\.\pipe\x`, `\\h\s\x`, `//h/s/x`, `C:/x`} {
		if validKey(k) {
			t.Errorf("validKey accepts %q", k)
		}
	}
	if !validKey(`C:\x`) {
		t.Error(`validKey refuses C:\x`)
	}
}

func TestMigrateLeavesInvalidKeysAlone(t *testing.T) {
	// A key that is not clean resolves to root, but an invalid key is never looked at as a path.
	root := realTempDir(t)
	unclean := root + string(filepath.Separator) + "."
	r := &Registry{Worktrees: map[string]map[string]int{unclean: {"APP_PORT": 8123}}}
	r.migrate(root)
	if _, ok := r.Worktrees[unclean]; !ok || len(r.Worktrees) != 1 {
		t.Errorf("an invalid key was migrated: %v", r.Worktrees)
	}
}

func TestRegistryMigrateFoldsMainsWorktrees(t *testing.T) {
	real, links := newLinkedDir(t, "b-link", "a-link")
	base := filepath.Dir(real)
	other, z := filepath.Join(base, "other"), filepath.Join(base, "z")
	// Resolves to real, but fails validKey, so it must not be resolved.
	unclean := real + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(real)
	r := &Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{
		"/a": {Worktrees: []string{links[0], other}},
		"/b": {Worktrees: []string{real, links[1], links[0]}},
		"/c": {Worktrees: []string{z, other}},
		"/d": {Worktrees: []string{unclean}},
	}}
	r.migrate(real)
	for m, want := range map[string][]string{
		"/a": {other, real},
		"/b": {real},
		"/c": {z, other}, // no alias: left as it is, order included
		"/d": {unclean},
	} {
		if got := r.Mains[m].Worktrees; !slices.Equal(got, want) {
			t.Errorf("mains[%s].worktrees = %q, want %q", m, got, want)
		}
	}
}

func TestPortsFromEnvIgnoresInvalidDefaults(t *testing.T) {
	defaults := map[string]int{"A_PORT": 0, "B_PORT": 70000, "C_PORT": -1, "D_PORT": 70000}
	e, err := parseEnv(strings.NewReader("D_PORT=8080\n"))
	if err != nil {
		t.Fatal(err)
	}
	// An invalid default is no fallback; a valid value in .env is still used.
	if got := portsFromEnv(e, defaults); len(got) != 1 || got["D_PORT"] != 8080 {
		t.Errorf("with .env: %v", got)
	}
	if got := portsFromEnv(nil, defaults); len(got) != 0 {
		t.Errorf("without .env: %v", got)
	}
}

func TestPortsFromEnvReadsTheLastLineOfAKey(t *testing.T) {
	defaults := map[string]int{"APP_PORT": 80}
	for env, want := range map[string]int{ // 0: nothing reserved
		"APP_PORT=8080\nAPP_PORT=8081\n":        8081,
		"export APP_PORT=8080\nAPP_PORT=8082\n": 8082,
		"APP_PORT=8080\nexport APP_PORT=8083\n": 8083,
		"APP_PORT=8080\nAPP_PORT=\n":            80, // the last line is empty: the compose default
		"APP_PORT=8080\n#APP_PORT=9\n":          8080,
		"APP_PORT=8080\nAPP_PORT=abc\n":         0,
	} {
		e, err := parseEnv(strings.NewReader(env))
		if err != nil {
			t.Fatal(err)
		}
		got := portsFromEnv(e, defaults)
		if p, ok := got["APP_PORT"]; (want == 0 && ok) || (want != 0 && p != want) {
			t.Errorf("%q: got %v, want %d", env, got, want)
		}
	}
	// Get, which up writes back to .env, still reads the first line.
	e, err := parseEnv(strings.NewReader("APP_PORT=8080\nAPP_PORT=8081\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get("APP_PORT"); v != "8080" {
		t.Errorf("Get = %q", v)
	}
}
