package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type owner struct {
	name string
	pid  int
}

// portsFakes replaces probePort (a port is free unless busy[port]) and lookupOwner, and records both kinds of call.
func portsFakes(t *testing.T, busy map[int]bool, owners map[int]owner) (probes, lookups *[]int) {
	t.Helper()
	var pr, lk []int
	oldProbe, oldOwner := probePort, lookupOwner
	probePort = func(p int) bool { pr = append(pr, p); return !busy[p] }
	lookupOwner = func(p int) (string, int, bool) {
		lk = append(lk, p)
		o, ok := owners[p]
		return o.name, o.pid, ok
	}
	t.Cleanup(func() { probePort, lookupOwner = oldProbe, oldOwner })
	return &pr, &lk
}

func runPorts(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	if !psWithDocker {
		args = append([]string{"--no-docker"}, args...)
	}
	o, e := captureStdout(t), captureStderr(t)
	err = cmdPorts(args)
	return o.String(), e.String(), err
}

func portsJSON(t *testing.T, args ...string) []portRow {
	t.Helper()
	out, _, err := runPorts(t, append([]string{"--json"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var rows []portRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return rows
}

// portsSetup: the current repository has two worktrees with ports 81, 3307 and 82; another repository has one with 90.
func portsSetup(t *testing.T) (main, wt, other string) {
	t.Helper()
	main, wt = setupWorktreeRepo(t)
	other = filepath.Join(filepath.Dir(wt), "app-other")
	runGit(t, main, "worktree", "add", "-q", other, "-b", "other")
	_, foreign := newRepoWithWorktree(t, realTempDir(t), "foreign", "x")
	writeRegistry(t, map[string]map[string]int{
		wt:      {"APP_PORT": 81, "FORWARD_DB_PORT": 3307},
		other:   {"APP_PORT": 82},
		foreign: {"APP_PORT": 90},
	})
	return main, wt, other
}

func TestParseLsof(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
		pid  int
		ok   bool
	}{
		{"one process", "p123\ncphp\nf5\n", "php", 123, true},
		{"first process wins", "p123\ncphp\nf5\np456\ncnode\n", "php", 123, true},
		{"command with spaces", "p9\ncGoogle Chrome\n", "Google Chrome", 9, true},
		{"empty", "", "", 0, false},
		{"pid without a name", "p12\n", "", 0, false},
		{"name without a pid", "cphp\n", "", 0, false},
		{"bad pid", "pabc\ncphp\n", "", 0, false},
		{"zero pid", "p0\ncphp\n", "", 0, false},
		{"carriage returns", "p5\r\ncphp\r\n", "", 0, false}, // a bad pid line ("5\r") is ignored
		{"long name is cut", "p3\nc" + strings.Repeat("x", 200) + "\n", strings.Repeat("x", maxProcessName), 3, true},
		{"noise", "garbage\n\n\x00\np7\nxjunk\ncsshd\n", "sshd", 7, true},
	}
	for _, c := range cases {
		name, pid, ok := parseLsof(c.out)
		if name != c.want || pid != c.pid || ok != c.ok {
			t.Errorf("%s: got (%q, %d, %v), want (%q, %d, %v)", c.name, name, pid, ok, c.want, c.pid, c.ok)
		}
	}
}

func TestLsofOwnerRunsAFixedCommand(t *testing.T) {
	var got []dockerCall
	old := output
	output = func(dir string, env []string, _ time.Duration, name string, args ...string) ([]byte, error) {
		got = append(got, dockerCall{dir, env, name, args})
		return []byte("p42\ncnginx\n"), nil
	}
	t.Cleanup(func() { output = old })
	t.Setenv("COMPOSE_FILE", "/x")
	name, pid, ok := lsofOwner(8080)
	if runtime.GOOS == "windows" {
		if ok || len(got) != 0 {
			t.Errorf("lsof was used on Windows: %v", got)
		}
		return
	}
	if !ok || name != "nginx" || pid != 42 || len(got) != 1 {
		t.Fatalf("got (%q, %d, %v) after %d calls", name, pid, ok, len(got))
	}
	c := got[0]
	if c.name != "lsof" || strings.Join(c.args, " ") != "-nP -w -iTCP:8080 -sTCP:LISTEN -Fcp" {
		t.Errorf("call = %+v", c)
	}
	for _, kv := range c.env {
		if !strings.HasPrefix(kv, "PATH=") && kv != "LC_ALL=C" {
			t.Errorf("unexpected variable %q passed to lsof", kv)
		}
	}
}

func TestLsofOwnerFailureIsNotAnOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lsof is not used on Windows")
	}
	old := output
	output = func(string, []string, time.Duration, string, ...string) ([]byte, error) { return nil, errBoom }
	t.Cleanup(func() { output = old })
	if _, _, ok := lsofOwner(80); ok {
		t.Error("a failing lsof gave an owner")
	}
}

func TestPortsListsTheCurrentRepository(t *testing.T) {
	_, wt, _ := portsSetup(t)
	portsFakes(t, nil, nil)
	out, errOut, err := runPorts(t)
	if err != nil || errOut != "" {
		t.Fatalf("err = %v, stderr = %q", err, errOut)
	}
	rows := portsJSON(t)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	want := []struct {
		port     int
		variable string
		worktree string
	}{{81, "APP_PORT", "app-feat"}, {82, "APP_PORT", "app-other"}, {3307, "FORWARD_DB_PORT", "app-feat"}}
	for i, w := range want {
		r := rows[i]
		if r.Port != w.port || r.Variable != w.variable || r.Worktree != w.worktree || r.Subdir != "." ||
			r.Host != hostFree || r.Conflict || r.Process != "" || r.PID != 0 || r.State != stateNone {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
	}
	for _, s := range []string{"PORT", "VARIABLE", "app-feat", "3307", "FORWARD_DB_PORT", "free"} {
		if !strings.Contains(out, s) {
			t.Errorf("table lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "90") || strings.Contains(out, "foreign") {
		t.Errorf("another repository is listed:\n%s", out)
	}
	if len(portsJSON(t, "--all")) != 4 {
		t.Error("--all did not add the other repository")
	}
	_ = wt
}

func TestPortsHostAndOwner(t *testing.T) {
	portsSetup(t)
	probes, lookups := portsFakes(t, map[int]bool{81: true, 3307: true}, map[int]owner{81: {"php", 123}})
	rows := portsJSON(t)
	byPort := map[int]portRow{}
	for _, r := range rows {
		byPort[r.Port] = r
	}
	if r := byPort[81]; r.Host != hostUnavailable || r.Process != "php" || r.PID != 123 {
		t.Errorf("81: %+v", r)
	}
	if r := byPort[3307]; r.Host != hostUnavailable || r.Process != "" || r.PID != 0 { // no owner found
		t.Errorf("3307: %+v", r)
	}
	if r := byPort[82]; r.Host != hostFree {
		t.Errorf("82: %+v", r)
	}
	if len(*probes) != 3 || len(*lookups) != 2 { // each port once; owners only for unavailable ports
		t.Errorf("probes = %v, lookups = %v", *probes, *lookups)
	}
	out, _, _ := runPorts(t)
	if !strings.Contains(out, "unavailable (php pid 123)") {
		t.Errorf("table:\n%s", out)
	}
}

func TestPortsRunningProjectsAreNotProbed(t *testing.T) {
	_, wt, other := portsSetup(t)
	main := filepath.Dir(wt)
	_ = main
	probes, _ := portsFakes(t, map[int]bool{81: true}, nil)
	mainTop, _ := mainWorktree(wt)
	useDocker(t, `[{"Name":"`+projectName(mainTop, wt, wt)+`","Status":"running(2)"}]`, nil)
	rows := portsJSON(t)
	for _, r := range rows {
		switch r.Worktree {
		case "app-feat":
			if r.Host != hostOwn || r.State != stateRunning {
				t.Errorf("running project: %+v", r)
			}
		default:
			if r.Host != hostFree || r.State != stateDown {
				t.Errorf("other project: %+v", r)
			}
		}
	}
	if len(*probes) != 1 || (*probes)[0] != 82 {
		t.Errorf("probed %v, want only 82", *probes)
	}
	_ = other
}

func TestPortsConflicts(t *testing.T) {
	_, wt, other := portsSetup(t)
	writeRegistry(t, map[string]map[string]int{
		wt:    {"APP_PORT": 81, "FORWARD_DB_PORT": 81}, // two variables of one entry
		other: {"APP_PORT": 81, "VITE_PORT": 5174},
	})
	portsFakes(t, nil, nil)
	conflicts := 0
	for _, r := range portsJSON(t) {
		if r.Conflict != (r.Port == 81) {
			t.Errorf("row %+v", r)
		}
		if r.Conflict {
			conflicts++
		}
	}
	if conflicts != 3 {
		t.Errorf("conflicting rows = %d", conflicts)
	}
	if out, _, _ := runPorts(t); strings.Count(out, "yes") != 3 {
		t.Errorf("table:\n%s", out)
	}
}

func TestPortsFilter(t *testing.T) {
	portsSetup(t)
	probes, _ := portsFakes(t, map[int]bool{4000: true}, map[int]owner{4000: {"node", 77}})
	rows := portsJSON(t, "3307")
	if len(rows) != 1 || rows[0].Variable != "FORWARD_DB_PORT" || rows[0].Worktree != "app-feat" || len(*probes) != 1 {
		t.Errorf("rows = %+v, probes = %v", rows, *probes)
	}
	// Nobody records 4000, but the host is still examined.
	rows = portsJSON(t, "4000")
	if len(rows) != 1 || rows[0].Port != 4000 || rows[0].Variable != "" || rows[0].Host != hostUnavailable || rows[0].Process != "node" {
		t.Errorf("unrecorded port: %+v", rows)
	}
	out, _, err := runPorts(t, "4000")
	if err != nil || !strings.Contains(out, "4000") || !strings.Contains(out, "unavailable (node pid 77)") {
		t.Errorf("table: %v\n%s", err, out)
	}
	// A port of another repository is not shown without --all, and is examined as unrecorded.
	if rows = portsJSON(t, "90"); len(rows) != 1 || rows[0].Variable != "" {
		t.Errorf("foreign port without --all: %+v", rows)
	}
	if rows = portsJSON(t, "90", "--all"); len(rows) != 1 || rows[0].Variable != "APP_PORT" {
		t.Errorf("foreign port with --all: %+v", rows)
	}
}

func TestPortsRejectsBadArguments(t *testing.T) {
	portsSetup(t)
	portsFakes(t, nil, nil)
	for _, args := range [][]string{{"0"}, {"65536"}, {"+80"}, {"-80"}, {"8 0"}, {"abc"}, {"1e3"}, {""}, {"80", "81"}, {"--bogus"}, {"99999999999999999999"}, {"\u0663"}} {
		if _, _, err := runPorts(t, args...); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	for _, ok := range []string{"1", "80", "65535", "0080"} {
		if _, _, err := runPorts(t, ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestPortsShowsStaleEntriesAndTheNote(t *testing.T) {
	main, wt, other := portsSetup(t)
	if err := os.RemoveAll(other); err != nil { // still listed by git (prunable)
		t.Fatal(err)
	}
	pruned := filepath.Join(filepath.Dir(wt), "app-pruned")
	runGit(t, main, "worktree", "add", "-q", pruned, "-b", "pruned")
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, other: {"APP_PORT": 82}, pruned: {"APP_PORT": 83}})
	runGit(t, main, "worktree", "remove", "--force", pruned)
	portsFakes(t, nil, nil)
	out, errOut, err := runPorts(t)
	if err != nil || !strings.Contains(out, stateStale) || !strings.Contains(errOut, "registry entries not listed: 1") {
		t.Errorf("err = %v\nstdout:\n%s\nstderr: %q", err, out, errOut)
	}
}

func TestPortsEmptyAndBrokenRegistry(t *testing.T) {
	setupWorktreeRepo(t)
	portsFakes(t, nil, nil)
	if out, _, err := runPorts(t); err != nil || !strings.Contains(out, "no registry entries") {
		t.Errorf("empty: %q, %v", out, err)
	}
	if out, _, err := runPorts(t, "--json"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Errorf("empty JSON: %q, %v", out, err)
	}
	writeFile(t, mustRegistryPath(t), "{not json")
	if _, _, err := runPorts(t); err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("broken registry: %v", err)
	}
}

func TestPortsEscapesTheProcessName(t *testing.T) {
	portsSetup(t)
	portsFakes(t, map[int]bool{81: true}, map[int]owner{81: {"evil\x1b[31m\u202e\x7f\U000e0001\nx", 5}})
	out, _, err := runPorts(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"\x1b", "\u202e", "\x7f"} {
		if strings.Contains(out, bad) {
			t.Errorf("table contains %q:\n%q", bad, out)
		}
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 4 { // header and three rows: the newline is written out
		t.Errorf("%d lines:\n%q", n, out)
	}
	js, _, _ := runPorts(t, "--json")
	if !json.Valid([]byte(js)) || strings.ContainsAny(js, "\x1b\u202e\x7f\U000e0001") {
		t.Errorf("JSON:\n%q", js)
	}
	var rows []portRow
	if err := json.Unmarshal([]byte(js), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		found = found || strings.Contains(r.Process, "\u202e")
	}
	if !found {
		t.Errorf("the name did not survive the round trip: %+v", rows)
	}
}

func TestPortsWritesNothing(t *testing.T) {
	_, wt, _ := portsSetup(t)
	writeFile(t, filepath.Join(wt, ".env"), "APP_KEY=base64:secret\n")
	portsFakes(t, nil, nil)
	read := func() string {
		b, _ := os.ReadFile(mustRegistryPath(t))
		fi, _ := os.Stat(mustRegistryPath(t))
		e, _ := os.ReadFile(filepath.Join(wt, ".env"))
		return string(b) + fi.ModTime().String() + string(e)
	}
	before := read()
	for _, args := range [][]string{nil, {"--all"}, {"--json"}, {"81"}} {
		if _, _, err := runPorts(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	if read() != before {
		t.Error("files changed")
	}
}

func TestPortsNoDockerAndDockerFailure(t *testing.T) {
	portsSetup(t)
	portsFakes(t, nil, nil)
	calls := useDocker(t, `[]`, nil)
	for _, r := range portsJSON(t, "--no-docker") {
		if r.State != stateNone {
			t.Errorf("--no-docker state = %q", r.State)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("--no-docker asked docker")
	}
	useDocker(t, "", errBoom)
	for _, r := range portsJSON(t) {
		if r.State != stateUnknown || r.Host != hostFree {
			t.Errorf("docker failure: %+v", r)
		}
	}
}

func TestPortsMixedStatesOnOnePort(t *testing.T) {
	_, wt, other := portsSetup(t)
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, other: {"APP_PORT": 81}})
	probes, _ := portsFakes(t, map[int]bool{81: true}, map[int]owner{81: {"docker-proxy", 9}})
	mainTop, _ := mainWorktree(wt)
	useDocker(t, `[{"Name":"`+projectName(mainTop, wt, wt)+`","Status":"running(1)"}]`, nil)
	byWorktree := map[string]portRow{}
	for _, r := range portsJSON(t) {
		byWorktree[r.Worktree] = r
	}
	// The running entry's row is its own port; the other row cannot tell, so it probes: both are flagged.
	if r := byWorktree["app-feat"]; r.Host != hostOwn || !r.Conflict {
		t.Errorf("running row: %+v", r)
	}
	if r := byWorktree["app-other"]; r.Host != hostUnavailable || r.Process != "docker-proxy" || !r.Conflict {
		t.Errorf("other row: %+v", r)
	}
	if len(*probes) != 1 {
		t.Errorf("probes = %v", *probes)
	}
}
