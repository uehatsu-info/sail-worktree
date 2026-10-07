package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const detectCompose = `services:
    laravel.test:
        ports:
            - '${APP_PORT:-80}:80'
        environment:
            - DB_PORT=${DB_PORT:-3306}
`

// setupDetectedRepo creates a main and a linked worktree whose Laravel project (artisan and compose.yaml, no
// .sail-worktree.json) is at rel ("" for the root), and changes into the project in the linked worktree.
func setupDetectedRepo(t *testing.T, rel string) (main, wt string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	base := realTempDir(t)
	main = filepath.Join(base, "app")
	wt = filepath.Join(base, "app-feat")
	writeFile(t, filepath.Join(main, rel, "compose.yaml"), detectCompose)
	writeFile(t, filepath.Join(main, rel, artisanName), "#!/usr/bin/env php\n")
	writeFile(t, filepath.Join(main, rel, ".env.example"), "APP_URL=http://localhost\n")
	runGit(t, main, "init", "-q", "-b", "main")
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-q", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	t.Chdir(filepath.Join(wt, rel))
	return main, wt
}

func TestUpDetectsConfiguration(t *testing.T) {
	for _, rel := range []string{"", "laravel"} {
		t.Run("rel="+rel, func(t *testing.T) {
			main, wt := setupDetectedRepo(t, rel)
			root := filepath.Join(wt, rel)
			if rel != "" {
				mkdirChdir(t, filepath.Join(root, "app"))
			}
			writeFakeSail(t, root)
			captureStderr(t)
			calls := captureRunner(t)
			if err := cmdUp(nil); err != nil {
				t.Fatal(err)
			}
			e, err := readEnv(filepath.Join(root, ".env"))
			if err != nil {
				t.Fatal(err)
			}
			if v, _ := e.Get("APP_PORT"); v == "" || v == "80" {
				t.Errorf("APP_PORT=%q", v)
			}
			if _, ok := e.Get("DB_PORT"); ok {
				t.Error("an environment entry was taken for a port variable")
			}
			if v, _ := e.Get("COMPOSE_PROJECT_NAME"); v != projectName(main, wt, root) {
				t.Errorf("COMPOSE_PROJECT_NAME=%q", v)
			}
			if len(*calls) != 1 || (*calls)[0].dir != root {
				t.Errorf("calls = %+v", *calls)
			}
			reg, _ := loadRegistry()
			if _, ok := reg.Worktrees[root]; !ok {
				t.Errorf("registry = %v", reg.Worktrees)
			}
		})
	}
}

func TestDetectedAndConfiguredProjectsAgree(t *testing.T) {
	_, wt := setupDetectedRepo(t, "laravel")
	c1, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := c1.config(); err != nil || !c1.detected || cfg.Compose != "compose.yaml" || len(cfg.PortVars) != 1 {
		t.Fatalf("detected = %v, %+v, %v", c1.detected, cfg, err)
	}
	writeFile(t, filepath.Join(wt, "laravel", configName), subdirConfig)
	c2, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if c2.detected || c2.root != c1.root || c2.projectName() != c1.projectName() {
		t.Errorf("with the file: %+v; without: %+v", c2, c1)
	}
}

func TestConfigFurtherUpWinsWithWarning(t *testing.T) {
	_, wt := setupDetectedRepo(t, "laravel")
	writeFile(t, filepath.Join(wt, configName), subdirConfig)
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	errOut := captureStderr(t)
	c, err := loadCtx()
	if err != nil {
		t.Fatal(err)
	}
	if c.root != wt || c.detected {
		t.Errorf("root = %q, detected = %v", c.root, c.detected)
	}
	if want := fmt.Sprintf("the nearer Laravel project %q is ignored", filepath.Join(wt, "laravel")); !strings.Contains(errOut.String(), want) {
		t.Errorf("stderr = %q", errOut.String())
	}
	// rm shows it before the prompt.
	errOut.Reset()
	expectRmRefusedBeforePrompt(t, "cannot read .env")
	if !strings.Contains(errOut.String(), "is ignored") {
		t.Errorf("rm did not warn: %q", errOut.String())
	}
}

func TestHalfProjectsAreNotProjects(t *testing.T) {
	_, wt := setupDetectedRepo(t, "laravel")
	docker := filepath.Join(wt, "laravel", "docker")
	writeFile(t, filepath.Join(docker, "compose.yaml"), detectCompose)
	t.Chdir(docker) // compose without artisan: walks on to laravel/
	if c, err := loadCtx(); err != nil || c.root != filepath.Join(wt, "laravel") {
		t.Fatalf("compose without artisan: %+v, %v", c, err)
	}
	if err := os.Remove(filepath.Join(wt, "laravel", artisanName)); err != nil {
		t.Fatal(err)
	}
	_, err := loadCtx()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("found %q, but no artisan next to it", filepath.Join(docker, "compose.yaml"))) {
		t.Errorf("no artisan hint: %v", err)
	}
	// artisan with only a non-standard compose name.
	nested := filepath.Join(wt, "laravel", "nested")
	writeFile(t, filepath.Join(nested, artisanName), "x\n")
	writeFile(t, filepath.Join(nested, "docker-compose.dev.yml"), detectCompose)
	t.Chdir(nested)
	_, err = loadCtx()
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("found artisan in %q, but no compose file", nested)) {
		t.Errorf("no compose hint: %v", err)
	}
}

func TestArtisanDirectoryAndBrokenComposeDoNotStopTheWalk(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	sub := filepath.Join(wt, "tools")
	if err := os.MkdirAll(filepath.Join(sub, artisanName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sub, "compose.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	captureStderr(t)
	if c, err := loadCtx(); err != nil || c.root != wt {
		t.Errorf("ctx = %+v, %v", c, err)
	}
}

func TestDetectionSkipsVendorAndStopsAtTheRoot(t *testing.T) {
	_, wt := setupDetectedRepo(t, "laravel")
	pkg := filepath.Join(wt, "laravel", "vendor", "acme", "pkg")
	writeFile(t, filepath.Join(pkg, artisanName), "x\n")
	writeFile(t, filepath.Join(pkg, "compose.yaml"), detectCompose)
	t.Chdir(pkg)
	captureStderr(t)
	if c, err := loadCtx(); err != nil || c.root != filepath.Join(wt, "laravel") {
		t.Errorf("vendor/ was not skipped: %+v, %v", c, err)
	}
	writeFile(t, filepath.Join(filepath.Dir(wt), artisanName), "x\n")
	writeFile(t, filepath.Join(filepath.Dir(wt), "compose.yaml"), detectCompose)
	t.Chdir(wt)
	if _, err := loadCtx(); err == nil || !strings.Contains(err.Error(), "no Laravel project found") {
		t.Errorf("a project above the worktree root was used: %v", err)
	}
}

func TestRmWithDetectedCompose(t *testing.T) {
	main, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "compose.yml"), detectCompose) // compose.yaml comes first
	writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n")
	expectRmUsesComposeFile(t, filepath.Join(wt, "compose.yaml"))
}

func TestRmRefusesDetectedComposeLinkedOutside(t *testing.T) {
	main, wt := setupDetectedRepo(t, "")
	outside := filepath.Join(realTempDir(t), "compose.yaml")
	writeFile(t, outside, detectCompose)
	if err := os.Remove(filepath.Join(wt, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(wt, "compose.yaml"))
	writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n")
	expectRmRefusedBeforePrompt(t, "resolves outside the project directory")
}

func TestUpPortVariablesRequiredOnlyWhenDetected(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	writeFakeSail(t, wt)
	calls := captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), "no port variable found in") || len(*calls) != 0 {
		t.Fatalf("detected without ports: %v, %v", err, *calls)
	}
	writeFile(t, filepath.Join(wt, configName), `{"compose":"compose.yaml","port_vars":[]}`)
	if err := cmdUp(nil); err != nil || len(*calls) != 1 {
		t.Errorf("a file with no port variables: %v, %v", err, *calls)
	}
}

func TestUpWithoutSailWritesNothing(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	captureRunner(t)
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "not found; run `composer install`") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env was written: %v", err)
	}
	if reg, _ := loadRegistry(); len(reg.Worktrees) != 0 {
		t.Errorf("registry = %v", reg.Worktrees)
	}
}

func TestMainRefusalComesBeforeAConfigurationError(t *testing.T) {
	main, _ := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(main, "compose.yaml"), strings.Repeat("x", maxSmallFile+1))
	t.Chdir(main)
	captureRunner(t)
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "cannot run in the main worktree") {
		t.Errorf("up: %v", err)
	}
	expectRmRefusedBeforePrompt(t, "cannot run in the main worktree")
}

func TestStopWithDetectedConfiguration(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFakeSail(t, wt)
	t.Setenv("APP_PORT", "1")
	calls := captureRunner(t)
	captureStderr(t)
	if err := cmdStop(nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || strings.Contains(strings.Join((*calls)[0].env, "\n"), "APP_PORT=") {
		t.Errorf("calls = %+v", *calls)
	}
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	if err := cmdStop(nil); err != nil || len(*calls) != 2 {
		t.Errorf("stop without port variables: %v", err)
	}
}

func TestOversizedComposeIsAnError(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "compose.yaml"), detectCompose+strings.Repeat("#", maxSmallFile))
	writeFakeSail(t, wt)
	captureRunner(t)
	for name, run := range map[string]func() error{"up": func() error { return cmdUp(nil) }, "stop": func() error { return cmdStop(nil) }} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "is larger than") {
			t.Errorf("%s: %v", name, err)
		}
	}
	expectRmRefusedBeforePrompt(t, "is larger than")
}

func TestDetectPortVarsBoundaries(t *testing.T) {
	compose := `
            - '${APP_PORT:-80}:80'
            - '${VITE_PORT:-5173}:${VITE_PORT:-5173}'
            - 'DB_PORT=${DB_PORT:-3306}'
            - '${BAD_PORT:80}:80'
            - '${ZERO_PORT:-0}:1'
            - '${BIG_PORT:-99999}:1'
            - '${HUGE_PORT:-99999999999999999999}:1'
            - '${MAX_PORT:-65535}:1'
            - '${lower_PORT:-1}:1'
            - '${PORTAL:-1}:1'
`
	var names []string
	for _, v := range detectPortVars(compose) {
		names = append(names, fmt.Sprintf("%s=%d", v.Name, v.Default))
	}
	if got, want := strings.Join(names, ","), "APP_PORT=80,VITE_PORT=5173,MAX_PORT=65535"; got != want {
		t.Errorf("detected %s, want %s", got, want)
	}
	if _, err := allocatePorts([]PortVar{{Name: "MAX_PORT", Default: 65535}}, nil, nil, func(int) bool { return true }); err == nil {
		t.Error("a port above 65535 was assigned")
	}
}

func TestInitWritesIntoTheNearestProject(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	captureStderr(t)
	// A nested app below a root that has a file: init writes into the nested app.
	writeFile(t, filepath.Join(wt, configName), subdirConfig)
	nested := filepath.Join(wt, "apps", "admin")
	writeFile(t, filepath.Join(nested, artisanName), "x\n")
	writeFile(t, filepath.Join(nested, "compose.yaml"), detectCompose)
	t.Chdir(nested)
	if err := cmdInit(); err != nil {
		t.Fatal(err)
	}
	readConfigAt(t, nested)
	if b, _ := os.ReadFile(filepath.Join(wt, configName)); string(b) != subdirConfig {
		t.Errorf("the root file was changed: %s", b)
	}
	// .devcontainer/ (compose, no artisan) of a project without a file: init writes at the Laravel root.
	if err := os.Remove(filepath.Join(wt, configName)); err != nil {
		t.Fatal(err)
	}
	dev := filepath.Join(wt, ".devcontainer")
	writeFile(t, filepath.Join(dev, "docker-compose.yml"), detectCompose)
	t.Chdir(dev)
	if err := cmdInit(); err != nil {
		t.Fatal(err)
	}
	readConfigAt(t, wt)
	if _, err := os.Stat(filepath.Join(dev, configName)); !os.IsNotExist(err) {
		t.Errorf("written into .devcontainer: %v", err)
	}
}

func TestInitUpdatesAndKeepsACustomCompose(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "docker", "compose.dev.yml"), detectCompose)
	writeFile(t, filepath.Join(wt, configName), `{"compose":"docker/compose.dev.yml","port_vars":[]}`)
	out := captureStdoutFile(t)
	if err := cmdInit(); err != nil {
		t.Fatal(err)
	}
	c := readConfigAt(t, wt)
	if c.Compose != "docker/compose.dev.yml" || len(c.PortVars) != 1 {
		t.Errorf("config = %+v", c)
	}
	if s := out(); !strings.Contains(s, "updated ") || !strings.Contains(s, "detected from docker/compose.dev.yml") {
		t.Errorf("stdout = %q", s)
	}
}

func TestLoadConfigDisappeared(t *testing.T) {
	dir := realTempDir(t)
	if _, err := loadConfig(dir); err == nil || err.Error() != fmt.Sprintf("%s disappeared from %q", configName, dir) {
		t.Errorf("error = %v", err)
	}
}

// captureStdoutFile redirects os.Stdout (init prints with fmt.Printf) and returns a function that reads what was
// written.
func captureStdoutFile(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	t.Cleanup(func() { os.Stdout = old; f.Close() })
	return func() string {
		b, _ := os.ReadFile(f.Name())
		return string(b)
	}
}
