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

var errBoom = errors.New("boom")

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
		fmt.Sprintf("project directory: %q", wt),
		"app-feat (branch feat)",
		"compose project:   " + projectName(main, wt, wt),
		"configuration:     " + configName,
		"docker:            stopped",
		".env:              ok",
		"APP_PORT",
		"env=81 registry=81 host=free",
		`APP_URL:           "http://localhost:81"`,
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
			appendEnv(t, wt, "COMPOSE_FILE=/other.yaml")
		}, "COMPOSE_FILE"},
		{"project name differs", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "COMPOSE_PROJECT_NAME", "other")
		}, `COMPOSE_PROJECT_NAME in .env is "other"`},
		{"project name missing", func(t *testing.T, _, wt string) {
			dropEnv(t, wt, "COMPOSE_PROJECT_NAME=")
		}, "COMPOSE_PROJECT_NAME is missing"},
		{"port variable missing", func(t *testing.T, _, wt string) {
			dropEnv(t, wt, "APP_PORT=")
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
			writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, filepath.Join(realTempDir(t), "elsewhere"): {"FORWARD_PORT": 81}})
		}, "port 81 (APP_PORT) is also recorded for"},
		{"port held by a main worktree", func(t *testing.T, _, wt string) {
			writeRegistryWithMains(t, map[string]map[string]int{wt: {"APP_PORT": 81}},
				map[string]MainRecord{filepath.Join(realTempDir(t), "gone-main"): viewMain(map[string]int{"FORWARD_PORT": 81})})
		}, "port 81 (APP_PORT) is also recorded for main worktree"},
		{"APP_URL uses another port", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_URL", "http://localhost:9000")
		}, "does not use APP_PORT 81"},
		{"APP_URL without a port", func(t *testing.T, _, wt string) {
			setEnv(t, wt, "APP_URL", "http://localhost")
		}, "does not use APP_PORT 81"},
		{"sanctum entry missing", func(t *testing.T, _, wt string) {
			appendEnv(t, wt, "SANCTUM_STATEFUL_DOMAINS=localhost:80")
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
	if err == nil || !strings.Contains(out, ".env:              symbolic link") || !strings.Contains(out, "is a symbolic link") {
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
	if err == nil || !strings.Contains(out, ".env:              hard link") || !strings.Contains(out, "hard-linked") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestStatusWarnsAboutPortsInUseOnlyWhenNotRunning(t *testing.T) {
	main, wt := statusSetup(t, "")
	name := projectName(main, wt, wt)
	calls := probePortFake(t, false)

	useDocker(t, `[{"Name":"`+name+`","Status":"running(1)"}]`, nil)
	out, _, err := runStatus(t)
	if err != nil || strings.Contains(out, "warnings:") || len(*calls) != 0 ||
		!strings.Contains(out, "in use by this project") {
		t.Errorf("running: err = %v, probes = %v\n%s", err, *calls, out)
	}

	useDocker(t, `[]`, nil)
	out, _, err = runStatus(t)
	if err != nil {
		t.Errorf("a warning changed the exit status: %v", err)
	}
	for _, want := range []string{"warnings:", "port 81 (APP_PORT) cannot be bound", "host=cannot bind"} {
		if !strings.Contains(out, want) {
			t.Errorf("down: output lacks %q:\n%s", want, out)
		}
	}
	if len(*calls) != 1 { // one probe feeds both the host column and the warning
		t.Errorf("probed %d times", len(*calls))
	}

	useDocker(t, "", errBoom)
	*calls = nil
	out, _, err = runStatus(t)
	if err != nil || strings.Contains(out, "warnings:") || len(*calls) != 0 ||
		!strings.Contains(out, "docker:            unknown") {
		t.Errorf("unknown: err = %v, probes = %v\n%s", err, *calls, out)
	}
}

func TestStatusNoDockerDoesNotAsk(t *testing.T) {
	statusSetup(t, "")
	calls := useDocker(t, `[]`, nil)
	out, _, err := runStatus(t, "--no-docker")
	if err != nil || len(*calls) != 0 || !strings.Contains(out, "docker:            not asked (--no-docker)") {
		t.Errorf("err = %v, calls = %v\n%s", err, *calls, out)
	}
}

func TestStatusHidesAppURLCredentials(t *testing.T) {
	_, wt := statusSetup(t, "")
	setEnv(t, wt, "APP_URL", "http://user:"+statusSecret+"@localhost:81")
	appendEnv(t, wt, "SANCTUM_STATEFUL_DOMAINS=${APP_URL}")
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
	// stop runs in the main worktree too, so the line names only up and rm.
	want := "this is the main worktree: up and rm do not run here; check records its ports"
	if err != nil || !strings.Contains(out, want) {
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

func TestStatusEscapesMainWorktreeKeys(t *testing.T) {
	_, wt := statusSetup(t, "")
	evil := filepath.Join(string(filepath.Separator), "x", "a\u202eb\x1b[31m")
	writeRegistryWithMains(t, map[string]map[string]int{wt: {"APP_PORT": 81}},
		map[string]MainRecord{evil: viewMain(map[string]int{"EVIL\x1b_PORT": 81})})
	out, _, err := runStatus(t)
	if err == nil || !strings.Contains(out, "also recorded for main worktree") {
		t.Fatalf("err = %v\n%s", err, out)
	}
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

// editEnv rewrites wt/.env through f, which gets and returns its lines.
func editEnv(t *testing.T, wt string, f func(lines []string) []string) {
	t.Helper()
	p := filepath.Join(wt, ".env")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := f(strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"))
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendEnv(t *testing.T, wt, line string) {
	t.Helper()
	editEnv(t, wt, func(l []string) []string { return append(l, line) })
}

// setEnv replaces the lines of key, or appends one.
func setEnv(t *testing.T, wt, key, value string) {
	t.Helper()
	editEnv(t, wt, func(lines []string) []string {
		env := &envFile{lines: lines}
		env.Set(key, value)
		return env.lines
	})
}

// dropEnv removes the lines that start with prefix.
func dropEnv(t *testing.T, wt, prefix string) {
	t.Helper()
	editEnv(t, wt, func(lines []string) []string {
		var keep []string
		for _, l := range lines {
			if !strings.HasPrefix(l, prefix) {
				keep = append(keep, l)
			}
		}
		return keep
	})
}

func TestStatusNeverEchoesInvalidPortValues(t *testing.T) {
	_, wt := statusSetup(t, "")
	// A port variable name comes from a committed file, so it can be any key, a secret's included.
	writeFile(t, filepath.Join(wt, configName), `{"compose":"compose.yaml","port_vars":[{"name":"DB_PASSWORD","default":3306}]}`)
	appendEnv(t, wt, "DB_PASSWORD=topsecret-value")
	out, errOut, err := runStatus(t)
	if err == nil || !strings.Contains(out, "DB_PASSWORD in .env is not a port number") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out+errOut, "topsecret-value") {
		t.Errorf("the value was printed:\n%s", out)
	}
}

func TestStatusSkipsAppURLCheckForAnInvalidAppPort(t *testing.T) {
	for _, bad := range []string{"99999", "-1", "0"} {
		t.Run(bad, func(t *testing.T) {
			_, wt := statusSetup(t, "")
			setEnv(t, wt, "APP_PORT", bad)
			out, errOut, err := runStatus(t)
			if err == nil || !strings.Contains(out, "APP_PORT in .env is not a port number") {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if strings.Contains(out+errOut, "does not use APP_PORT") || strings.Contains(out+errOut, "APP_PORT "+bad) ||
				strings.Contains(out+errOut, "="+bad) {
				t.Errorf("the invalid value took part in the APP_URL check:\n%s%s", out, errOut)
			}
		})
	}
}

func TestStatusHidesAppURLQueryAndFragment(t *testing.T) {
	_, wt := statusSetup(t, "")
	setEnv(t, wt, "APP_URL", "http://localhost:9000/?token="+statusSecret+"#"+statusSecret)
	out, errOut, _ := runStatus(t) // the port differs, so the URL is also quoted in a problem
	if strings.Contains(out+errOut, statusSecret) || !strings.Contains(out, `"http://localhost:9000/"`) {
		t.Errorf("output:\n%s", out)
	}
}

func TestStatusProblemOrderIsStable(t *testing.T) {
	_, wt := statusSetup(t, "")
	base := realTempDir(t)
	writeRegistry(t, map[string]map[string]int{
		wt:                            {"APP_PORT": 81},
		filepath.Join(base, "b"):      {"APP_PORT": 81},
		filepath.Join(base, "a"):      {"APP_PORT": 81, "FORWARD_PORT": 81},
		filepath.Join(base, "c", "d"): {"X_PORT": 81},
	})
	first, _, err := runStatus(t)
	if err == nil || strings.Count(first, "is also recorded for") != 4 {
		t.Fatalf("err = %v\n%s", err, first)
	}
	for i := 0; i < 15; i++ {
		if again, _, _ := runStatus(t); again != first {
			t.Fatalf("output changed between runs:\n%s\n---\n%s", first, again)
		}
	}
}

func TestStatusWarnsAboutASanctumValueUpOnlyWarnsAbout(t *testing.T) {
	_, wt := statusSetup(t, "")
	appendEnv(t, wt, "SANCTUM_STATEFUL_DOMAINS=${APP_URL}")
	out, _, err := runStatus(t)
	if err != nil || !strings.Contains(out, "warnings:") || !strings.Contains(out, "is not a plain list") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestStatusFromASubdirectoryNamesTheProjectDirectoryOnBothStreams(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	_ = main
	mkdirChdir(t, filepath.Join(wt, "laravel", "app", "Http"))
	out, errOut, _ := runStatus(t)
	if !strings.HasPrefix(out, fmt.Sprintf("project directory: %q", filepath.Join(wt, "laravel"))) ||
		!strings.Contains(errOut, "project directory:") {
		t.Errorf("stdout:\n%s\nstderr: %q", out, errOut)
	}
}

// A port variable set on two lines of the worktree's own .env is a warning: status shows the first line, Sail and
// compose use the last (#63). Values are not shown, and other keys set twice are not mentioned.
func TestStatusWarnsAboutAPortKeySetTwice(t *testing.T) {
	statusSetup(t, "APP_PORT=8123\nAPP_NAME=a\nAPP_NAME=b\n")
	out, _, err := runStatus(t)
	if err != nil {
		t.Fatalf("a duplicate changed the exit status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "  - APP_PORT is set on more than one line in .env; status shows the first line") {
		t.Errorf("no warning about APP_PORT:\n%s", out)
	}
	if strings.Contains(out, "8123") || strings.Contains(out, "APP_NAME") {
		t.Errorf("a value or another key is shown:\n%s", out)
	}
}

// The continuation lines of a warning or a problem are indented under its text, so that they cannot pass for a
// heading or an item; control characters other than the newline are still escaped (#76).
func TestReportIndentsContinuationLines(t *testing.T) {
	var b strings.Builder
	r := &report{w: &b}
	r.warn("a\nb")
	r.warn("c\rd")
	r.problem("e\nproblems:")
	if err := r.finish(); err == nil {
		t.Error("a problem does not fail")
	}
	want := "warnings:\n  - a\n    b\n  - c\\rd\nproblems:\n  - e\n    problems:\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// status labels other holders as check does (holder.String), so a name is escaped by cell: a newline is written out
// instead of starting an indented continuation line.
func TestStatusOtherHoldersUseCheckLabels(t *testing.T) {
	_, wt := statusSetup(t, "")
	other := filepath.Join(realTempDir(t), "elsewhere")
	main := filepath.Join(realTempDir(t), "gone-main")
	writeRegistryWithMains(t, map[string]map[string]int{wt: {"APP_PORT": 81}, other: {"X\nPORT": 81}},
		map[string]MainRecord{main: viewMain(map[string]int{"FORWARD_PORT": 81})})
	out, _, err := runStatus(t)
	if err == nil {
		t.Fatalf("no error\n%s", out)
	}
	for _, want := range []string{
		fmt.Sprintf("port 81 (APP_PORT) is also recorded for %s", holder{dir: other, name: "X\nPORT"}),
		fmt.Sprintf("port 81 (APP_PORT) is also recorded for worktree %q X\\nPORT", other),
		fmt.Sprintf("port 81 (APP_PORT) is also recorded for main worktree %q FORWARD_PORT", main),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A value that is not a port number holds nothing, as in check and ports, even if two records share it.
func TestOtherHoldersIgnoreInvalidPorts(t *testing.T) {
	view := map[string]map[string]int{"/a": {"APP_PORT": 70000}, "/b": {"APP_PORT": 70000}}
	mains := map[string]mainView{"/m": {rec: viewMain(map[string]int{"APP_PORT": 70000})}}
	for _, port := range []int{0, -1, 70000} {
		if got := otherHolders(view, mains, "/a", port); got != nil {
			t.Errorf("otherHolders(%d) = %q, want none", port, got)
		}
	}
	if got := otherHolders(map[string]map[string]int{"/b": {"APP_PORT": 81}}, nil, "/a", 81); len(got) != 1 {
		t.Errorf("otherHolders(81) = %q", got)
	}
}

// status still counts a replaced main record as another holder (up avoids its ports), and says that it is replaced.
func TestStatusLabelsReplacedMainHolders(t *testing.T) {
	_, wt := statusSetup(t, "")
	stale, live := aliasMain(t)
	writeRegistryWithMains(t, map[string]map[string]int{wt: {"APP_PORT": 81}},
		map[string]MainRecord{stale: viewMain(map[string]int{"APP_PORT": 81}), live: viewMain(map[string]int{"APP_PORT": 82})})
	out, _, err := runStatus(t)
	want := fmt.Sprintf("port 81 (APP_PORT) is also recorded for main worktree %q APP_PORT (replaced by %q)", stale, live)
	if err == nil || !strings.Contains(out, want) {
		t.Errorf("err = %v; missing %q in:\n%s", err, want, out)
	}
}
