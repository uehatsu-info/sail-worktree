package main

import (
	"encoding/json"
	"errors"
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
		{"/", nil},
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
	if err == nil || !strings.Contains(err.Error(), "no Laravel project found") {
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
	if !strings.Contains(msg, "run this in your Laravel project's directory") || !strings.Contains(msg, `a project found in: "laravel"`) {
		t.Errorf("no hint: %s", msg)
	}
	if strings.Contains(msg, "linked") {
		t.Errorf("a linked directory is listed: %s", msg)
	}
	// Without the file, a compose file is a project only next to artisan.
	if err := os.Remove(filepath.Join(wt, "laravel", configName)); err != nil {
		t.Fatal(err)
	}
	if _, err = loadCtx(); err == nil || strings.Contains(err.Error(), "a project found in") {
		t.Errorf("a compose file without artisan is listed: %v", err)
	}
	writeFile(t, filepath.Join(wt, "laravel", artisanName), "#!/usr/bin/env php\n")
	if _, err = loadCtx(); err == nil || !strings.Contains(err.Error(), `a project found in: "laravel"`) {
		t.Errorf("no hint for a project without the file: %v", err)
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
	if v, _ := e.Get("APP_URL"); !strings.HasPrefix(v, "http://localhost:") {
		t.Errorf("APP_URL=%q (not copied from the main worktree's laravel/.env)", v)
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

// A main worktree that has a directory at the project's relative path, but no project there, is not recorded: such a
// main never runs, and a record would reserve the defaults (#95).
func TestMainCounterpartThatIsNotAProjectIsNotRecorded(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, true)
	other := filepath.Join(main, "laravel")
	writeFile(t, filepath.Join(other, "README.md"), "unrelated\n")
	root := filepath.Join(wt, "laravel")
	writeFakeSail(t, root)
	writeFile(t, filepath.Join(root, ".env"), "APP_NAME=x\n")
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	if reg, _ := loadRegistry(); len(reg.Mains) != 0 {
		t.Errorf("up recorded a main counterpart that is not a project: %+v", reg.Mains)
	}
	captureStderr(t)
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if want := fmt.Sprintf("no main worktree was recorded: the main worktree has no project directory at %q", other); !strings.Contains(out, want) {
		t.Errorf("no warning %q:\n%s", want, out)
	}
	if reg, _ := loadRegistry(); len(reg.Mains) != 0 {
		t.Errorf("check recorded a main counterpart that is not a project: %+v", reg.Mains)
	}
}

// A counterpart without .sail-worktree.json but with artisan and a compose file is a project by lookupProject's rule.
func TestMainCounterpartThatIsALaravelProjectIsRecorded(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, true)
	other := filepath.Join(main, "laravel")
	writeFile(t, filepath.Join(other, artisanName), "")
	writeFile(t, filepath.Join(other, "compose.yaml"), "services: {}\n")
	root := filepath.Join(wt, "laravel")
	writeFakeSail(t, root)
	writeFile(t, filepath.Join(root, ".env"), "APP_NAME=x\n")
	captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	reg, _ := loadRegistry()
	if _, ok := reg.Mains[other]; !ok || len(reg.Mains) != 1 {
		t.Errorf("the Laravel project in the main worktree was not recorded: %+v", reg.Mains)
	}
}

func TestIsProjectDir(t *testing.T) {
	mk := func(files ...string) string {
		dir := realTempDir(t)
		for _, f := range files {
			writeFile(t, filepath.Join(dir, f), "")
		}
		return dir
	}
	badConfig := realTempDir(t)
	if err := os.Mkdir(filepath.Join(badConfig, configName), 0o755); err != nil {
		t.Fatal(err)
	}
	artisanDir := realTempDir(t)
	if err := os.Mkdir(filepath.Join(artisanDir, artisanName), 0o755); err != nil {
		t.Fatal(err)
	}
	composeDir := mk(artisanName)
	if err := os.Mkdir(filepath.Join(composeDir, "compose.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		dir             string
		want, surelyNot bool
	}{
		"plain directory":                {mk("README.md"), false, true},
		"configuration":                  {mk(configName), true, false},
		"artisan and compose file":       {mk(artisanName, "docker-compose.yml"), true, false},
		"artisan alone":                  {mk(artisanName), false, true},
		"compose file alone":             {mk("compose.yaml"), false, true},
		"configuration that is a folder": {badConfig, false, true},
		"artisan that is a folder":       {artisanDir, false, true},
		"compose file that is a folder":  {composeDir, false, true},
	} {
		if got := isProjectDir(c.dir); got != c.want {
			t.Errorf("%s: isProjectDir = %v, want %v", name, got, c.want)
		}
		if got := surelyNotProject(c.dir); got != c.surelyNot {
			t.Errorf("%s: surelyNotProject = %v, want %v", name, got, c.surelyNot)
		}
	}
}

// surelyNotProject does not follow a marker that is a link: whatever it leads to, the directory is in doubt and its
// record is kept (#102).
func TestSurelyNotProjectLeavesLinkedMarkersInDoubt(t *testing.T) {
	target := filepath.Join(realTempDir(t), "target")
	writeFile(t, target, "")
	for _, c := range []struct {
		name, link string
		files      []string // regular files next to the link
		dangling   bool
	}{
		{"configuration", configName, nil, false},
		{"dangling configuration", configName, nil, true},
		{"artisan", artisanName, []string{"compose.yaml"}, false},
		{"compose file", "compose.yaml", []string{artisanName}, false},
		{"dangling compose file", "compose.yaml", []string{artisanName}, true},
	} {
		dir := realTempDir(t)
		for _, f := range c.files {
			writeFile(t, filepath.Join(dir, f), "")
		}
		to := target
		if c.dangling {
			to = filepath.Join(dir, "missing")
		}
		symlinkOrSkip(t, to, filepath.Join(dir, c.link))
		if surelyNotProject(dir) {
			t.Errorf("%s: a linked marker was taken as absent or followed", c.name)
		}
	}
}

func TestMainCounterpartOutsideMainWorktreeIsRefused(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, true)
	outside := realTempDir(t)
	writeFile(t, filepath.Join(outside, ".env"), "APP_KEY=secret\n")
	symlinkOrSkip(t, outside, filepath.Join(main, "laravel"))
	writeFakeSail(t, filepath.Join(wt, "laravel"))
	calls := captureRunner(t)
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "resolves outside the main worktree") {
		t.Errorf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "laravel", ".env")); !os.IsNotExist(err) || len(*calls) != 0 {
		t.Errorf(".env was written or sail ran: %v, %v", err, *calls)
	}
}

func TestWithin(t *testing.T) {
	base := filepath.Join(realTempDir(t), "wt")
	for rel, want := range map[string]bool{
		".":                            true,
		"a":                            true,
		"..foo":                        true,
		filepath.Join("a", ".."):       true,
		"..":                           false,
		filepath.Join("..", "x"):       false,
		filepath.Join("..", "wt2"):     false,
		filepath.Join("a", "..", ".."): false,
	} {
		if got := within(base, filepath.Join(base, rel)); got != want {
			t.Errorf("within(base, base/%s) = %v", rel, got)
		}
	}
}

// initRepo is a main worktree only (init runs there), changed into dir below it.
func initRepo(t *testing.T, files map[string]string, dir string) (top string) {
	t.Helper()
	top = filepath.Join(realTempDir(t), "app")
	for name, content := range files {
		writeFile(t, filepath.Join(top, filepath.FromSlash(name)), content)
	}
	runGit(t, top, "init", "-q", "-b", "main")
	mkdirChdir(t, filepath.Join(top, filepath.FromSlash(dir)))
	return top
}

func readConfigAt(t *testing.T, dir string) Config {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, configName))
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInitInSubdirectory(t *testing.T) {
	top := initRepo(t, map[string]string{"laravel/compose.yaml": sampleCompose, "laravel/artisan": "x\n"}, "laravel/app/Http")
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	c := readConfigAt(t, filepath.Join(top, "laravel"))
	if c.Compose != "compose.yaml" || len(c.PortVars) != 3 {
		t.Errorf("config = %+v", c)
	}
	if _, err := os.Stat(filepath.Join(top, configName)); !os.IsNotExist(err) {
		t.Errorf("config written at the worktree root: %v", err)
	}
}

func TestInitAtRootNamesSubdirectory(t *testing.T) {
	top := initRepo(t, map[string]string{"laravel/compose.yaml": sampleCompose, "laravel/artisan": "x\n"}, ".")
	err := cmdInit(nil)
	if err == nil {
		t.Fatal("init succeeded without a compose file at the root")
	}
	for _, want := range []string{"no Laravel project found", "run this in your Laravel project's directory", `a project found in: "laravel"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is missing: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(top, configName)); !os.IsNotExist(err) {
		t.Errorf("config written: %v", err)
	}
}

func TestInitRootProjectFromSubdirectory(t *testing.T) {
	top := initRepo(t, map[string]string{"compose.yaml": sampleCompose, "artisan": "x\n", "docs/x.md": "x\n"}, "docs")
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	if c := readConfigAt(t, top); c.Compose != "compose.yaml" {
		t.Errorf("config = %+v", c)
	}
}

func TestInitSkipsComposeUnderVendor(t *testing.T) {
	top := initRepo(t, map[string]string{"compose.yaml": sampleCompose, "artisan": "x\n", "vendor/acme/pkg/docker-compose.yml": sampleCompose, "vendor/acme/pkg/artisan": "x\n"}, "vendor/acme/pkg")
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	readConfigAt(t, top)
	if _, err := os.Stat(filepath.Join(top, "vendor", "acme", "pkg", configName)); !os.IsNotExist(err) {
		t.Errorf("config written under vendor/: %v", err)
	}
}

func TestInitFirstExistingComposeNameDecides(t *testing.T) {
	initRepo(t, map[string]string{"compose.yml": sampleCompose, "compose.yaml/x": "x\n", "artisan": "x\n"}, ".")
	if err := cmdInit(nil); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("a directory named compose.yaml is not refused: %v", err)
	}
}

// init replaces .sail-worktree.json, so --help and a mistyped option must leave it alone (#39).
func TestInitArgumentsWriteNothing(t *testing.T) {
	const hand = "{\"compose\": \"compose.yaml\", \"port_vars\": []}\n"
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"--help"}, ""},
		{[]string{"-h"}, ""},
		{[]string{"--help", "--bogus"}, "--help cannot be combined with other arguments"},
		{[]string{"--bogus", "-h"}, "-h cannot be combined with other arguments"},
		{[]string{"--bogus"}, "unknown argument: --bogus"},
		{[]string{"compose.yaml"}, "unknown argument: compose.yaml"},
	} {
		for _, existing := range []bool{true, false} {
			files := map[string]string{"compose.yaml": sampleCompose, "artisan": "x\n"}
			if existing {
				files[configName] = hand
			}
			top := initRepo(t, files, ".")
			out := captureStdout(t)
			err := run("init", tc.args)
			if tc.wantErr == "" {
				if err != nil || !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "sail-worktree init") {
					t.Errorf("%q: err = %v, output = %q; want the usage", tc.args, err, out.String())
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Errorf("%q: err = %v, want %q", tc.args, err, tc.wantErr)
			}
			b, err := os.ReadFile(filepath.Join(top, configName))
			switch {
			case existing && (err != nil || string(b) != hand):
				t.Errorf("%q: %s was changed: %q, %v", tc.args, configName, b, err)
			case !existing && !os.IsNotExist(err):
				t.Errorf("%q: %s was written: %v", tc.args, configName, err)
			}
		}
	}
}

func TestInitRefusesLinkedConfig(t *testing.T) {
	top := initRepo(t, map[string]string{"compose.yaml": sampleCompose, "artisan": "x\n"}, ".")
	target := filepath.Join(realTempDir(t), "target")
	writeFile(t, target, "keep\n")
	symlinkOrSkip(t, target, filepath.Join(top, configName))
	err := cmdInit(nil)
	if err == nil || !strings.Contains(err.Error(), "replace it with a real file") {
		t.Errorf("a linked %s is not refused: %v", configName, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Errorf("link target was rewritten: %q", b)
	}
}

func TestUpRootProjectWithoutSourceEnv(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), "cannot copy .env from the main worktree's project directory") || strings.Contains(err.Error(), "same relative path") {
		t.Errorf("error = %v", err)
	}
}

// A main counterpart that is a project but whose .env and .env.example cannot be read gets a message that names the
// files and the directory, not the hint about a missing project (#127). It fails before the lock and any write.
func TestUpCannotReadTheMainsEnvFiles(t *testing.T) {
	for _, sub := range []bool{true, false} {
		for _, exampleDir := range []bool{false, true} {
			t.Run(fmt.Sprintf("subdir=%v/exampleDir=%v", sub, exampleDir), func(t *testing.T) {
				var main, root string
				if sub {
					m, wt := setupSubdirWorktreeRepo(t, false)
					main, root = filepath.Join(m, "laravel"), filepath.Join(wt, "laravel")
				} else {
					main, root = setupWorktreeRepo(t)
				}
				if exampleDir {
					if err := os.Mkdir(filepath.Join(main, ".env.example"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				writeFakeSail(t, root)
				calls := captureRunner(t)
				locked := false
				orig := upLock
				t.Cleanup(func() { upLock = orig })
				upLock = func() (func(), error) { locked = true; return orig() }
				err := cmdUp(nil)
				want := fmt.Sprintf("cannot copy .env from the main worktree's project directory %q: reading .env (or .env.example when there is no .env) failed: ", main)
				if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "; create .env in this worktree first") ||
					strings.Contains(err.Error(), "same relative path") {
					t.Fatalf("error = %v", err)
				}
				if exampleDir != strings.Contains(err.Error(), "is not a regular file") {
					t.Errorf("error = %v", err)
				}
				if _, err := os.Lstat(filepath.Join(root, ".env")); !os.IsNotExist(err) || len(*calls) != 0 || locked {
					t.Errorf(".env written, sail ran or the lock was taken: %v, %v, %v", err, *calls, locked)
				}
			})
		}
	}
}

// A main .env that is a link to a missing file is refused, not replaced by .env.example: the user meant the link's
// target (#138). The refusal comes before the lock and any write; without the link, .env.example is still copied.
func TestUpRefusesADanglingMainEnvLink(t *testing.T) {
	for _, sub := range []bool{true, false} {
		t.Run(fmt.Sprintf("subdir=%v", sub), func(t *testing.T) {
			var main, root string
			if sub {
				m, wt := setupSubdirWorktreeRepo(t, false)
				main, root = filepath.Join(m, "laravel"), filepath.Join(wt, "laravel")
			} else {
				main, root = setupWorktreeRepo(t)
			}
			link := filepath.Join(main, ".env")
			symlinkOrSkip(t, filepath.Join(realTempDir(t), "missing"), link)
			writeFile(t, filepath.Join(main, ".env.example"), "FROM=example\n")
			writeFakeSail(t, root)
			calls := captureRunner(t)
			captureStdout(t)
			captureStderr(t)
			locked := false
			orig := upLock
			t.Cleanup(func() { upLock = orig })
			upLock = func() (func(), error) { locked = true; return orig() }
			err := cmdUp(nil)
			want := fmt.Sprintf("cannot copy .env: the main worktree's .env, %q, is a symbolic link whose target does not exist; fix the link, or create .env in this worktree first", link)
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %s", err, want)
			}
			if _, err := os.Lstat(filepath.Join(root, ".env")); !os.IsNotExist(err) || len(*calls) != 0 || locked {
				t.Errorf(".env written, sail ran or the lock was taken: %v, %v, %v", err, *calls, locked)
			}

			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := cmdUp(nil); err != nil {
				t.Fatal(err)
			}
			env, err := readEnv(filepath.Join(root, ".env"))
			if err != nil {
				t.Fatal(err)
			}
			if v, _ := env.Get("FROM"); v != "example" {
				t.Errorf("FROM = %q, want .env.example's value", v)
			}
		})
	}
}

func TestInitWithoutAnyComposeFile(t *testing.T) {
	initRepo(t, map[string]string{"README.md": "x\n"}, ".")
	err := cmdInit(nil)
	if err == nil || !strings.Contains(err.Error(), "no Laravel project found") || strings.Contains(err.Error(), "found in:") {
		t.Errorf("error = %v", err)
	}
}

func TestInitWithoutPortVariables(t *testing.T) {
	top := initRepo(t, map[string]string{"laravel/compose.yaml": "services: {}\n", "laravel/artisan": "x\n"}, "laravel")
	err := cmdInit(nil)
	if want := fmt.Sprintf("no port variable found in %q (a host port mapping such as '${APP_PORT:-80}:80'); add one to the compose file, or write .sail-worktree.json by hand (see README)", filepath.Join(top, "laravel", "compose.yaml")); err == nil || err.Error() != want {
		t.Errorf("error = %v, want %s", err, want)
	}
}

func TestWriteFileNoFollowMarksRefusals(t *testing.T) {
	dir := realTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileNoFollow(filepath.Join(dir, "d"), []byte("x"), 0o600); !errors.Is(err, errNotOwnFile) {
		t.Errorf("a directory is not marked as a refusal: %v", err)
	}
	if err := writeFileNoFollow(filepath.Join(dir, "missing", "f"), []byte("x"), 0o600); err == nil || errors.Is(err, errNotOwnFile) {
		t.Errorf("an I/O error is marked as a refusal: %v", err)
	}
}

// up does not copy .env from the main worktree's counterpart when it is not a project directory, even if it holds a
// .env: Sail sources the copy, so it must come from the project. The refusal comes before the lock and any write (#103).
func TestUpDoesNotCopyEnvFromANonProjectCounterpart(t *testing.T) {
	for _, sub := range []bool{true, false} {
		t.Run(fmt.Sprintf("subdir=%v", sub), func(t *testing.T) {
			var main, root string
			if sub {
				m, wt := setupSubdirWorktreeRepo(t, true)
				main, root = filepath.Join(m, "laravel"), filepath.Join(wt, "laravel")
				writeFile(t, filepath.Join(main, "README.md"), "unrelated\n")
			} else {
				m, wt := setupWorktreeRepo(t)
				if err := os.Remove(filepath.Join(m, configName)); err != nil {
					t.Fatal(err)
				}
				main, root = m, wt
			}
			writeFile(t, filepath.Join(main, ".env"), "APP_KEY=unrelated\n")
			writeFile(t, filepath.Join(main, ".env.example"), "APP_KEY=unrelated\n")
			writeFakeSail(t, root)
			calls := captureRunner(t)
			locked := false
			orig := upLock
			t.Cleanup(func() { upLock = orig })
			upLock = func() (func(), error) { locked = true; return orig() }
			err := cmdUp(nil)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q, is not a project directory", main)) ||
				!strings.Contains(err.Error(), "create .env in this worktree first") {
				t.Fatalf("error = %v", err)
			}
			if sub != strings.Contains(err.Error(), "same relative path") {
				t.Errorf("error = %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, ".env")); !os.IsNotExist(err) || len(*calls) != 0 || locked {
				t.Errorf(".env written, sail ran or the lock was taken: %v, %v, %v", err, *calls, locked)
			}
			if reg, _ := loadRegistry(); len(reg.Worktrees) != 0 || len(reg.Mains) != 0 {
				t.Errorf("registry = %+v", reg)
			}
		})
	}
}
