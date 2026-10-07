package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const statusSecret = "hunter2-secret"

// statusSetup is a linked worktree whose .env, registry entry and APP_URL agree. extra is appended to .env.
func statusSetup(t *testing.T, extra string) (main, wt string) {
	t.Helper()
	main, wt = setupWorktreeRepo(t)
	env := "APP_URL=http://localhost:81\nAPP_PORT=81\nCOMPOSE_PROJECT_NAME=" + projectName(main, wt, wt) +
		"\nAPP_KEY=base64:" + statusSecret + "\nDB_PASSWORD=" + statusSecret + "\n" + extra
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}})
	probePortFake(t, true)
	return main, wt
}

// probePortFake replaces probePort; the calls are returned.
func probePortFake(t *testing.T, free bool) *[]int {
	t.Helper()
	var calls []int
	old := probePort
	probePort = func(p int) bool { calls = append(calls, p); return free }
	t.Cleanup(func() { probePort = old })
	return &calls
}

func runStatus(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	if !psWithDocker {
		args = append([]string{"--no-docker"}, args...)
	}
	o, e := captureStdout(t), captureStderr(t)
	err = cmdStatus(args)
	return o.String(), e.String(), err
}

func TestStatusHealthy(t *testing.T) {
	main, wt := statusSetup(t, "")
	useDocker(t, `[{"Name":"`+projectName(main, wt, wt)+`","Status":"exited(1)"}]`, nil)
	out, errOut, err := runStatus(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, s := range []string{
		fmt.Sprintf("project directory: %q", wt), "app-feat (branch feat)", "compose project:   " + projectName(main, wt, wt),
		"configuration:     " + configName, "docker:            stopped", ".env:              ok",
		"APP_PORT", "env=81 registry=81 host=free", `APP_URL:           "http://localhost:81"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "problems:") || strings.Contains(out, "warnings:") {
		t.Errorf("unexpected findings:\n%s", out)
	}
	for _, s := range []string{statusSecret, "APP_KEY", "DB_PASSWORD"} {
		if strings.Contains(out+errOut, s) {
			t.Errorf("%q was printed:\n%s", s, out+errOut)
		}
	}
}

func TestStatusProblems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, main, wt string)
		want   string
	}{
		{"missing .env", func(t *testing.T, _, wt string) {
			os.Remove(filepath.Join(wt, ".env"))
		}, ".env is missing"},
		{"override key", func(t *testing.T, _, wt string) {
			appendFile(t, filepath.Join(wt, ".env"), "COMPOSE_FILE=/other.yaml\n")
		}, "COMPOSE_FILE"},
		{"project name differs", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "COMPOSE_PROJECT_NAME", "other")
		}, `COMPOSE_PROJECT_NAME in .env is "other"`},
		{"project name missing", func(t *testing.T, _, wt string) {
			rewrite(t, wt, func(l string) bool { return strings.HasPrefix(l, "COMPOSE_PROJECT_NAME=") })
		}, "COMPOSE_PROJECT_NAME is missing"},
		{"port variable missing", func(t *testing.T, _, wt string) {
			rewrite(t, wt, func(l string) bool { return strings.HasPrefix(l, "APP_PORT=") })
		}, "APP_PORT is not set in .env"},
		{"port is not a number", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_PORT", "eighty")
		}, "not a port number"},
		{".env and registry differ", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_PORT", "82")
			setEnv(t, wt, "APP_URL", "http://localhost:82")
		}, "APP_PORT is 82 in .env but 81 in the registry"},
		{"not in the registry", func(t *testing.T, _, wt string) {
			writeRegistry(t, map[string]map[string]int{})
		}, "APP_PORT is not recorded in the registry"},
		{"port held by another entry", func(t *testing.T, _, wt string) {
			writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, "/elsewhere": {"FORWARD_PORT": 81}})
		}, "port 81 (APP_PORT) is also recorded for"},
		{"APP_URL uses another port", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_URL", "http://localhost:9000")
		}, "does not use APP_PORT 81"},
		{"APP_URL without a port", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_URL", "http://localhost")
		}, "does not use APP_PORT 81"},
		{"sanctum entry missing", func(t *testing.T, _, wt string) {
			appendFile(t, filepath.Join(wt, ".env"), "SANCTUM_STATEFUL_DOMAINS=localhost:80\n")
		}, `SANCTUM_STATEFUL_DOMAINS lacks "localhost:81"`},
		{"broken configuration", func(t *testing.T, _, wt string) {
			writeFile(t, filepath.Join(wt, configName), "{not json")
		}, "configuration:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			main, wt := statusSetup(t, "")
			c.mutate(t, main, wt)
			out, _, err := runStatus(t)
			if err == nil || !strings.Contains(err.Error(), "problem(s)") {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if !strings.Contains(out, "problems:") || !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
		})
	}
}

func TestStatusSymlinkedEnvIsReportedNotRead(t *testing.T) {
	_, wt := statusSetup(t, "")
	target := filepath.Join(realTempDir(t), "target.env")
	writeFile(t, target, "APP_KEY=base64:"+statusSecret+"\nCOMPOSE_FILE=/evil\n")
	os.Remove(filepath.Join(wt, ".env"))
	symlinkOrSkip(t, target, filepath.Join(wt, ".env"))
	out, errOut, err := runStatus(t)
	if err == nil || !strings.Contains(out, "is a symbolic link") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out+errOut, statusSecret) || strings.Contains(out, "COMPOSE_FILE") {
		t.Errorf("the link target was read:\n%s", out)
	}
}

func TestStatusHardLinkedEnvIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard links are detected on Unix only")
	}
	_, wt := statusSetup(t, "")
	if err := os.Link(filepath.Join(wt, ".env"), filepath.Join(realTempDir(t), "other")); err != nil {
		t.Fatal(err)
	}
	out, _, err := runStatus(t)
	if err == nil || !strings.Contains(out, "hard-linked") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestStatusWarnsAboutPortsInUseOnlyWhenNotRunning(t *testing.T) {
	main, wt := statusSetup(t, "")
	name := projectName(main, wt, wt)
	calls := probePortFake(t, false)

	useDocker(t, `[{"Name":"`+name+`","Status":"running(1)"}]`, nil)
	out, _, err := runStatus(t)
	if err != nil || strings.Contains(out, "warnings:") || len(*calls) != 0 || !strings.Contains(out, "in use by this project") {
		t.Errorf("running: err = %v, probes = %v\n%s", err, *calls, out)
	}

	useDocker(t, `[]`, nil)
	out, _, err = runStatus(t)
	if err != nil {
		t.Errorf("a warning changed the exit status: %v", err)
	}
	if !strings.Contains(out, "warnings:") || !strings.Contains(out, "port 81 (APP_PORT) cannot be bound") || !strings.Contains(out, "host=cannot bind") {
		t.Errorf("down:\n%s", out)
	}

	useDocker(t, "", errBoom)
	*calls = nil
	out, _, err = runStatus(t)
	if err != nil || strings.Contains(out, "warnings:") || len(*calls) != 0 || !strings.Contains(out, "docker:            unknown") {
		t.Errorf("unknown: err = %v, probes = %v\n%s", err, *calls, out)
	}
}

func TestStatusNoDockerDoesNotAsk(t *testing.T) {
	statusSetup(t, "")
	calls := useDocker(t, `[]`, nil)
	out, _, err := runStatus(t, "--no-docker")
	if err != nil || len(*calls) != 0 || !strings.Contains(out, "docker:            -") {
		t.Errorf("err = %v, calls = %v\n%s", err, *calls, out)
	}
}

func TestStatusHidesAppURLCredentials(t *testing.T) {
	_, wt := statusSetup(t, "")
	setEnv(t, wt, "APP_URL", "http://user:"+statusSecret+"@localhost:81")
	appendFile(t, filepath.Join(wt, ".env"), "SANCTUM_STATEFUL_DOMAINS=${APP_URL}\n")
	out, errOut, err := runStatus(t)
	if strings.Contains(out+errOut, statusSecret) {
		t.Errorf("credentials were printed (err = %v):\n%s", err, out+errOut)
	}
	if !strings.Contains(out, `APP_URL:           "http://localhost:81"`) {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusInTheMainWorktree(t *testing.T) {
	main, _ := statusSetup(t, "")
	t.Chdir(main)
	out, _, err := runStatus(t)
	if err != nil || !strings.Contains(out, "this is the main worktree") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestStatusEscapesUntrustedText(t *testing.T) {
	_, wt := statusSetup(t, "")
	setEnv(t, wt, "APP_PORT", "8\x1b[31m0\u202e")
	out, _, _ := runStatus(t)
	for _, bad := range []string{"\x1b", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%q", bad, out)
		}
	}
}

func TestStatusWritesNothing(t *testing.T) {
	_, wt := statusSetup(t, "SANCTUM_STATEFUL_DOMAINS=localhost:80\n")
	regPath := mustRegistryPath(t)
	files := []string{filepath.Join(wt, ".env"), filepath.Join(wt, configName), regPath}
	read := func() (s string) {
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			fi, _ := os.Stat(f)
			s += f + fi.ModTime().String() + string(b)
		}
		return s
	}
	before := read()
	runStatus(t)
	if after := read(); after != before {
		t.Errorf("files changed:\n%s\n---\n%s", before, after)
	}
}

func TestStatusBrokenRegistryIsAProblem(t *testing.T) {
	statusSetup(t, "")
	writeFile(t, mustRegistryPath(t), "{not json")
	out, _, err := runStatus(t)
	if err == nil || !strings.Contains(out, "failed to parse") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestStatusRejectsArguments(t *testing.T) {
	statusSetup(t, "")
	if _, _, err := runStatus(t, "--bogus"); err == nil || !strings.Contains(err.Error(), "unknown argument: --bogus") {
		t.Errorf("err = %v", err)
	}
}

var errBoom = errors.New("boom")

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, s...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setEnv sets key in wt/.env (replacing the first line of the key).
func setEnv(t *testing.T, wt, key, value string) {
	t.Helper()
	p := filepath.Join(wt, ".env")
	env, err := readEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	env.Set(key, value)
	if err := os.WriteFile(p, []byte(strings.Join(env.lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewrite drops the lines of wt/.env for which drop is true.
func rewrite(t *testing.T, wt string, drop func(string) bool) {
	t.Helper()
	p := filepath.Join(wt, ".env")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if !drop(l) {
			keep = append(keep, l)
		}
	}
	if err := os.WriteFile(p, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
