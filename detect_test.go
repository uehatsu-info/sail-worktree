package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if _, err := c.config(); err != nil {
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
	captureStderr(t)
	if c, err := loadCtx(); err != nil || c.root != filepath.Join(wt, "laravel") || c.cfgErr != nil {
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
	if c, err := loadCtx(); err != nil || c.root != wt || c.cfgErr != nil {
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
	if c, err := loadCtx(); err != nil || c.root != filepath.Join(wt, "laravel") || c.cfgErr != nil {
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
	out := captureStdout(t)
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "not found; run `composer install`") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "created .env") {
		t.Errorf("reported a .env it did not write: %q", out.String())
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
	if err := cmdInit(nil); err != nil {
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
	if err := cmdInit(nil); err != nil {
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
	if err := cmdInit(nil); err != nil {
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

func TestInitWithAnUnusableComposeValue(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, configName), `{"compose":"missing.yml","port_vars":[]}`)
	if err := os.Remove(filepath.Join(wt, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	captureStdoutFile(t)
	if err := cmdInit(nil); err == nil || !strings.Contains(err.Error(), "no compose file (compose.yaml") {
		t.Errorf("error = %v", err)
	}
	// With a standard name present, init falls back to it.
	writeFile(t, filepath.Join(wt, "compose.yaml"), detectCompose)
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	if c := readConfigAt(t, wt); c.Compose != "compose.yaml" {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigDisappeared(t *testing.T) {
	dir := realTempDir(t)
	if _, err := loadConfig(dir); err == nil || err.Error() != fmt.Sprintf("%s disappeared from %q", configName, dir) {
		t.Errorf("error = %v", err)
	}
}

func TestLoadConfigValidatesPortVars(t *testing.T) {
	dir := realTempDir(t)
	cfg := filepath.Join(dir, configName)
	for _, tc := range []struct{ vars, want string }{
		{`{"name":"","default":80}`, `port_vars[0] (""): name in`},
		{`{"name":"1_PORT","default":80}`, `port_vars[0] ("1_PORT"): name in`},
		{`{"name":"A=B","default":80}`, `port_vars[0] ("A=B"): name in`},
		{`{"name":"A\nB_PORT","default":80}`, `port_vars[0] ("A\nB_PORT"): name in`},
		{`{"name":"A B","default":80}`, `port_vars[0] ("A B"): name in`},
		{`{"name":"APP_PORT","default":80},{"name":"X-PORT","default":80}`, `port_vars[1] ("X-PORT"): name in`},
		{`{"name":"APP_PORT"}`, `port_vars[0] ("APP_PORT"): default (0)`},
		{`{"name":"APP_PORT","default":-1}`, `port_vars[0] ("APP_PORT"): default (-1)`},
		{`{"name":"APP_PORT","default":65536}`, `port_vars[0] ("APP_PORT"): default (65536)`},
		{`{"name":"PATH","default":80}`, `port_vars[0] ("PATH"): name in .sail-worktree.json is reserved: the shell`},
		{`{"name":"home","default":80}`, `port_vars[0] ("home"): name in .sail-worktree.json is reserved: the shell`},
		{`{"name":"COMPOSE_FILE","default":80}`, `port_vars[0] ("COMPOSE_FILE"): name in .sail-worktree.json is reserved: it would override`},
		{`{"name":"Compose_Ansi","default":80}`, `port_vars[0] ("Compose_Ansi"): name in .sail-worktree.json is reserved: compose reads it`},
		{`{"name":"docker_context","default":80}`, `port_vars[0] ("docker_context"): name in .sail-worktree.json is reserved: docker`},
		{`{"name":"SAIL_FILES","default":80}`, `port_vars[0] ("SAIL_FILES"): name in .sail-worktree.json is reserved: it would override`},
		{`{"name":"DOCKER_HOST","default":80}`, `port_vars[0] ("DOCKER_HOST"): name in .sail-worktree.json is reserved: docker`},
		{`{"name":"WWWUSER","default":80}`, `port_vars[0] ("WWWUSER"): name in .sail-worktree.json is reserved: Sail's`},
		{`{"name":"APP_ENV","default":80}`, `port_vars[0] ("APP_ENV"): name in .sail-worktree.json is reserved: Sail's`},
		{`{"name":"COMPOSE_PROJECT_NAME","default":80}`, `port_vars[0] ("COMPOSE_PROJECT_NAME"): name in .sail-worktree.json is reserved: up writes it`},
		{`{"name":"SESSION_COOKIE","default":80}`, `port_vars[0] ("SESSION_COOKIE"): name in .sail-worktree.json is reserved: up writes it`},
		{`{"name":"APP_URL","default":80}`, `port_vars[0] ("APP_URL"): name in .sail-worktree.json is reserved: up writes it`},
		{`{"name":"SANCTUM_STATEFUL_DOMAINS","default":80}`, `port_vars[0] ("SANCTUM_STATEFUL_DOMAINS"): name in .sail-worktree.json is reserved: up writes it`},
		{`{"name":"APP_PORT","default":80},{"name":"APP_PORT","default":81}`, `port_vars[1] ("APP_PORT"): name in .sail-worktree.json repeats port_vars[0] ("APP_PORT") (names are compared case-insensitively)`},
		{`{"name":"APP_PORT","default":80},{"name":"DB_PORT","default":3306},{"name":"app_Port","default":80}`, `port_vars[2] ("app_Port"): name in .sail-worktree.json repeats port_vars[0] ("APP_PORT")`},
	} {
		writeFile(t, cfg, `{"compose":"compose.yaml","port_vars":[`+tc.vars+`]}`)
		if _, err := loadConfig(dir); err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want prefix %s", tc.vars, err, tc.want)
		}
	}
	// A hand-added variable need not follow the detection rule; the bounds themselves are ports.
	writeFile(t, cfg, `{"compose":"compose.yaml","port_vars":[{"name":"vite_port","default":1},{"name":"_X","default":65535},{"name":"PATH_PORT","default":2},{"name":"APP_URL_PORT","default":3},{"name":"APP_PORT","default":80},{"name":"APP_PORT_2","default":80},{"name":"compose_x_port","default":80},{"name":"DOCKER_WEB_PORT","default":8080},{"name":"docker_web_port_2","default":8081},{"name":"Compose_Web_Port","default":8082}]}`)
	if c, err := loadConfig(dir); err != nil || len(c.PortVars) != 10 {
		t.Errorf("valid port_vars: %+v, %v", c, err)
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

// Only the settings compose and docker read are reserved, so DOCKER_* and COMPOSE_* port variables that v0.8.1
// handled are detected, written by init and up, and kept out of sail's environment like any port variable (#74).
func TestDetectionAcceptsDockerAndComposePortNames(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services:\n  app:\n    ports:\n      - '${APP_PORT:-80}:80'\n      - '${DOCKER_WEB_PORT:-8080}:81'\n      - '${COMPOSE_WEB_PORT:-8090}:82'\n")
	want := []PortVar{{"APP_PORT", 80}, {"DOCKER_WEB_PORT", 8080}, {"COMPOSE_WEB_PORT", 8090}}
	if c, err := detectConfig(wt, "compose.yaml"); err != nil || !slices.Equal(c.PortVars, want) {
		t.Fatalf("detectConfig: %+v, %v", c, err)
	}
	writeFakeSail(t, wt)
	t.Setenv("DOCKER_WEB_PORT", "1")
	t.Setenv("COMPOSE_WEB_PORT", "2")
	captureStderr(t)
	captureStdout(t)
	calls := captureRunner(t)
	if err := cmdUp(nil); err != nil {
		t.Fatalf("up: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	env := "\n" + string(b)
	for _, k := range []string{"DOCKER_WEB_PORT=", "COMPOSE_WEB_PORT="} {
		if !strings.Contains(env, "\n"+k) {
			t.Errorf(".env has no %s line:\n%s", k, env)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	for _, kv := range (*calls)[0].env {
		if strings.HasPrefix(kv, "DOCKER_WEB_PORT=") || strings.HasPrefix(kv, "COMPOSE_WEB_PORT=") {
			t.Errorf("sail's environment has %s", kv)
		}
	}

	captureStdoutFile(t)
	if err := cmdInit(nil); err != nil {
		t.Fatalf("init: %v", err)
	}
	if c, err := loadConfig(wt); err != nil || !slices.Equal(c.PortVars, want) {
		t.Errorf("loadConfig after init: %+v, %v", c, err)
	}
}

// A .sail-worktree.json that lists a reserved name is a configuration error for stop and rm too (#63).
func TestReservedNameInConfigBlocksStopAndRm(t *testing.T) {
	main, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_PROJECT_NAME="+projectName(main, wt, wt)+"\n")
	writeFakeSail(t, wt)
	captureStderr(t)
	captureStdout(t)
	calls := captureRunner(t)
	writeFile(t, filepath.Join(wt, configName), `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80},{"name":"DOCKER_HOST","default":81}]}`)
	want := `port_vars[1] ("DOCKER_HOST"): name in .sail-worktree.json is reserved`
	if err := cmdStop(nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("stop with %s: error = %v", configName, err)
	}
	if err := cmdRm([]string{"-y"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("rm with %s: error = %v", configName, err)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused command ran %v", *calls)
	}
}

// stop's and rm's exception (teardownConfig) returns the detected configuration, and only for a reservedPortVarError;
// TestDetectedReservedNameRefusedOnlyByUpAndInit goes through detection.
func TestTeardownConfigTakesTheDetectedConfiguration(t *testing.T) {
	cfg := &Config{Compose: "compose.yaml", PortVars: []PortVar{{"X_PORT", 80}}}
	c := &ctx{cfgErr: &reservedPortVarError{msg: "reserved", detected: cfg}, teardown: cfg}
	if got, err := c.teardownConfig(); err != nil || got != cfg {
		t.Errorf("teardownConfig = %v, %v", got, err)
	}
	if _, err := c.config(); err == nil {
		t.Error("config() accepts a reserved name")
	}
	broken := &ctx{cfgErr: errors.New("broken")}
	if _, err := broken.teardownConfig(); err == nil {
		t.Error("teardownConfig hides another configuration error")
	}
}

// A repeated port variable is a configuration error like the others: up, stop and rm fail before running anything,
// status reports it and check only warns.
func TestRepeatedPortVariableIsAConfigurationError(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	cfg := `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80},{"name":"app_port","default":80}]}`
	writeFile(t, filepath.Join(wt, configName), cfg)
	want := `port_vars[1] ("app_port"): name in .sail-worktree.json repeats port_vars[0] ("APP_PORT")`
	for name, run := range map[string]func() error{
		"up":   func() error { return cmdUp(nil) },
		"stop": func() error { return cmdStop(nil) },
		"rm":   func() error { return cmdRm([]string{"-y"}) },
	} {
		calls := captureRunner(t)
		if err := run(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v", name, err)
		}
		if len(*calls) != 0 {
			t.Errorf("%s ran %v", name, *calls)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Errorf("a command wrote .env: %v", err)
	}

	writeFile(t, filepath.Join(wt, ".env"), "APP_PORT=81\n")
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}})
	probePortFake(t, true)
	out, _, err := runStatus(t)
	if err == nil || !strings.Contains(out, "configuration: "+want) {
		t.Errorf("status: err = %v\n%s", err, out)
	}

	writeFile(t, filepath.Join(main, configName), cfg)
	t.Chdir(main)
	out, err = runCheck(t)
	if err != nil || !strings.Contains(out, "no main worktree was recorded: "+want) {
		t.Errorf("check: err = %v\n%s", err, out)
	}
}

// Detection yields each variable once, keeping the first valid default, so init never writes a file that
// validatePortVars refuses as repeated.
func TestDetectionListsARepeatedVariableOnce(t *testing.T) {
	_, wt := setupDetectedRepo(t, "")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services:\n  app:\n    ports:\n      - '${APP_PORT:-80}:80'\n      - '${APP_PORT:-81}:8080'\n      - '${VITE_PORT:-0}:1'\n      - '${VITE_PORT:-5173}:5173'\n      - '${VITE_PORT:-5174}:5174'\n")
	captureStdoutFile(t)
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(wt)
	if err != nil {
		t.Fatal(err)
	}
	want := []PortVar{{"APP_PORT", 80}, {"VITE_PORT", 5173}}
	if !slices.Equal(c.PortVars, want) {
		t.Errorf("port_vars = %v, want %v", c.PortVars, want)
	}
}

func TestReservedPortVarNames(t *testing.T) {
	// Written out so that the list cannot lose one unnoticed: the keys up writes, the shell and Sail names, and the
	// settings that send compose to other files or docker to another daemon, the proxy and SSH agent names, the
	// BuildKit and buildx settings, and the names Sail's bin/sail reads. The map's own keys are checked too, which
	// catches a key that is not upper-case.
	reserved := []string{"SHELL", "USER", "PWD", "IFS", "APP_SERVICE", "WWWGROUP", "PATH", "HOME", "WWWUSER", "APP_ENV",
		"COMPOSE_PROJECT_NAME", "SESSION_COOKIE", "APP_URL", "SANCTUM_STATEFUL_DOMAINS", "SAIL_FILES",
		"COMPOSE_FILE", "COMPOSE_ENV_FILES", "COMPOSE_PROFILES", "COMPOSE_PATH_SEPARATOR", "COMPOSE_DISABLE_ENV_FILE",
		"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
		"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "NO_PROXY", "ALL_PROXY", "SSH_AUTH_SOCK",
		"BUILDKIT_COLORS", "BUILDKIT_HOST", "BUILDKIT_PROGRESS", "BUILDKIT_TTY_LOG_LINES",
		"EXPERIMENTAL_BUILDKIT_SOURCE_POLICY",
		"BUILDX_BAKE_DISABLE_VARS_ENV_LOOKUP", "BUILDX_BAKE_FILE", "BUILDX_BAKE_FILE_RELATIVE_PATHS",
		"BUILDX_BAKE_FILE_SEPARATOR", "BUILDX_BAKE_GIT_ADVICE", "BUILDX_BAKE_GIT_AUTH_HEADER",
		"BUILDX_BAKE_GIT_AUTH_TOKEN", "BUILDX_BAKE_GIT_SSH", "BUILDX_BUILDER", "BUILDX_CONFIG", "BUILDX_CPU_PROFILE",
		"BUILDX_EXPERIMENTAL", "BUILDX_GIT_CHECK_DIRTY", "BUILDX_GIT_INFO", "BUILDX_GIT_LABELS", "BUILDX_MEM_PROFILE",
		"BUILDX_METADATA_PROVENANCE", "BUILDX_METADATA_WARNINGS", "BUILDX_NO_DEFAULT_ATTESTATIONS",
		"BUILDX_NO_DEFAULT_LOAD", "BUILDX_NO_DEFAULT_OCI_ARTIFACT", "BUILDX_SEND_GIT_QUERY_AS_INPUT",
		"UID", "APP_USER", "SAIL_DOCKER_BINARY", "SAIL_SKIP_CHECKS", "SAIL_SHARE_DASHBOARD", "SAIL_SHARE_DOMAIN", "SAIL_SHARE_SERVER",
		"SAIL_SHARE_SERVER_HOST", "SAIL_SHARE_SERVER_PORT", "SAIL_SHARE_SUBDOMAIN", "SAIL_SHARE_TOKEN"}
	for n := range reservedNames {
		reserved = append(reserved, n)
	}
	for _, n := range reserved {
		for _, v := range []string{n, strings.ToLower(n), strings.ToUpper(n[:1]) + strings.ToLower(n[1:])} {
			if _, ok := reservedPortVar(v); !ok {
				t.Errorf("%s is not reserved", v)
			}
		}
	}
	for _, n := range []string{"APP_PORT", "FORWARD_DB_PORT", "VITE_PORT", "PATH_PORT", "SAIL_FILES_PORT",
		"DOCKER_WEB_PORT", "docker_web_port", "COMPOSE_WEB_PORT", "Compose_Web_Port", "DOCKER_X", "COMPOSE_X",
		"SAIL_SHARE_PORT", "HTTP_PROXY_PORT", "BUILDKIT_PORT", "BUILDX_PORT", "SSH_PORT"} {
		if why, ok := reservedPortVar(n); ok {
			t.Errorf("%s is reserved: %s", n, why)
		}
	}
}

// Detection yields only names ending in PORT, and SAIL_SHARE_SERVER_PORT is the only reserved one. If another one
// matched, check TestDetectedReservedNameRefusedOnlyByUpAndInit's paths for it before adding it.
func TestOnlySailShareServerPortIsDetected(t *testing.T) {
	for n := range reservedNames {
		vars := detectPortVars("      - '${" + n + ":-80}:80'\n")
		if n == "SAIL_SHARE_SERVER_PORT" {
			if len(vars) != 1 || vars[0].Name != n {
				t.Errorf("%s is not detected: %v", n, vars)
			}
		} else if len(vars) != 0 {
			t.Errorf("%s is detected: %v", n, vars)
		}
	}
}

// A compose file that maps SAIL_SHARE_SERVER_PORT (a name Sail's share command reads) is refused by up and init,
// which would write the name into .env or .sail-worktree.json; stop and rm write neither and still run.
func TestDetectedReservedNameRefusedOnlyByUpAndInit(t *testing.T) {
	main, wt := setupDetectedRepo(t, "")
	compose := detectCompose + "            - '${SAIL_SHARE_SERVER_PORT:-8080}:8080'\n"
	writeFile(t, filepath.Join(wt, "compose.yaml"), compose)
	env := "COMPOSE_PROJECT_NAME=" + projectName(main, wt, wt) + "\n"
	writeFile(t, filepath.Join(wt, ".env"), env)
	writeFakeSail(t, wt)
	captureStderr(t)
	captureStdout(t)
	calls := captureRunner(t)
	want := `port variable "SAIL_SHARE_SERVER_PORT" in "compose.yaml" cannot be used: ` + sailUses
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("up: error = %v", err)
	}
	if err := cmdInit(nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("init: error = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); string(b) != env {
		t.Errorf("up wrote .env: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(wt, configName)); !os.IsNotExist(err) {
		t.Errorf("init wrote %s: %v", configName, err)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused command ran: %s %v", (*calls)[0].name, (*calls)[0].args)
	}
	t.Setenv("SAIL_SHARE_SERVER_PORT", "1")
	if err := cmdStop(nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(*calls) != 1 || slices.Contains((*calls)[0].env, "SAIL_SHARE_SERVER_PORT=1") {
		t.Errorf("stop: calls = %+v", *calls)
	}
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	// rm keeps the shell value (cleanEnv(nil)); down -v removes by the pinned project name.
	rm := (*calls)[len(*calls)-1]
	if len(*calls) != 2 || rm.name != "docker" || !slices.Contains(rm.env, "SAIL_SHARE_SERVER_PORT=1") ||
		!slices.Contains(rm.args, projectName(main, wt, wt)) {
		t.Errorf("rm: %s %v", rm.name, rm.args)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); string(b) != env {
		t.Errorf("stop or rm wrote .env: %q", b)
	}
}
