package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const sampleCompose = `services:
    laravel.test:
        ports:
            - '${APP_PORT:-80}:80'
            - '${VITE_PORT:-5173}:${VITE_PORT:-5173}'
    mysql:
        ports:
            - '${FORWARD_DB_PORT:-3306}:3306'
`

func TestDetectPortVars(t *testing.T) {
	v := detectPortVars(sampleCompose)
	want := []PortVar{{"APP_PORT", 80}, {"VITE_PORT", 5173}, {"FORWARD_DB_PORT", 3306}}
	if len(v) != len(want) {
		t.Fatalf("got %v", v)
	}
	for i := range want {
		if v[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, v[i], want[i])
		}
	}
}

func TestAllocate(t *testing.T) {
	vars := []PortVar{{"APP_PORT", 80}, {"DB", 3306}}
	free := func(p int) bool { return p != 81 }
	got, err := allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{82: true}, free)
	if err != nil {
		t.Fatal(err)
	}
	if got["DB"] != 3310 || got["APP_PORT"] != 83 {
		t.Errorf("got %v", got)
	}
	// An existing assignment taken by another worktree is reassigned
	got, _ = allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{3310: true}, func(int) bool { return true })
	if got["DB"] != 3307 {
		t.Errorf("got %v", got)
	}
	// An assignment that is not a port number (a hand-edited registry) is replaced, not written to .env again.
	for _, bad := range []int{0, -1, 65536, 70000} {
		got, _ = allocatePorts(vars, map[string]int{"APP_PORT": bad}, nil, func(int) bool { return true })
		if got["APP_PORT"] != 81 {
			t.Errorf("current %d: got %v", bad, got)
		}
	}
}

func TestEnvSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	e := &envFile{lines: []string{"# c", "APP_PORT=80", "APP_URL=\"http://localhost\""}}
	e.Set("APP_PORT", "81")
	e.Set("NEW", "1")
	if v, _ := e.Get("APP_URL"); v != "http://localhost" {
		t.Errorf("APP_URL=%q", v)
	}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	r, _ := readEnv(p)
	if v, _ := r.Get("APP_PORT"); v != "81" {
		t.Errorf("APP_PORT=%q", v)
	}
	if v, _ := r.Get("NEW"); v != "1" {
		t.Errorf("NEW=%q", v)
	}
}

func TestSessionCookieName(t *testing.T) {
	if got := sessionCookieName("app-wt-abc123"); got != "app-wt-abc123-session" {
		t.Errorf("got %q", got)
	}
}

func TestEnvSetCollapsesDuplicates(t *testing.T) {
	e := &envFile{lines: []string{"APP_PORT=80", "X=1", "export APP_PORT=90", "APP_PORT=95"}}
	e.Set("APP_PORT", "81")
	want := []string{"APP_PORT=81", "X=1"}
	if strings.Join(e.lines, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", e.lines, want)
	}
}

func TestEnvWriteMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no 0600 file permission")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	e := &envFile{lines: []string{"A=1"}}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of a new file = %v", fi.Mode().Perm())
	}
	// The mode of an existing file is unchanged
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode of an existing file changed to %v", fi.Mode().Perm())
	}
}

func TestCheckOwnEnv(t *testing.T) {
	dir := t.TempDir()
	if err := checkOwnEnv(filepath.Join(dir, ".env")); err != nil {
		t.Errorf("a missing file is allowed: %v", err)
	}
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnEnv(real); err != nil {
		t.Errorf("a regular file is allowed: %v", err)
	}
}

// Kept apart from TestCheckOwnEnv so that its missing-file and regular-file checks are reported as run on Windows
// without the right to create links.
func TestCheckOwnEnvRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sym")
	symlinkOrSkip(t, real, link)
	if err := checkOwnEnv(link); err == nil {
		t.Error("symbolic link not refused")
	}
	e := &envFile{lines: []string{"A=2"}}
	if err := e.Write(link); err == nil {
		t.Error("wrote through a symbolic link")
	}
	if b, _ := os.ReadFile(real); string(b) != "A=1\n" {
		t.Errorf("link target was rewritten: %q", b)
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestOverrideKey(t *testing.T) {
	if k, ok := (&envFile{lines: []string{"A=1", "# COMPOSE_FILE=x"}}).overrideKey(rmOverrideKeys); ok {
		t.Errorf("a comment is allowed: %s", k)
	}
	for _, k := range rmOverrideKeys {
		if got, ok := (&envFile{lines: []string{k + "=x"}}).overrideKey(rmOverrideKeys); !ok || got != k {
			t.Errorf("rm does not refuse %s", k)
		}
	}
	// COMPOSE_PROFILES is allowed by up and refused by rm.
	prof := &envFile{lines: []string{"COMPOSE_PROFILES=x"}}
	if _, ok := prof.overrideKey(upOverrideKeys); ok {
		t.Error("up refuses COMPOSE_PROFILES")
	}
	if _, ok := prof.overrideKey(rmOverrideKeys); !ok {
		t.Error("rm does not refuse COMPOSE_PROFILES")
	}
	for _, k := range []string{"COMPOSE_FILE", "COMPOSE_ENV_FILES", "SAIL_FILES"} {
		if _, ok := (&envFile{lines: []string{k + "=x"}}).overrideKey(upOverrideKeys); !ok {
			t.Errorf("up does not refuse %s", k)
		}
	}
}

func TestCleanEnv(t *testing.T) {
	t.Setenv("COMPOSE_FILE", "/evil.yaml")
	t.Setenv("COMPOSE_PROJECT_NAME", "other")
	t.Setenv("COMPOSE_PATH_SEPARATOR", ";")
	t.Setenv("SAIL_FILES", "x")
	t.Setenv("APP_PORT", "9999")
	t.Setenv("KEEP_ME", "1")
	got := strings.Join(cleanEnv([]string{"APP_PORT"}), "\n")
	for _, k := range []string{"COMPOSE_FILE=", "COMPOSE_PROJECT_NAME=", "COMPOSE_PATH_SEPARATOR=", "SAIL_FILES=", "APP_PORT="} {
		if strings.Contains(got, k) {
			t.Errorf("%s is left", k)
		}
	}
	if !strings.Contains(got, "KEEP_ME=1") {
		t.Error("an unrelated variable was removed")
	}
}

func TestFilterEnv(t *testing.T) {
	environ := []string{"compose_file=x", "Compose_Project_Name=y", "sail_files=z", "app_port=1", "KEEP=1", "=C:=C:\\", "PATH=/bin"}
	got := filterEnv(environ, []string{"app_port"})
	want := []string{"KEEP=1", "=C:=C:\\", "PATH=/bin"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", got, want)
	}
	// Never nil even if everything is removed (a nil Env makes exec.Cmd inherit the parent environment).
	if got := filterEnv([]string{"COMPOSE_FILE=x"}, nil); got == nil || len(got) != 0 {
		t.Errorf("empty result is nil: %#v", got)
	}
	if got := filterEnv(nil, nil); got == nil {
		t.Error("nil when the input is empty")
	}
}

func TestLoopbackBindBlocked(t *testing.T) {
	perm := fmt.Errorf("listen: %w", os.ErrPermission)
	inUse := fmt.Errorf("listen: %w", syscall.EADDRINUSE)
	noAddr := fmt.Errorf("listen: %w", syscall.EADDRNOTAVAIL)
	cases := []struct {
		name string
		err  error
		goos string
		want bool
	}{
		{"a permission error on darwin (privileged port) is not taken", perm, "darwin", false},
		{"a permission error on linux is taken", perm, "linux", true},
		{"a permission error on windows is taken", perm, "windows", true},
		{"an environment without loopback is not taken", noAddr, "linux", false},
		{"in use is taken (darwin)", inUse, "darwin", true},
		{"in use is taken (linux)", inUse, "linux", true},
		{"any other error is taken", fmt.Errorf("boom"), "linux", true},
	}
	for _, c := range cases {
		if got := loopbackBindBlocked(c.err, c.goos); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestPortFreeDetectsLoopbackOnly(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Errorf("port %d bound only on 127.0.0.1 was considered free", port)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Creates a main worktree and a linked worktree, and changes into the linked one.
// HOME and XDG_CONFIG_HOME point at temporary directories so that the real user's registry and settings are not touched.
func setupWorktreeRepo(t *testing.T) (main, wt string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main = filepath.Join(base, "app")
	wt = filepath.Join(base, "app-feat")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-q", "-b", "main")
	cfg := `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80}]}`
	writeFile(t, filepath.Join(main, configName), cfg)
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-q", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	t.Chdir(wt)
	return main, wt
}

func TestRmRefusesMismatchedProjectName(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	env := "COMPOSE_PROJECT_NAME=other-project\n"
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdRm([]string{"-y"})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("a name mismatch is not refused: %v", err)
	}
}

func TestNameMismatchErrorGuidance(t *testing.T) {
	msg := nameMismatchError("old-name", "app-feat-abc123").Error()
	if first, _, _ := strings.Cut(msg, "\n"); !strings.Contains(first, "set COMPOSE_PROJECT_NAME in .env to app-feat-abc123") {
		t.Errorf("the first line lacks the main remedy: %s", msg)
	}
	for _, want := range []string{`"old-name"`, "docker compose ls -a", "another worktree", "unset", "docker compose -p old-name down -v --rmi local --remove-orphans", "cannot be undone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q is missing: %s", want, msg)
		}
	}
	// The steps are numbered and the irreversibility warning comes before the command to run.
	idx := func(s string) int { return strings.Index(msg, s) }
	if !(idx("\n  1. ") >= 0 && idx("\n  1. ") < idx("\n  2. ") && idx("\n  2. ") < idx("\n  3. ") && idx("cannot be undone") < idx("docker compose -p old-name")) {
		t.Errorf("the order of the steps is broken: %s", msg)
	}
}

// A long worktree name makes a long project name; it is still a name projectName made, so the recovery command is
// shown for it, up to the longest name projectName can make (#63). Only strings are used: such directories would not
// fit in a Windows path.
func TestNameMismatchErrorGuidesLongNames(t *testing.T) {
	got := projectName(filepath.Join("base", strings.Repeat("m", 255)), filepath.Join("base", strings.Repeat("w", 255)), "/p")
	if len(got) != 518 || !safeProjectName.MatchString(got) {
		t.Fatalf("projectName = %q (%d bytes), want a long name the hint accepts", got, len(got))
	}
	short := projectName(filepath.Join("base", "app"), filepath.Join("base", "app-"+strings.Repeat("feature-", 10)), "/p")
	for _, name := range []string{got, short, strings.Repeat("a", 600)} {
		if msg := nameMismatchError(name, "app-feat-abc123").Error(); !strings.Contains(msg, "docker compose -p "+name+" down") {
			t.Errorf("no recovery command for %q (%d bytes): %s", name, len(name), msg)
		}
	}
}

func TestNameMismatchErrorEscapesUntrustedNames(t *testing.T) {
	for _, bad := range []string{"x; rm -rf ~", "$(id)", "a`id`", "line1\nline2", "esc\x1b[31m", "-rf", "UPPER", "\u540d\u524d", "", strings.Repeat("a", 601)} {
		msg := nameMismatchError(bad, "app-feat-abc123").Error()
		if strings.Contains(msg, "docker compose -p") {
			t.Errorf("%q produced a command: %s", bad, msg)
		}
		for _, r := range msg {
			if r != '\n' && r < 0x20 || r == 0x7f {
				t.Errorf("%q: control character %U is not escaped: %q", bad, r, msg)
			}
		}
		if !strings.Contains(msg, fmt.Sprintf("%+q", bad)) {
			t.Errorf("%q is not shown with %%+q: %q", bad, msg)
		}
	}
	// An invalid want is shown quoted and escaped and is not mixed into the command.
	msg := nameMismatchError("old-name", "bad\nname $(id)").Error()
	if strings.Contains(msg, "bad\nname") || !strings.Contains(msg, `"bad\nname $(id)"`) {
		t.Errorf("want is not escaped properly: %q", msg)
	}
	if strings.Contains(msg, "-p bad") || strings.Contains(msg, "-p \"bad") {
		t.Errorf("want is mixed into the command: %q", msg)
	}
}

func TestRmRefusalsDoNotReadStdin(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	cases := map[string]struct{ env, want string }{
		"name mismatch":   {"COMPOSE_PROJECT_NAME=other\n", "does not match"},
		"refused key":     {"COMPOSE_PROJECT_NAME=" + proj + "\nCOMPOSE_PROFILES=x\n", "remove that line from .env before rm"},
		"missing compose": {"COMPOSE_PROJECT_NAME=" + proj + "\n", "compose file not found"},
	}
	for name, c := range cases {
		if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(c.env), 0o600); err != nil {
			t.Fatal(err)
		}
		in := strings.NewReader("y\n")
		old := stdin
		stdin = in
		calls := captureRunner(t)
		err := cmdRm(nil)
		stdin = old
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: not refused as expected: %v", name, err)
		}
		if in.Len() != 2 || len(*calls) != 0 {
			t.Errorf("%s: should refuse before the prompt but read stdin or ran a command", name)
		}
	}
}

func TestRmRefusesBeforePromptWithoutReadingStdin(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	// Only .env exists; there is no compose file (setupWorktreeRepo does not create one).
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader("y\n")
	old := stdin
	stdin = in
	t.Cleanup(func() { stdin = old })
	calls := captureRunner(t)
	err := cmdRm(nil)
	if err == nil || !strings.Contains(err.Error(), "compose file not found") {
		t.Fatalf("a missing compose file is not refused: %v", err)
	}
	if in.Len() != 2 || len(*calls) != 0 {
		t.Errorf("should refuse before the prompt but read stdin or ran a command: remaining=%d calls=%d", in.Len(), len(*calls))
	}
}

func TestOverrideErrorsGuideBySource(t *testing.T) {
	cases := []struct {
		src  envSource
		want []string
	}{
		{envOwn, []string{".env has COMPOSE_FILE, so up cannot continue", "remove that line from .env"}},
		{envFromMain, []string{"the main worktree's .env has COMPOSE_FILE", "also affects the source of other worktrees", "create .env in this worktree first"}},
		{envFromMainExample, []string{"the main worktree's .env.example has COMPOSE_FILE", "Remove it from .env.example", "create .env in this worktree first"}},
	}
	for _, c := range cases {
		msg := upOverrideError("COMPOSE_FILE", c.src).Error()
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("src=%d: %q is missing: %s", c.src, w, msg)
			}
		}
	}
}

func TestUpOverrideErrorSourceIsTracked(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	captureRunner(t)
	// There is no .env and the main worktree's .env has COMPOSE_FILE.
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("COMPOSE_FILE=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "the main worktree's .env has COMPOSE_FILE") {
		t.Errorf("the guidance does not come from the main .env: %v", err)
	}
	// There is no main .env and .env.example has COMPOSE_FILE.
	os.Remove(filepath.Join(main, ".env"))
	if err := os.WriteFile(filepath.Join(main, ".env.example"), []byte("COMPOSE_FILE=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "the main worktree's .env.example has COMPOSE_FILE") {
		t.Errorf("the guidance does not come from .env.example: %v", err)
	}
	// The worktree's own .env has COMPOSE_FILE.
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_FILE=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), ".env has COMPOSE_FILE, so up cannot continue") {
		t.Errorf("the guidance is not for the worktree's own .env: %v", err)
	}
}

func TestRmAndUpRefuseComposeOverrides(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	env := "COMPOSE_PROJECT_NAME=" + proj + "\nCOMPOSE_FILE=/evil.yaml\n"
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdRm([]string{"-y"}); err == nil || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("rm does not refuse COMPOSE_FILE: %v", err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("up does not refuse COMPOSE_FILE: %v", err)
	}
}

func TestUpRefusesSymlinkEnv(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	target := filepath.Join(main, ".env")
	if err := os.WriteFile(target, []byte("APP_URL=http://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, target, filepath.Join(wt, ".env"))
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("up does not refuse a symbolic link: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "APP_URL=http://localhost\n" {
		t.Errorf("the main .env was rewritten: %q", b)
	}
}

func TestProjectNameIsStableAcrossSymlinkedPaths(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	link := filepath.Join(filepath.Dir(wt), "via-link")
	symlinkOrSkip(t, wt, link)
	root, _, err := worktreeRootAndPrefix(link)
	if err != nil {
		t.Fatal(err)
	}
	if root != wt {
		t.Errorf("not resolved to the real path: %q != %q", root, wt)
	}
	if projectName(main, root, root) != projectName(main, wt, wt) {
		t.Error("the project name depends on the path used")
	}
}

// oldProjectName is the formula of older versions for a project at the worktree root. It is valid only for a non-empty
// slug: an empty one gave "-<hash>".
func oldProjectName(main, root string) string {
	sum := sha1.Sum([]byte(root))
	slug := regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(strings.ToLower(filepath.Base(main)+"-"+filepath.Base(root)), "-")
	return strings.Trim(slug, "-_") + "-" + hex.EncodeToString(sum[:])[:6]
}

// A project at the worktree root must keep the name older versions wrote to .env, or rm refuses existing worktrees.
func TestProjectNameOfRootProjectIsUnchanged(t *testing.T) {
	main := filepath.Join(t.TempDir(), "My App")
	wt := filepath.Join(t.TempDir(), "my-app_feat.x")
	if got, want := projectName(main, wt, wt), oldProjectName(main, wt); got != want {
		t.Errorf("projectName = %q, the old name was %q", got, want)
	}
}

// compose refuses a project name that does not match its rule, so sail up and rm would fail (#35).
func TestProjectNameIsValidForComposeWithoutASCIINames(t *testing.T) {
	compose := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	for _, tc := range []struct{ main, wt string }{
		{"\u30a2\u30d7\u30ea", "\u30a2\u30d7\u30ea-\u65b0\u6a5f\u80fd"},
		{"\u00e9\u00e8", "\u00e0"},
		{"__", "-_."},
	} {
		mainTop := filepath.Join(t.TempDir(), tc.main)
		wtTop := filepath.Join(t.TempDir(), tc.wt)
		got := projectName(mainTop, wtTop, wtTop)
		sum := sha1.Sum([]byte(wtTop))
		if want := "wt-" + hex.EncodeToString(sum[:])[:6]; got != want {
			t.Errorf("%q, %q: projectName = %q, want %q", tc.main, tc.wt, got, want)
		}
		if !compose.MatchString(got) || !safeProjectName.MatchString(got) {
			t.Errorf("%q, %q: %q does not match the compose rule", tc.main, tc.wt, got)
		}
	}
	// A name with an ASCII letter or digit keeps the old formula, even next to non-ASCII characters.
	mainTop := filepath.Join(t.TempDir(), "\u30a2\u30d7\u30ea")
	wtTop := filepath.Join(t.TempDir(), "\u30a2-feat_")
	if got, want := projectName(mainTop, wtTop, wtTop), oldProjectName(mainTop, wtTop); got != want || !compose.MatchString(got) {
		t.Errorf("projectName = %q, the old name was %q", got, want)
	}
}

func TestProjectNameArguments(t *testing.T) {
	mainTop := filepath.Join(t.TempDir(), "app")
	wtTop := filepath.Join(t.TempDir(), "app-feat")
	root := filepath.Join(wtTop, "laravel")
	got := projectName(mainTop, wtTop, root)
	sum := sha1.Sum([]byte(root))
	if want := "app-app-feat-" + hex.EncodeToString(sum[:])[:6]; got != want {
		t.Errorf("projectName = %q, want %q (slug from the tops, hash of the project directory)", got, want)
	}
	if projectName(mainTop, root, wtTop) == got {
		t.Error("swapping wtTop and root does not change the name")
	}
}

func TestKeyOfHandlesExportWithTab(t *testing.T) {
	for _, l := range []string{"export COMPOSE_FILE=x", "export\tCOMPOSE_FILE=x", "  export   COMPOSE_FILE = x"} {
		if k, ok := keyOf(l); !ok || k != "COMPOSE_FILE" {
			t.Errorf("%q: key=%q ok=%v", l, k, ok)
		}
	}
	if k, _ := keyOf("exported=1"); k != "exported" {
		t.Errorf("a key that only starts with export: %q", k)
	}
}

func TestLoadConfigRejectsUnsafeCompose(t *testing.T) {
	bad := []string{"", "/etc/compose.yaml", `\etc\compose.yaml`, "../compose.yaml", "a/../../compose.yaml", "."}
	if runtime.GOOS == "windows" {
		// Drive and UNC paths are special only on Windows ("C:x" is an ordinary file name on unix).
		bad = append(bad, `C:\compose.yaml`, `C:compose.yaml`, `\\srv\share\compose.yaml`, `..\compose.yaml`)
	}
	for _, c := range bad {
		dir := t.TempDir()
		b := `{"compose":` + strconvQuote(c) + `,"port_vars":[]}`
		writeFile(t, filepath.Join(dir, configName), b)
		if _, err := loadConfig(dir); err == nil {
			t.Errorf("compose=%q is not refused", c)
		}
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, configName), `{"compose":"docker/compose.yaml","port_vars":[]}`)
	if _, err := loadConfig(dir); err != nil {
		t.Errorf("a relative path is allowed: %v", err)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type call struct {
	dir  string
	env  []string
	name string
	args []string
}

// Replaces runner and records the calls (it uses a global variable, t.Setenv and t.Chdir, so tests cannot run in parallel).
func captureRunner(t *testing.T) *[]call {
	t.Helper()
	var calls []call
	old := runner
	runner = func(dir string, env []string, name string, args ...string) error {
		calls = append(calls, call{dir, env, name, args})
		return nil
	}
	t.Cleanup(func() { runner = old })
	return &calls
}

func TestRmPassesPinnedArguments(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	t.Setenv("COMPOSE_FILE", "/evil.yaml")
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("number of calls = %d", len(*calls))
	}
	c := (*calls)[0]
	want := strings.Join(rmArgs(proj, wt, filepath.Join(wt, "compose.yaml")), " ")
	if c.name != "docker" || strings.Join(c.args, " ") != want {
		t.Errorf("arguments = %v", c.args)
	}
	for _, f := range []string{"--project-name " + proj, "--project-directory " + wt, "-f " + filepath.Join(wt, "compose.yaml")} {
		if !strings.Contains(want, f) {
			t.Errorf("%q is not pinned: %s", f, want)
		}
	}
	if strings.Contains(strings.Join(c.env, "\n"), "COMPOSE_FILE=") {
		t.Error("COMPOSE_FILE is left in the environment")
	}
}

func TestUpWritesEnvAndCleansSailEnvironment(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), "APP_URL=http://localhost\nSESSION_COOKIE=old\nSESSION_COOKIE=older\n")
	if err := os.MkdirAll(filepath.Join(wt, "vendor", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "vendor", "bin", "sail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PORT", "9999")
	t.Setenv("COMPOSE_PROFILES", "x")
	calls := captureRunner(t)
	if err := cmdUp([]string{"-d"}); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	proj := projectName(main, wt, wt)
	if v, _ := e.Get("COMPOSE_PROJECT_NAME"); v != proj {
		t.Errorf("COMPOSE_PROJECT_NAME=%q", v)
	}
	if v, _ := e.Get("SESSION_COOKIE"); v != proj+"-session" {
		t.Errorf("SESSION_COOKIE=%q", v)
	}
	n := 0
	for _, l := range e.lines {
		if k, _ := keyOf(l); k == "SESSION_COOKIE" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("number of SESSION_COOKIE lines = %d", n)
	}
	if port, _ := e.Get("APP_PORT"); port == "" || port == "80" || port == "9999" {
		t.Errorf("APP_PORT=%q", port)
	}
	if u, _ := e.Get("APP_URL"); !strings.HasPrefix(u, "http://localhost:") {
		t.Errorf("APP_URL=%q", u)
	}
	if fi, _ := os.Stat(filepath.Join(wt, ".env")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v", fi.Mode().Perm())
	}
	if len(*calls) != 1 || (*calls)[0].args[0] != "up" || (*calls)[0].args[1] != "-d" {
		t.Fatalf("calls to sail = %v", *calls)
	}
	env := strings.Join((*calls)[0].env, "\n")
	if strings.Contains(env, "APP_PORT=") || strings.Contains(env, "COMPOSE_PROFILES=") {
		t.Error("port variables or COMPOSE_* are left in sail's environment")
	}
}

func writeFakeSail(t *testing.T, wt string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(wt, "vendor", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "vendor", "bin", "sail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func captureStderr(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := stderr
	stderr = &b
	t.Cleanup(func() { stderr = old })
	return &b
}

func TestStopDoesNotRefuseAndWarns(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	// stop proceeds even with an override key or another project name (it warns instead of refusing).
	env := "COMPOSE_FILE=/other.yaml\nCOMPOSE_PROJECT_NAME=other-project\nCOMPOSE_PROFILES=x\n"
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	warn := captureStderr(t)
	calls := captureRunner(t)
	if err := cmdStop(nil); err != nil {
		t.Fatalf("stop refused: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].args[0] != "stop" {
		t.Fatalf("calls to sail = %v", *calls)
	}
	for _, want := range []string{"COMPOSE_FILE", `"other-project"`, strconvQuote(projectName(main, wt, wt))} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("the warning lacks %s: %s", want, warn)
		}
	}
	if strings.Contains(warn.String(), "COMPOSE_PROFILES") {
		t.Errorf("warned about COMPOSE_PROFILES: %s", warn)
	}
}

func TestStopAllowsSymlinkEnv(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	target := filepath.Join(main, ".env")
	if err := os.WriteFile(target, []byte("COMPOSE_PROJECT_NAME=other-project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, target, filepath.Join(wt, ".env"))
	warn := captureStderr(t)
	calls := captureRunner(t)
	if err := cmdStop(nil); err != nil || len(*calls) != 1 {
		t.Fatalf("stop: err=%v calls=%v", err, *calls)
	}
	if warn.Len() != 0 {
		t.Errorf("a linked .env is not read: %s", warn)
	}
}

func TestStopDoesNotWarnWithoutProjectName(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	warn := captureStderr(t)
	calls := captureRunner(t)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("APP_URL=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdStop(nil); err != nil || len(*calls) != 1 || warn.Len() != 0 {
		t.Errorf("err=%v warn=%s", err, warn)
	}
}

func TestStopDoesNotWarnInMainWorktree(t *testing.T) {
	main, _ := setupWorktreeRepo(t)
	writeFakeSail(t, main)
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("COMPOSE_PROJECT_NAME=whatever\nCOMPOSE_FILE=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(main)
	warn := captureStderr(t)
	calls := captureRunner(t)
	if err := cmdStop(nil); err != nil || len(*calls) != 1 || warn.Len() != 0 {
		t.Errorf("err=%v calls=%v warn=%s", err, *calls, warn)
	}
}

func TestUpAllowsProfilesRefusesEnvFiles(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	proj := projectName(main, wt, wt)
	write := func(extra string) {
		if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\n"+extra), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	calls := captureRunner(t)
	write("COMPOSE_PROFILES=debug\n")
	if err := cmdUp(nil); err != nil || len(*calls) != 1 {
		t.Fatalf("up refused COMPOSE_PROFILES: err=%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); !strings.Contains(string(b), "COMPOSE_PROFILES=debug") {
		t.Errorf("COMPOSE_PROFILES disappeared: %s", b)
	}
	write("COMPOSE_ENV_FILES=other.env\n")
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "COMPOSE_ENV_FILES") {
		t.Errorf("up does not refuse COMPOSE_ENV_FILES: %v", err)
	}
}

func TestRmRefusesProfiles(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\nCOMPOSE_PROFILES=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err == nil || !strings.Contains(err.Error(), "COMPOSE_PROFILES") || len(*calls) != 0 {
		t.Errorf("rm does not refuse COMPOSE_PROFILES: %v", err)
	}
}

func TestReadEnvIfRegularSkipsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readEnvIfRegular(filepath.Join(dir, "missing")); ok {
		t.Error("read a missing file")
	}
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, ok := readEnvIfRegular(real); !ok {
		t.Error("cannot read a regular file")
	} else if v, _ := e.Get("A"); v != "1" {
		t.Errorf("A=%q", v)
	}
	// An extra check: the rest of the test needs no link, so do not skip the whole test on Windows.
	if trySymlink(t, real, filepath.Join(dir, "link")) {
		if _, ok := readEnvIfRegular(filepath.Join(dir, "link")); ok {
			t.Error("read a link")
		}
	}
	if _, ok := readEnvIfRegular(dir); ok {
		t.Error("read a directory")
	}
}

// envPadding is comment lines of 100 bytes each, at least n bytes in all: many lines, since parseEnv's scanner also
// limits a single line.
func envPadding(n int) string {
	line := "#" + strings.Repeat("x", 98) + "\n"
	return strings.Repeat(line, (n+len(line)-1)/len(line))
}

func TestReadEnvIfRegularRefusesALargerFile(t *testing.T) {
	dir := t.TempDir()
	pad := envPadding(maxSmallFile - 100)
	want := strings.Repeat("1", maxSmallFile-len(pad)-3)
	fit := pad + "A=" + want + "\n"
	if len(fit) != maxSmallFile {
		t.Fatalf("fixture is %d bytes", len(fit))
	}
	p := filepath.Join(dir, "env")
	writeFile(t, p, fit)
	if e, ok := readEnvIfRegular(p); !ok {
		t.Error("cannot read a file of exactly maxSmallFile bytes")
	} else if v, _ := e.Get("A"); v != want {
		t.Errorf("the last line was cut: A=%q", v)
	}
	writeFile(t, p, fit+"B=2\n")
	if _, ok := readEnvIfRegular(p); ok {
		t.Error("a file larger than maxSmallFile was read (cut)")
	}
}

// A cut main .env could drop a port variable near its end and record the default silently; the record is kept instead.
func TestUpKeepsTheMainRecordWhenTheMainEnvIsTooLarge(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(wt, ".env"), "APP_NAME=x\n")
	writeFile(t, filepath.Join(main, ".env"), envPadding(maxSmallFile)+"APP_PORT=8124\n")
	reg := &Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{
		main: {Ports: map[string]int{"APP_PORT": 8200}, Defaults: map[string]int{"APP_PORT": 80}},
	}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	errOut := captureStderr(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadRegistry()
	if got := reg.Mains[main].Ports["APP_PORT"]; got != 8200 {
		t.Errorf("the record was replaced from a cut .env: APP_PORT=%d", got)
	}
	if !strings.Contains(errOut.String(), mainEnvUnreadable(main)) {
		t.Errorf("no warning: %q", errOut.String())
	}
}

func newLinkedDir(t *testing.T, names ...string) (real string, links []string) {
	t.Helper()
	base, err := realPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real = filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		l := filepath.Join(base, n)
		symlinkOrSkip(t, real, l)
		links = append(links, l)
	}
	return real, links
}

func TestRegistryMigrate(t *testing.T) {
	real, links := newLinkedDir(t, "b-link", "a-link")
	gone := filepath.Join(filepath.Dir(real), "gone")

	// An alias key is merged into root and no alias remains. A key of a path that no longer exists is kept.
	r := &Registry{Worktrees: map[string]map[string]int{links[0]: {"APP_PORT": 81}, gone: {"APP_PORT": 82}}}
	r.migrate(real, "")
	if got := r.Worktrees[real]["APP_PORT"]; got != 81 {
		t.Errorf("not merged: %v", r.Worktrees)
	}
	if _, ok := r.Worktrees[links[0]]; ok {
		t.Errorf("an alias is left: %v", r.Worktrees)
	}
	if r.Worktrees[gone]["APP_PORT"] != 82 {
		t.Errorf("touched the key of a missing path: %v", r.Worktrees)
	}

	// An existing root entry wins and the alias is dropped (the alias's ports are not left in used).
	r = &Registry{Worktrees: map[string]map[string]int{real: {"APP_PORT": 90}, links[0]: {"APP_PORT": 81}}}
	r.migrate(real, "")
	if r.Worktrees[real]["APP_PORT"] != 90 || len(r.Worktrees) != 1 || r.used("other")[81] {
		t.Errorf("root does not win: %v", r.Worktrees)
	}

	// k == root is left unchanged.
	r = &Registry{Worktrees: map[string]map[string]int{real: {"APP_PORT": 90}}}
	r.migrate(real, "")
	if r.Worktrees[real]["APP_PORT"] != 90 || len(r.Worktrees) != 1 {
		t.Errorf("changed k==root: %v", r.Worktrees)
	}

	// With several aliases and no root entry, the smallest key (a-link) is adopted.
	r = &Registry{Worktrees: map[string]map[string]int{links[0]: {"APP_PORT": 81}, links[1]: {"APP_PORT": 82}}}
	r.migrate(real, "")
	if r.Worktrees[real]["APP_PORT"] != 82 || len(r.Worktrees) != 1 {
		t.Errorf("not adopted deterministically: %v", r.Worktrees)
	}
}

func TestUpReusesPortsRecordedUnderSymlinkedPath(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("APP_URL=http://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	symlinkOrSkip(t, wt, alias)
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg.Worktrees[alias] = map[string]int{"APP_PORT": 8123}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, _ := readEnv(filepath.Join(wt, ".env"))
	if v, _ := e.Get("APP_PORT"); v != "8123" {
		t.Errorf("the port recorded under the alias is not reused: APP_PORT=%q", v)
	}
	reg, _ = loadRegistry()
	if _, ok := reg.Worktrees[alias]; ok || reg.Worktrees[wt]["APP_PORT"] != 8123 {
		t.Errorf("registry not migrated: %v", reg.Worktrees)
	}
}

func TestRmReleasesAliasKeysAndFailsEarlyOnBrokenRegistry(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	// A broken registry fails without using docker or stdin.
	p, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "{broken")
	in := strings.NewReader("y\n")
	old := stdin
	stdin = in
	t.Cleanup(func() { stdin = old })
	calls := captureRunner(t)
	if err := cmdRm(nil); err == nil || len(*calls) != 0 || in.Len() != 2 {
		t.Fatalf("a broken registry does not fail: err=%v calls=%d", err, len(*calls))
	}

	// Keys recorded under an alias are all released by rm.
	alias := filepath.Join(t.TempDir(), "alias")
	symlinkOrSkip(t, wt, alias)
	reg := &Registry{Worktrees: map[string]map[string]int{alias: {"APP_PORT": 8123}, "/other/wt": {"APP_PORT": 8200}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	if err := cmdRm([]string{"-y"}); err != nil || len(*calls) != 1 {
		t.Fatalf("rm: err=%v calls=%d", err, len(*calls))
	}
	reg, _ = loadRegistry()
	if len(reg.Worktrees) != 1 || reg.Worktrees["/other/wt"]["APP_PORT"] != 8200 {
		t.Errorf("the alias key was not released, or another worktree was removed: %v", reg.Worktrees)
	}
}

func writeRmFixtures(t *testing.T, main, wt string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
}

func TestRmKeepsRegistryUpdatesMadeWhileDockerRuns(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeRmFixtures(t, main, wt)
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	// The situation where another worktree's up updates the registry while docker is running.
	old := runner
	runner = func(string, []string, string, ...string) error {
		r, err := loadRegistry()
		if err != nil {
			return err
		}
		r.Worktrees["/during/rm"] = map[string]int{"APP_PORT": 8300}
		return r.save()
	}
	t.Cleanup(func() { runner = old })
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadRegistry()
	if _, ok := reg.Worktrees[wt]; ok || reg.Worktrees["/during/rm"]["APP_PORT"] != 8300 {
		t.Errorf("lost the update made while docker ran, or did not release: %v", reg.Worktrees)
	}
}

func TestRmDoesNotSaveRegistryWhenDockerFails(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeRmFixtures(t, main, wt)
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	old := runner
	runner = func(string, []string, string, ...string) error { return fmt.Errorf("docker failed") }
	t.Cleanup(func() { runner = old })
	if err := cmdRm([]string{"-y"}); err == nil {
		t.Fatal("docker failure is not returned")
	}
	reg, _ = loadRegistry()
	if reg.Worktrees[wt]["APP_PORT"] != 8123 {
		t.Errorf("changed the registry when docker failed: %v", reg.Worktrees)
	}
}

// realTempDir returns a temporary directory whose path has no links in it (macOS TempDir is under /var -> /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// trySymlink creates a symbolic link and reports whether it exists. Only Windows may lack the right to create one;
// elsewhere a failure is a real error, because skipping would let the symlink-refusal tests pass without running.
func trySymlink(t *testing.T, oldname, newname string) bool {
	t.Helper()
	err := os.Symlink(oldname, newname)
	if err == nil {
		return true
	}
	if runtime.GOOS != "windows" {
		t.Fatalf("cannot create symbolic links (required off Windows, see AGENTS.md): %v", err)
	}
	// Shown with -v or on failure: tell why the symlink-refusal check was not verified.
	t.Logf("symbolic link check not verified: cannot create symbolic links here: %v", err)
	return false
}

// symlinkOrSkip is trySymlink for a test that cannot run without the link: it skips on Windows only and fails
// everywhere else.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if !trySymlink(t, oldname, newname) {
		t.Skip("cannot create symbolic links here")
	}
}

// writeFile is for 0644 fixtures. Fixtures that must keep another mode (.env 0600, the sail script 0755 so that it is
// executable) call os.WriteFile themselves, so do not fold them into this helper.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rmWorktree prepares a worktree that gets past every rm check but the compose one, with compose set to rel.
func rmWorktree(t *testing.T, rel string) (wt string) {
	t.Helper()
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n")
	cfg := fmt.Sprintf(`{"compose":%q,"port_vars":[{"name":"APP_PORT","default":80}]}`, rel)
	writeFile(t, filepath.Join(wt, configName), cfg)
	return wt
}

// expectRmRefusedBeforePrompt runs rm and checks the error, that stdin was not read and that no command ran.
func expectRmRefusedBeforePrompt(t *testing.T, want string) {
	t.Helper()
	in := strings.NewReader("y\n")
	old := stdin
	stdin = in
	t.Cleanup(func() { stdin = old })
	calls := captureRunner(t)
	err := cmdRm(nil)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	if in.Len() != 2 || len(*calls) != 0 {
		t.Errorf("should refuse before the prompt but read stdin or ran a command: remaining=%d calls=%d", in.Len(), len(*calls))
	}
}

// expectRmUsesComposeFile runs rm -y and checks the path given to -f.
func expectRmUsesComposeFile(t *testing.T, want string) {
	t.Helper()
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("number of calls = %d", len(*calls))
	}
	args := (*calls)[0].args
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			if args[i+1] != want {
				t.Errorf("-f = %q, want %q", args[i+1], want)
			}
			return
		}
	}
	t.Errorf("no -f in %v", args)
}

func TestRmRefusesComposeLinkedOutsideWorktree(t *testing.T) {
	wt := rmWorktree(t, "compose.yaml")
	outside := filepath.Join(realTempDir(t), "compose.yaml")
	writeFile(t, outside, "services: {}\n")
	symlinkOrSkip(t, outside, filepath.Join(wt, "compose.yaml"))
	expectRmRefusedBeforePrompt(t, "resolves outside the project directory")
}

func TestRmRefusesComposeBehindLinkedDirectory(t *testing.T) {
	wt := rmWorktree(t, "sub/compose.yaml")
	outsideDir := realTempDir(t)
	writeFile(t, filepath.Join(outsideDir, "compose.yaml"), "services: {}\n")
	symlinkOrSkip(t, outsideDir, filepath.Join(wt, "sub"))
	expectRmRefusedBeforePrompt(t, "resolves outside the project directory")
}

func TestRmRefusesComposeChainThatLeavesWorktree(t *testing.T) {
	wt := rmWorktree(t, "a.yaml")
	outside := filepath.Join(realTempDir(t), "compose.yaml")
	writeFile(t, outside, "services: {}\n")
	symlinkOrSkip(t, outside, filepath.Join(wt, "b.yaml"))
	symlinkOrSkip(t, filepath.Join(wt, "b.yaml"), filepath.Join(wt, "a.yaml"))
	expectRmRefusedBeforePrompt(t, "resolves outside the project directory")
}

func TestRmAcceptsComposeChainInsideWorktreeAndPassesRealPath(t *testing.T) {
	wt := rmWorktree(t, "a.yaml")
	real := filepath.Join(wt, "real", "compose.yaml")
	writeFile(t, real, "services: {}\n")
	symlinkOrSkip(t, real, filepath.Join(wt, "b.yaml"))
	symlinkOrSkip(t, filepath.Join(wt, "b.yaml"), filepath.Join(wt, "a.yaml"))
	// docker must get the checked real path, not the configured one (which would be resolved again).
	if real == filepath.Join(wt, "a.yaml") {
		t.Fatal("the test needs the real path to differ from the configured one")
	}
	expectRmUsesComposeFile(t, real)
}

func TestRmRefusesBrokenComposeLink(t *testing.T) {
	wt := rmWorktree(t, "compose.yaml")
	symlinkOrSkip(t, filepath.Join(wt, "missing.yaml"), filepath.Join(wt, "compose.yaml"))
	expectRmRefusedBeforePrompt(t, "compose file not found")
}

func TestRmRefusesComposeLinkedToDirectory(t *testing.T) {
	wt := rmWorktree(t, "compose.yaml")
	if err := os.Mkdir(filepath.Join(wt, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(wt, "dir"), filepath.Join(wt, "compose.yaml"))
	expectRmRefusedBeforePrompt(t, "is not a regular file")
}

func TestRmThroughWorktreeAliasIsNotRefused(t *testing.T) {
	wt := rmWorktree(t, "compose.yaml")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	alias := filepath.Join(realTempDir(t), "alias")
	symlinkOrSkip(t, wt, alias)
	t.Chdir(alias)
	expectRmUsesComposeFile(t, filepath.Join(wt, "compose.yaml"))
}

func TestComposeInsideProject(t *testing.T) {
	root := realTempDir(t)
	writeFile(t, filepath.Join(root, "compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(root, "..foo.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(root, "..d", "compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(filepath.Dir(root), "outside.yaml"), "services: {}\n")
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	const notRegular = "is not a regular file"
	const notRegularHint = "use a regular file inside the project directory"
	cases := []struct {
		name, rel, want string // want is an error substring; empty means accepted
	}{
		{"regular file", "compose.yaml", ""},
		{"name that starts with two dots", "..foo.yaml", ""},
		{"directory that starts with two dots", filepath.Join("..d", "compose.yaml"), ""},
		{"missing file", "missing.yaml", "compose file not found"},
		{"directory", "dir", notRegular},
		{"the worktree itself", ".", notRegular},
		{"parent directory", "..", "resolves outside the project directory"},
		{"file in the parent directory", filepath.Join("..", "outside.yaml"), "resolves outside the project directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := composeInsideProject(root, c.rel)
			if c.want == "" {
				if err != nil || got != filepath.Join(root, c.rel) {
					t.Errorf("got %q, %v; want the file itself", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
			if c.want == notRegular && !strings.Contains(err.Error(), notRegularHint) {
				t.Errorf("error = %v, want it to say how to recover: %q", err, notRegularHint)
			}
		})
	}
}

func TestEscapeControl(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":             "plain text",
		"a\x1b[31mb":             `a\x1b[31mb`,
		"bell\a":                 `bell\a`,
		"tab\there":              `tab\there`,
		"del\x7f":                `del\x7f`,
		"c1\u0085":               `c1\u0085`,
		"bidi\u202egnp.exe":      `bidi\u202egnp.exe`,
		"isolate\u2066x":         `isolate\u2066x`,
		"csi\x9bb":               `csi\x9bb`,
		"line1\nline2":           "line1\nline2",
		"cr\rover":               `cr\rover`,
		"cut\xe6\x97":            `cut\xe6\x97`,
		"caf\u00e9 \u65e5\u672c": "caf\u00e9 \u65e5\u672c",
	} {
		if got := escapeControl(in); got != want {
			t.Errorf("escapeControl(%+q) = %+q, want %+q", in, got, want)
		}
	}
}

func TestPrintErrEscapes(t *testing.T) {
	var b strings.Builder
	printErr(&b, fmt.Errorf("open %s: denied", "/x/\x1b]0;title\a/.env"))
	if got, want := b.String(), "error: open /x/\\x1b]0;title\\a/.env: denied\n"; got != want {
		t.Errorf("printErr wrote %+q, want %+q", got, want)
	}
}

func TestWriteFileNoFollowRefusesLinksAndDirectories(t *testing.T) {
	dir := realTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileNoFollow(filepath.Join(dir, "d"), []byte("x"), 0o600); err == nil {
		t.Error("wrote to a directory")
	}
	target := filepath.Join(dir, "target")
	writeFile(t, target, "keep\n")
	link := filepath.Join(dir, "link")
	symlinkOrSkip(t, target, link)
	if err := writeFileNoFollow(link, []byte("x"), 0o600); err == nil {
		t.Error("wrote through a symbolic link")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Errorf("link target was rewritten: %q", b)
	}
}

func TestWriteFileNoFollowCreatesAndReplaces(t *testing.T) {
	p := filepath.Join(realTempDir(t), "f")
	if err := writeFileNoFollow(p, []byte("long content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileNoFollow(p, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "short\n" {
		t.Errorf("content = %q (not truncated)", b)
	}
}

func TestUpKeepsIPv6BracketsInAppURL(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), "APP_URL=http://[::1]\n")
	writeFakeSail(t, wt)
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := e.Get("APP_PORT")
	if v, _ := e.Get("APP_URL"); v != "http://[::1]:"+port {
		t.Errorf("APP_URL=%q", v)
	}
}

func TestUpReplacesAnInvalidRecordedPort(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(main, ".env"), "APP_NAME=x\n")
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 0}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	out := captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, _ := readEnv(filepath.Join(wt, ".env"))
	v, _ := e.Get("APP_PORT")
	if p, err := strconv.Atoi(v); err != nil || !validPort(p) {
		t.Fatalf("APP_PORT=%q", v)
	}
	if want := "  APP_PORT=" + v + " (was 0, which is not a valid port)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output does not contain %q:\n%s", want, out)
	}
}

// A branch whose configuration lacks a variable must not free the main worktree's port for it.
func TestUpKeepsAMainVariableUnknownToTheBranch(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8080\n")
	rec := MainRecord{Ports: map[string]int{"APP_PORT": 80, "FORWARD_REDIS_PORT": 6380},
		Defaults: map[string]int{"APP_PORT": 80, "FORWARD_REDIS_PORT": 6379}}
	if err := (&Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{main: rec}}).save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := loadRegistry()
	got := reg.Mains[main]
	if got.Ports["APP_PORT"] != 8080 || got.Ports["FORWARD_REDIS_PORT"] != 6380 || got.Defaults["FORWARD_REDIS_PORT"] != 6379 {
		t.Errorf("main record = %+v", got)
	}
	if !reg.used("")[6380] {
		t.Error("the kept port is not avoided")
	}
}

func TestUpRecordsMainPortsAndKeepsThemFree(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8123\n")
	// This worktree holds the main's port already: up moves it.
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	out := captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, _ := readEnv(filepath.Join(wt, ".env"))
	if v, _ := e.Get("APP_PORT"); v == "8123" || v == "" {
		t.Errorf("the worktree kept the main worktree's port: APP_PORT=%q", v)
	}
	if !strings.Contains(out.String(), "(was 8123,") {
		t.Errorf("the move is not reported:\n%s", out)
	}
	reg, _ = loadRegistry()
	rec, ok := reg.Mains[main]
	if !ok || rec.Ports["APP_PORT"] != 8123 || rec.Defaults["APP_PORT"] != 80 || len(rec.Worktrees) != 1 || rec.Worktrees[0] != wt {
		t.Errorf("main record = %+v (ok=%v)", rec, ok)
	}

	// Without the key in the main's .env, compose uses the default, and that is what is recorded.
	writeFile(t, filepath.Join(main, ".env"), "APP_NAME=x\n")
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadRegistry()
	if got := reg.Mains[main].Ports["APP_PORT"]; got != 80 {
		t.Errorf("APP_PORT recorded for the main = %d, want the default 80", got)
	}
}

// up reads the main's .env before the lock (#79): a change after the read is not seen by this run.
func TestUpReadsMainEnvBeforeTheLock(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8123\n")
	betweenUpReadAndLock(t, func() { writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8125\n") })
	captureRunner(t)
	captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := loadRegistry()
	if got := reg.Mains[main].Ports["APP_PORT"]; got != 8123 {
		t.Errorf("APP_PORT recorded for the main = %d, want 8123 (the read before the lock)", got)
	}
}

// A main record that another run changed while up waited for the lock is newer and stays, as in check (#79). When it
// comes from an older read (a check that waited too), the port this up read is still avoided.
func TestUpKeepsAMainRecordChangedBeforeTheLock(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8124\n")
	// This worktree holds the port the main's .env now sets.
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8124}}})
	betweenUpReadAndLock(t, func() {
		saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8124}},
			Mains: map[string]MainRecord{main: mainRec(8123)}})
	})
	captureRunner(t)
	captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := loadRegistry()
	if rec := reg.Mains[main]; rec.Ports["APP_PORT"] != 8123 || len(rec.Worktrees) != 1 || rec.Worktrees[0] != wt {
		t.Errorf("main record = %+v, want APP_PORT 8123 (changed meanwhile) and the worktree listed", rec)
	}
	if got := reg.Worktrees[wt]["APP_PORT"]; got == 8123 || got == 8124 {
		t.Errorf("worktree APP_PORT = %d, want neither the recorded nor the read port of the main", got)
	}
}

func TestUpDoesNotReadSymlinkedMainEnvForTheRecord(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	target := filepath.Join(t.TempDir(), "env")
	writeFile(t, target, "APP_PORT=8124\n")
	symlinkOrSkip(t, target, filepath.Join(main, ".env"))
	reg := &Registry{Worktrees: map[string]map[string]int{}, Mains: map[string]MainRecord{
		main: {Ports: map[string]int{"APP_PORT": 8200}, Defaults: map[string]int{"APP_PORT": 80}},
	}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	errOut := captureStderr(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadRegistry()
	if got := reg.Mains[main].Ports["APP_PORT"]; got != 8200 {
		t.Errorf("the record was replaced through a link: APP_PORT=%d", got)
	}
	if !strings.Contains(errOut.String(), mainEnvUnreadable(main)) {
		t.Errorf("no warning: %q", errOut.String())
	}
}

func TestUpDoesNotRecordMissingMainProjectDirectory(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, true)
	root := filepath.Join(wt, "laravel")
	writeFakeSail(t, root)
	writeFile(t, filepath.Join(root, ".env"), "APP_NAME=x\n")
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := loadRegistry()
	if len(reg.Mains) != 0 {
		t.Errorf("a missing main project directory was recorded: %+v", reg.Mains)
	}
}

func TestRmForgetsTheWorktreeButKeepsTheMain(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeRmFixtures(t, main, wt)
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}, Mains: map[string]MainRecord{
		main: {Ports: map[string]int{"APP_PORT": 80}, Defaults: map[string]int{"APP_PORT": 80}, Worktrees: []string{"/other", wt}},
	}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	reg, _ = loadRegistry()
	if rec := reg.Mains[main]; len(rec.Worktrees) != 1 || rec.Worktrees[0] != "/other" || rec.Ports["APP_PORT"] != 80 {
		t.Errorf("main record after rm = %+v", rec)
	}
}

func TestRmArgumentsAndConfirmation(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt, wt)
	writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_PROJECT_NAME="+proj+"\n")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	reg := &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	before := registryBytes(t)
	old := stdin
	t.Cleanup(func() { stdin = old })

	// An unknown argument is refused before anything is read or run.
	in := strings.NewReader("y\n")
	stdin = in
	calls := captureRunner(t)
	if err := cmdRm([]string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "unknown argument: --bogus") ||
		in.Len() != 2 || len(*calls) != 0 {
		t.Fatalf("unknown argument: err=%v remaining=%d calls=%d", err, in.Len(), len(*calls))
	}

	// Anything but y or yes (an empty input included) aborts, and both lines go to the package stdout.
	for _, ans := range []string{"n\n", ""} {
		stdin = strings.NewReader(ans)
		out := captureStdout(t)
		if err := cmdRm(nil); err != nil || len(*calls) != 0 {
			t.Fatalf("%q: err=%v calls=%d", ans, err, len(*calls))
		}
		want := fmt.Sprintf("of project %q. Continue? [y/N] aborted\n", proj)
		if !strings.HasSuffix(out.String(), want) {
			t.Errorf("%q: output = %q, want a suffix %q", ans, out.String(), want)
		}
		if string(registryBytes(t)) != string(before) {
			t.Errorf("%q: the registry changed", ans)
		}
	}

	in = strings.NewReader(" YES \n")
	stdin = in
	out := captureStdout(t)
	if err := cmdRm(nil); err != nil || len(*calls) != 1 || in.Len() != 0 {
		t.Fatalf("yes: err=%v calls=%d remaining=%d", err, len(*calls), in.Len())
	}
	if s := out.String(); !strings.Contains(s, "[y/N] ") || strings.Contains(s, "aborted") ||
		!strings.HasSuffix(s, "released the port assignments\n") {
		t.Errorf("yes: output = %q", s)
	}
}

func TestHelpRequested(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		help    bool
		wantErr string
	}{
		{"up", []string{"--help"}, true, ""},
		{"up", []string{"-h", "-d"}, true, ""},
		{"up", []string{"-d", "--help"}, false, ""}, // for sail
		{"stop", []string{"-h"}, true, ""},
		{"stop", nil, false, ""},
		{"rm", []string{"-h"}, true, ""},
		{"rm", []string{"-y", "--help"}, false, "--help cannot be combined with other arguments"},
		{"ps", []string{"--json", "-h"}, false, "-h cannot be combined with other arguments"},
		{"ps", []string{"--json"}, false, ""},
		{"init", []string{"--help", "--bogus"}, false, "--help cannot be combined with other arguments"},
		{"prune", []string{"--help"}, true, ""},
	} {
		help, err := helpRequested(tc.name, tc.args)
		if help != tc.help || (err == nil) != (tc.wantErr == "") || err != nil && err.Error() != tc.wantErr {
			t.Errorf("%s %q: help = %v, err = %v; want %v, %q", tc.name, tc.args, help, err, tc.help, tc.wantErr)
		}
	}
}

// -h and --help print the usage before a command looks at anything: no prompt is read, nothing runs and the registry
// is not written (#63).
func TestEveryCommandAcceptsHelp(t *testing.T) {
	setupWorktreeRepo(t)
	captureStderr(t)
	calls := captureRunner(t)
	in := strings.NewReader("y\n")
	old := stdin
	stdin = in
	t.Cleanup(func() { stdin = old })
	for name := range commands {
		for _, h := range []string{"-h", "--help"} {
			out := captureStdout(t)
			if err := run(name, []string{h}); err != nil || !strings.Contains(out.String(), "Usage:") {
				t.Errorf("%s %s: err = %v, output = %q", name, h, err, out.String())
			}
		}
	}
	if in.Len() != 2 || len(*calls) != 0 {
		t.Errorf("help read stdin or ran a command: remaining=%d calls=%v", in.Len(), *calls)
	}
	if _, err := os.Stat(mustRegistryPath(t)); !os.IsNotExist(err) {
		t.Errorf("help wrote the registry: %v", err)
	}
	if err := run("rm", []string{"-y", "--help"}); err == nil || len(*calls) != 0 {
		t.Errorf("rm -y --help: err = %v, calls = %v", err, *calls)
	}
}

// setupBareWorktreeRepo creates a bare repository (app.git) with two linked worktrees, app-main on main and app-feat
// on feat, and chdirs into app-feat. There is no main worktree checkout (#69).
func setupBareWorktreeRepo(t *testing.T) (bare, mainWt, wt string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	base := realTempDir(t)
	src := filepath.Join(base, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(src, configName), `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80}]}`)
	runGit(t, src, "add", ".")
	runGit(t, src, "commit", "-q", "-m", "init")
	bare = filepath.Join(base, "app.git")
	runGit(t, base, "clone", "-q", "--bare", src, bare)
	mainWt = filepath.Join(base, "app-main")
	wt = filepath.Join(base, "app-feat")
	runGit(t, bare, "worktree", "add", "-q", mainWt, "main")
	runGit(t, bare, "worktree", "add", "-q", "-b", "feat", wt)
	t.Chdir(wt)
	return bare, mainWt, wt
}

func TestParseMainWorktree(t *testing.T) {
	bareOut := "worktree /r/app.git\nbare\n\nworktree /r/app-feat\nHEAD abc\nbranch refs/heads/feat\n"
	if p, bare, err := parseMainWorktree(bareOut, false); err != nil || p != "/r/app.git" || !bare {
		t.Errorf("bare listing: %q %v %v", p, bare, err)
	}
	normal := "worktree /r/app\nHEAD abc\nbranch refs/heads/main\n\nworktree /r/app-feat\nbare\n"
	if p, bare, err := parseMainWorktree(normal, false); err != nil || p != "/r/app" || bare {
		t.Errorf("a bare line of a later record counts: %q %v %v", p, bare, err)
	}
	crlf := strings.ReplaceAll(bareOut, "\n", "\r\n")
	if p, bare, err := parseMainWorktree(crlf, true); err != nil || p != "/r/app.git" || !bare {
		t.Errorf("CRLF listing on Windows: %q %v %v", p, bare, err)
	}
	for _, bad := range []string{"", "worktree \nbare\n\nworktree /r/app-feat\n"} {
		if _, _, err := parseMainWorktree(bad, false); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// A bare line of the next record does not count for the main one, even without a blank line between them.
	if _, bare, err := parseMainWorktree("worktree /r/app\nworktree /r/app.git\nbare\n", false); err != nil || bare {
		t.Errorf("a later record's bare line counts: %v %v", bare, err)
	}
	// A path that starts with a quote is returned as git printed it (ps skips it; see parseWorktreeList).
	if p, _, err := parseMainWorktree("worktree \"r/app\n", false); err != nil || p != `"r/app` {
		t.Errorf("leading quote: %q %v", p, err)
	}
}

func TestParseWorktreeRecords(t *testing.T) {
	out := "bare\nworktree /r/app\nHEAD 1\nbranch refs/heads/main\nworktree /r/a\nprunable gone\n\ndetached\n\nworktree \"q\nbare\n"
	// A bare line before the first record and a key after a blank line belong to no record; a worktree line starts a
	// record without a blank line before it; the path is kept as git printed it.
	got := parseWorktreeRecords(out, false)
	want := []worktreeInfo{{Path: "/r/app", Branch: "main"}, {Path: "/r/a", Prunable: true}, {Path: `"q`, Bare: true}}
	if !slices.Equal(got, want) {
		t.Errorf("records = %+v, want %+v", got, want)
	}
	crlf := "worktree /r/app\r\nbare\r\n\r\nworktree /r/b\r\n"
	if got := parseWorktreeRecords(crlf, true); len(got) != 2 || got[0].Path != "/r/app" || !got[0].Bare || got[1].Path != "/r/b" {
		t.Errorf("CRLF on Windows = %+v", got)
	}
	// Elsewhere \r may be part of a name, so it stays (and "\r" alone is no blank line).
	if got := parseWorktreeRecords(crlf, false); len(got) != 2 || got[0].Path != "/r/app\r" || got[0].Bare {
		t.Errorf("CRLF elsewhere = %+v", got)
	}
}

func TestUpInABareLayoutCopiesOwnEnvExampleAndRecordsNoMain(t *testing.T) {
	bare, mainWt, wt := setupBareWorktreeRepo(t)
	writeFile(t, filepath.Join(mainWt, ".env"), "FROM=main\n")
	writeFile(t, filepath.Join(wt, ".env.example"), "FROM=example\nAPP_URL=http://localhost\n")
	writeFakeSail(t, wt)
	captureRunner(t)
	out := captureStdout(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get("FROM"); v != "example" {
		t.Errorf("FROM=%q, want the worktree's own .env.example", v)
	}
	if v, _ := e.Get("COMPOSE_PROJECT_NAME"); v != projectName(bare, wt, wt) {
		t.Errorf("COMPOSE_PROJECT_NAME=%q, want %q", v, projectName(bare, wt, wt))
	}
	if !strings.Contains(out.String(), "copied from .env.example") {
		t.Errorf("output:\n%s", out)
	}
	reg, _ := loadRegistry()
	if len(reg.Mains) != 0 {
		t.Errorf("a main was recorded in a bare layout: %v", reg.Mains)
	}
	if reg.Worktrees[wt]["APP_PORT"] == 0 {
		t.Errorf("no ports recorded: %v", reg.Worktrees)
	}
}

func TestUpInABareLayoutWithoutEnvExampleFails(t *testing.T) {
	_, _, wt := setupBareWorktreeRepo(t)
	writeFakeSail(t, wt)
	calls := captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), "the repository is bare") || !strings.Contains(err.Error(), "create .env first") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Error(".env was written")
	}
	if len(*calls) != 0 {
		t.Errorf("sail ran: %v", *calls)
	}
}

func TestUpInABareLayoutRefusesALinkedEnvExample(t *testing.T) {
	_, _, wt := setupBareWorktreeRepo(t)
	target := filepath.Join(t.TempDir(), "secret")
	writeFile(t, target, "SECRET=1\n")
	symlinkOrSkip(t, target, filepath.Join(wt, ".env.example"))
	writeFakeSail(t, wt)
	captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Error(".env was written")
	}
}

func TestRmInABareLayout(t *testing.T) {
	bare, _, wt := setupBareWorktreeRepo(t)
	writeRmFixtures(t, bare, wt)
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}}})
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "docker" {
		t.Fatalf("calls = %v", *calls)
	}
	reg, _ := loadRegistry()
	if _, ok := reg.Worktrees[wt]; ok || len(reg.Mains) != 0 {
		t.Errorf("registry = %+v", reg)
	}
}

func TestUpInABareLayoutRefusesOverridesInEnvExample(t *testing.T) {
	_, _, wt := setupBareWorktreeRepo(t)
	writeFile(t, filepath.Join(wt, ".env.example"), "COMPOSE_FILE=other.yaml\n")
	writeFakeSail(t, wt)
	calls := captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.HasPrefix(err.Error(), ".env.example has COMPOSE_FILE") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) || len(*calls) != 0 {
		t.Errorf(".env was written or sail ran: %v", *calls)
	}
}

// The worktree on the main branch is a linked worktree like any other in a bare layout.
func TestUpInABareLayoutRunsInTheMainBranchWorktree(t *testing.T) {
	_, mainWt, _ := setupBareWorktreeRepo(t)
	t.Chdir(mainWt)
	writeFile(t, filepath.Join(mainWt, ".env"), "APP_PORT=80\n")
	writeFakeSail(t, mainWt)
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	if e, _ := readEnv(filepath.Join(mainWt, ".env")); e == nil {
		t.Fatal("no .env")
	} else if v, _ := e.Get("APP_PORT"); v == "" || v == "80" {
		t.Errorf("APP_PORT=%q, want a port above the default", v)
	}
}
