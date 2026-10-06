package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const subdirConfig = `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80}]}`

// setupSubdirWorktreeRepo is setupWorktreeRepo for a Laravel project in laravel/ (config and compose file there) and
// changes into wt/laravel. With onlyOnBranch, laravel/ exists only in the linked worktree (the main worktree is on a
// branch without it).
func setupSubdirWorktreeRepo(t *testing.T, onlyOnBranch bool) (main, wt string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	base := realTempDir(t)
	main = filepath.Join(base, "app")
	wt = filepath.Join(base, "app-feat")
	writeFile(t, filepath.Join(main, "README.md"), "app\n")
	runGit(t, main, "init", "-q", "-b", "main")
	if !onlyOnBranch {
		writeFile(t, filepath.Join(main, "laravel", configName), subdirConfig)
		writeFile(t, filepath.Join(main, "laravel", "compose.yaml"), "services: {}\n")
	}
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-q", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	if onlyOnBranch {
		writeFile(t, filepath.Join(wt, "laravel", configName), subdirConfig)
		writeFile(t, filepath.Join(wt, "laravel", "compose.yaml"), "services: {}\n")
	}
	t.Chdir(filepath.Join(wt, "laravel"))
	return main, wt
}

func mkdirChdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
}

func TestProjectCandidates(t *testing.T) {
	top := filepath.Join(realTempDir(t), "vendor", "wt") // a "vendor" element in the root itself is not skipped
	cases := []struct {
		prefix string
		rels   []string // nil means an error
	}{
		{"", []string{""}},
		{"laravel/", []string{"laravel", ""}},
		{"laravel/app/", []string{"laravel/app", "laravel", ""}},
		{"..foo/", []string{"..foo", ""}},
		{"a/.../", []string{"a/...", "a", ""}},
		{"laravel/vendor/pkg/", []string{"laravel", ""}},
		{"laravel/Node_Modules/x/", []string{"laravel", ""}},
		{"../x/", nil},
		{"/x/", nil},
		{"a/../../b/", nil},
		{"a//b/", nil},
		{"./a/", nil},
	}
	for _, c := range cases {
		got, err := projectCandidates(top, c.prefix)
		if c.rels == nil {
			if err == nil {
				t.Errorf("prefix %q: no error, got %v", c.prefix, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("prefix %q: %v", c.prefix, err)
			continue
		}
		var rels []string
		for _, g := range got {
			rels = append(rels, g.rel)
			if want := filepath.Join(top, filepath.FromSlash(g.rel)); g.dir != want {
				t.Errorf("prefix %q: dir %q, want %q", c.prefix, g.dir, want)
			}
		}
		if strings.Join(rels, "|") != strings.Join(c.rels, "|") {
			t.Errorf("prefix %q: rels %q, want %q", c.prefix, rels, c.rels)
		}
	}
	// On Windows a backslash is a separator, so this element would climb out of the root; elsewhere it is a name.
	got, err := projectCandidates(top, `a\..\..\x/`)
	if filepath.Separator == '\\' {
		if err == nil {
			t.Errorf("a backslash element escaping the root is accepted: %v", got)
		}
	} else if err != nil || len(got) != 2 {
		t.Errorf("a backslash is a plain character here: %v, %v", got, err)
	}
}

func TestParseTopAndPrefix(t *testing.T) {
	abs := filepath.Join(realTempDir(t), "wt")
	cases := []struct {
		out         string
		windows     bool
		top, prefix string
		wantErr     bool
	}{
		{abs + "\nlaravel/\n", false, abs, "laravel/", false},
		{abs + "\n\n", false, abs, "", false},
		{abs + "\n lead/\n", false, abs, " lead/", false},
		{abs + "\r\nlaravel/\r\n", true, abs, "laravel/", false},
		{abs + "\r\nlaravel/\r\n", false, abs + "\r", "laravel/\r", false},
		{abs + "\n", false, "", "", true},
		{abs + "\nbad\nname/\n", false, "", "", true},
		{"\nlaravel/\n", false, "", "", true},
		{"relative\n\n", false, "", "", true},
	}
	for _, c := range cases {
		top, prefix, err := parseTopAndPrefix(c.out, c.windows)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: no error", c.out)
			}
			continue
		}
		if err != nil || top != c.top || prefix != c.prefix {
			t.Errorf("%q: got %q, %q, %v; want %q, %q", c.out, top, prefix, err, c.top, c.prefix)
		}
	}
}

func TestLoadCtxFindsSubdirProject(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	errOut := captureStderr(t)
	c, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if c.root != filepath.Join(wt, "laravel") || c.main != filepath.Join(main, "laravel") || c.wtTop != wt || c.mainTop != main {
		t.Errorf("ctx = %+v", c)
	}
	if errOut.Len() != 0 {
		t.Errorf("project directory printed when run in it: %q", errOut.String())
	}
	mkdirChdir(t, filepath.Join(wt, "laravel", "app", "Http"))
	c, err = loadCtx()
	if err != nil || c.root != filepath.Join(wt, "laravel") {
		t.Fatalf("from a deeper directory: %+v, %v", c, err)
	}
	if want := fmt.Sprintf("project directory: %q\n", c.root); errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

func TestLoadCtxRootProjectFromSubdirectory(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	mkdirChdir(t, filepath.Join(wt, "docs", "x"))
	captureStderr(t)
	c, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if c.root != wt || c.main != main || c.wtTop != wt || c.mainTop != main {
		t.Errorf("ctx = %+v", c)
	}
}

func TestLoadCtxNearestConfigWins(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	writeFile(t, filepath.Join(wt, configName), subdirConfig)
	if c, err := loadCtx(); err != nil || c.root != filepath.Join(wt, "laravel") {
		t.Errorf("in laravel/: %+v, %v", c, err)
	}
	t.Chdir(wt)
	if c, err := loadCtx(); err != nil || c.root != wt {
		t.Errorf("at the root: %+v, %v", c, err)
	}
}

func TestLoadCtxIgnoresMarkersAboveWorktree(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	writeFile(t, filepath.Join(filepath.Dir(wt), configName), subdirConfig)
	writeFile(t, filepath.Join(filepath.Dir(wt), "compose.yaml"), "services: {}\n")
	t.Chdir(wt)
	_, err := loadCtx()
	if err == nil || !strings.Contains(err.Error(), configName+" not found") {
		t.Fatalf("a config above the worktree root was used: %v", err)
	}
}

func TestLoadCtxSkipsVendor(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	pkg := filepath.Join(wt, "laravel", "vendor", "acme", "pkg")
	writeFile(t, filepath.Join(pkg, configName), subdirConfig)
	t.Chdir(pkg)
	errOut := captureStderr(t)
	c, err := loadCtx()
	if err != nil || c.root != filepath.Join(wt, "laravel") {
		t.Fatalf("vendor/ was not skipped: %+v, %v", c, err)
	}
	if !strings.Contains(errOut.String(), "project directory: ") {
		t.Errorf("the chosen directory is not shown: %q", errOut.String())
	}
}

func TestLoadCtxRefusesNonRegularConfig(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	app := filepath.Join(wt, "laravel", "app")
	if err := os.MkdirAll(filepath.Join(app, configName), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(app)
	if _, err := loadCtx(); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("a directory named %s is not refused: %v", configName, err)
	}
}

func TestConfigNotFoundNamesSubdirectories(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	trySymlink(t, filepath.Join(wt, "laravel"), filepath.Join(wt, "linked")) // a link is not entered
	t.Chdir(wt)
	_, err := loadCtx()
	if err == nil {
		t.Fatal("no error at the worktree root")
	}
	msg := err.Error()
	if !strings.Contains(msg, "run this in your Laravel project's directory") || !strings.Contains(msg, configName+` found in: "laravel"`) {
		t.Errorf("no hint: %s", msg)
	}
	if strings.Contains(msg, "linked") {
		t.Errorf("a linked directory is listed: %s", msg)
	}
	// Before init there is only a compose file.
	if err := os.Remove(filepath.Join(wt, "laravel", configName)); err != nil {
		t.Fatal(err)
	}
	_, err = loadCtx()
	if err == nil || !strings.Contains(err.Error(), `a compose file found in: "laravel" (run `+"`sail-worktree init`"+` there first)`) {
		t.Errorf("no init hint: %v", err)
	}
}

func TestUpInSubdirProject(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	root := filepath.Join(wt, "laravel")
	writeFile(t, filepath.Join(main, "laravel", ".env"), "APP_URL=http://localhost\n")
	writeFakeSail(t, root)
	reg := &Registry{Worktrees: map[string]map[string]int{filepath.Join(realTempDir(t), "other"): {"APP_PORT": 81}}}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
	calls := captureRunner(t)
	if err := cmdUp([]string{"-d"}); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := e.Get("COMPOSE_PROJECT_NAME"); v != projectName(main, wt, root) || !strings.HasPrefix(v, "app-app-feat-") {
		t.Errorf("COMPOSE_PROJECT_NAME=%q", v)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env written at the worktree root: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].dir != root || (*calls)[0].name != filepath.Join(root, "vendor", "bin", "sail") {
		t.Fatalf("calls = %+v", *calls)
	}
	reg, err = loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ports, ok := reg.Worktrees[root]
	if !ok || ports["APP_PORT"] == 81 || ports["APP_PORT"] == 80 {
		t.Errorf("registry = %v", reg.Worktrees)
	}
}

func TestUpAndRmRefuseInMainWorktree(t *testing.T) {
	for _, subdir := range []bool{false, true} {
		t.Run(fmt.Sprintf("subdir=%v", subdir), func(t *testing.T) {
			var dir string
			if subdir {
				main, _ := setupSubdirWorktreeRepo(t, false)
				dir = filepath.Join(main, "laravel")
			} else {
				dir, _ = setupWorktreeRepo(t)
			}
			t.Chdir(dir)
			writeFakeSail(t, dir)
			calls := captureRunner(t)
			if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "cannot run in the main worktree") {
				t.Errorf("up: %v", err)
			}
			if len(*calls) != 0 {
				t.Errorf("up ran %v", *calls)
			}
			expectRmRefusedBeforePrompt(t, "cannot run in the main worktree")
			errOut := captureStderr(t)
			writeFile(t, filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=other\n")
			if err := cmdStop(nil); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() != 0 {
				t.Errorf("stop warned in the main worktree: %q", errOut.String())
			}
		})
	}
}

func TestIsMain(t *testing.T) {
	cases := []struct {
		c    ctx
		want bool
	}{
		{ctx{root: "/w/l", main: "/m/l", wtTop: "/w", mainTop: "/m"}, false},
		{ctx{root: "/m/l", main: "/m/l", wtTop: "/m", mainTop: "/m"}, true},
		{ctx{root: "/w/l", main: "/m/l", wtTop: "/m", mainTop: "/m"}, true}, // the roots decide
		{ctx{root: "/m/l", main: "/m/l", wtTop: "/w", mainTop: "/m"}, true}, // defense in depth
	}
	for _, c := range cases {
		if got := c.c.isMain(); got != c.want {
			t.Errorf("isMain(%+v) = %v", c.c, got)
		}
	}
}

func TestStopInSubdirProjectWarnsWithNewName(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	root := filepath.Join(wt, "laravel")
	writeFakeSail(t, root)
	writeFile(t, filepath.Join(root, ".env"), "COMPOSE_PROJECT_NAME=other\n")
	errOut := captureStderr(t)
	calls := captureRunner(t)
	if err := cmdStop(nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].dir != root {
		t.Errorf("calls = %+v", *calls)
	}
	if want := fmt.Sprintf("%+q", projectName(main, wt, root)); !strings.Contains(errOut.String(), want) {
		t.Errorf("warning %q does not name %s", errOut.String(), want)
	}
}

func TestRmInSubdirProject(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	root := filepath.Join(wt, "laravel")
	proj := projectName(main, wt, root)
	writeFile(t, filepath.Join(root, ".env"), "COMPOSE_PROJECT_NAME="+proj+"\n")
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	want := strings.Join(rmArgs(proj, root, filepath.Join(root, "compose.yaml")), " ")
	if len(*calls) != 1 || strings.Join((*calls)[0].args, " ") != want || (*calls)[0].dir != root {
		t.Errorf("calls = %+v, want args %s", *calls, want)
	}
}

func TestRmRefusesComposeOutsideProjectDirectory(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	root := filepath.Join(wt, "laravel")
	writeFile(t, filepath.Join(root, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, root)+"\n")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	if err := os.Remove(filepath.Join(root, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, filepath.Join(wt, "compose.yaml"), filepath.Join(root, "compose.yaml"))
	expectRmRefusedBeforePrompt(t, "resolves outside the project directory")
}

func TestRmFromNestedConfigRefusesRootProjectName(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	sub := filepath.Join(wt, "sub")
	writeFile(t, filepath.Join(sub, configName), subdirConfig)
	writeFile(t, filepath.Join(sub, "compose.yaml"), "services: {}\n")
	writeFile(t, filepath.Join(sub, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n")
	t.Chdir(sub)
	expectRmRefusedBeforePrompt(t, "does not match")
}

func TestLoadCtxThroughSymlinkedCwd(t *testing.T) {
	_, wt := setupSubdirWorktreeRepo(t, false)
	link := filepath.Join(realTempDir(t), "via")
	symlinkOrSkip(t, wt, link)
	t.Chdir(filepath.Join(link, "laravel"))
	c, err := loadCtx()
	if err != nil || c.root != filepath.Join(wt, "laravel") || c.wtTop != wt {
		t.Errorf("ctx = %+v, %v", c, err)
	}
}

func TestLoadCtxWithCwdInOtherCase(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	root := filepath.Join(wt, "laravel")
	upper := strings.ToUpper(root)
	if upper == root {
		t.Skip("the path has no letters to change")
	}
	if _, err := os.Stat(upper); err != nil {
		t.Skip("the file system is case-sensitive")
	}
	t.Chdir(upper)
	captureStderr(t)
	// If this fails, find out why (git or EvalSymlinks keeping the typed case) instead of skipping it.
	c, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if c.root != root || c.projectName() != projectName(main, wt, root) {
		t.Errorf("root = %q, name = %q; want %q, %q", c.root, c.projectName(), root, projectName(main, wt, root))
	}
}

func TestUpWithoutMainCounterpart(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, true)
	writeFakeSail(t, filepath.Join(wt, "laravel"))
	captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", filepath.Join(main, "laravel"))) || !strings.Contains(err.Error(), "same relative path") {
		t.Errorf("error = %v", err)
	}
}

func TestMainCounterpartOutsideMainWorktreeIsRefused(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, true)
	outside := realTempDir(t)
	writeFile(t, filepath.Join(outside, ".env"), "APP_KEY=secret\n")
	if !trySymlink(t, outside, filepath.Join(main, "laravel")) {
		return
	}
	writeFakeSail(t, filepath.Join(wt, "laravel"))
	calls := captureRunner(t)
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "resolves outside the main worktree") {
		t.Errorf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "laravel", ".env")); !os.IsNotExist(err) || len(*calls) != 0 {
		t.Errorf(".env was written or sail ran: %v, %v", err, *calls)
	}
}
