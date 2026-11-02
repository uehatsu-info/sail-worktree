package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCheck runs check with the port probe faked (every port is free unless listed in busy).
func runCheck(t *testing.T, busy ...int) (out string, err error) {
	t.Helper()
	old := probePort
	probePort = func(p int) bool {
		for _, b := range busy {
			if p == b {
				return false
			}
		}
		return true
	}
	t.Cleanup(func() { probePort = old })
	b := captureStdout(t)
	err = cmdCheck(nil)
	return b.String(), err
}

// isolatedRegistry points the registry at a temporary directory and leaves any repository.
func isolatedRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Chdir(realTempDir(t))
}

func saveRegistry(t *testing.T, reg *Registry) {
	t.Helper()
	if reg.Worktrees == nil {
		reg.Worktrees = map[string]map[string]int{}
	}
	if err := reg.save(); err != nil {
		t.Fatal(err)
	}
}

func mainRec(port int) MainRecord {
	return MainRecord{Ports: map[string]int{"APP_PORT": port}, Defaults: map[string]int{"APP_PORT": 80}}
}

func TestCheckRecordsAMainOnlyRepository(t *testing.T) {
	main, _ := setupWorktreeRepo(t)
	t.Chdir(main)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8080\n")
	before, _ := os.ReadFile(filepath.Join(main, ".env"))
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if rec := reg.Mains[main]; rec.Ports["APP_PORT"] != 8080 || rec.Defaults["APP_PORT"] != 80 || len(rec.Worktrees) != 0 {
		t.Errorf("record = %+v", rec)
	}
	if !strings.Contains(out, "recorded main worktree: "+fmt.Sprintf("%q", main)) || !strings.Contains(out, "no port is used more than once") {
		t.Errorf("output:\n%s", out)
	}
	if after, _ := os.ReadFile(filepath.Join(main, ".env")); string(after) != string(before) {
		t.Error("check changed the main worktree's .env")
	}
	// Nothing changed: the registry file is not rewritten.
	p, _ := registryPath()
	fi1, _ := os.Stat(p)
	if err := os.Chtimes(p, fi1.ModTime().Add(-time.Hour), fi1.ModTime().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fi1, _ = os.Stat(p)
	if _, err := runCheck(t); err != nil {
		t.Fatal(err)
	}
	if fi2, _ := os.Stat(p); !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Error("the registry was saved although nothing changed")
	}
}

func TestCheckInALinkedWorktreeListsIt(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}}})
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if rec := reg.Mains[main]; rec.Ports["APP_PORT"] != 80 || len(rec.Worktrees) != 1 || rec.Worktrees[0] != wt {
		t.Errorf("record = %+v", rec)
	}
}

func TestCheckOutsideARepositoryStillReports(t *testing.T) {
	isolatedRegistry(t)
	a, b := realTempDir(t), realTempDir(t)
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80), b: mainRec(80)}})
	out, err := runCheck(t)
	if err == nil {
		t.Fatalf("a conflict does not fail\n%s", out)
	}
	for _, want := range []string{
		"no main worktree was recorded",
		fmt.Sprintf("port 80: main worktree %q APP_PORT, main worktree %q APP_PORT", a, b),
		fmt.Sprintf("keeps it: main worktree %q APP_PORT", a),
		fmt.Sprintf("fix: set APP_PORT=81 in the .env of %q", b),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	reg, _ := loadRegistry()
	if len(reg.Mains) != 2 {
		t.Errorf("mains = %+v", reg.Mains)
	}

	// Outside a repository the recorded mains are still refreshed and saved.
	writeFile(t, filepath.Join(b, ".env"), "APP_PORT=8090\n")
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ = loadRegistry()
	if got := reg.Mains[b].Ports["APP_PORT"]; got != 8090 {
		t.Errorf("not refreshed outside a repository: %+v", reg.Mains[b])
	}
}

func TestCheckDoesNotUseALinkedKeyAsADirectory(t *testing.T) {
	isolatedRegistry(t)
	a := realTempDir(t)
	real := realTempDir(t)
	writeFile(t, filepath.Join(real, ".env"), "APP_PORT=9000\n")
	link := filepath.Join(realTempDir(t), "link")
	symlinkOrSkip(t, real, link)
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80), link: mainRec(80)}})
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("a linked key took part in a conflict: %v\n%s", err, out)
	}
	if want := fmt.Sprintf("the record of main worktree %q keeps ports free, but it is not a usable local directory", link); !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	reg, _ := loadRegistry()
	if got := reg.Mains[link].Ports["APP_PORT"]; got != 80 {
		t.Errorf("the .env behind a linked key was read: %+v", reg.Mains[link])
	}
}

func TestCheckSuggestionSkipsUsedPorts(t *testing.T) {
	isolatedRegistry(t)
	a, b, c := realTempDir(t), realTempDir(t), realTempDir(t)
	wt := realTempDir(t)
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}},
		Mains:     map[string]MainRecord{a: mainRec(80), b: mainRec(80), c: mainRec(80)},
	})
	out, err := runCheck(t, 82)
	if err == nil {
		t.Fatalf("no error\n%s", out)
	}
	// 81 is a worktree's, 82 cannot be bound, then each suggestion takes the next free one.
	for _, want := range []string{
		fmt.Sprintf("set APP_PORT=83 in the .env of %q", b),
		fmt.Sprintf("set APP_PORT=84 in the .env of %q", c),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckTellsAWorktreeToRunUp(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8123\n")
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8123}}})
	out, err := runCheck(t)
	if err == nil || !strings.Contains(err.Error(), "found 1 problem") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{
		fmt.Sprintf("keeps it: main worktree %q APP_PORT", main),
		fmt.Sprintf("worktree %q APP_PORT (of %q)", wt, main),
		fmt.Sprintf("fix: run `sail-worktree up` in %q", wt),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckIgnoresMissingDirectoriesAndRemovedKeys(t *testing.T) {
	isolatedRegistry(t)
	gone := filepath.Join(realTempDir(t), "gone")
	a := realTempDir(t)
	// a's .env no longer sets APP_PORT (the record still says 3307): the default is used, so no conflict with b.
	writeFile(t, filepath.Join(a, ".env"), "APP_NAME=x\n")
	b := realTempDir(t)
	writeFile(t, filepath.Join(b, ".env"), "APP_PORT=3307\n")
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{filepath.Join(gone, "wt"): {"APP_PORT": 80}},
		Mains: map[string]MainRecord{
			gone:           mainRec(80),
			"relative/key": mainRec(80),
			a:              mainRec(3307),
			b:              mainRec(80),
		},
	})
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{
		fmt.Sprintf("the record of main worktree %q keeps ports free, but it is not a usable local directory: APP_PORT=80", gone),
		fmt.Sprintf("the record of main worktree %q keeps ports free", "relative/key"),
		fmt.Sprintf("the record of worktree %q keeps ports free", filepath.Join(gone, "wt")),
		"no port is used more than once",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCheckDoesNotReadASymlinkedMainEnv(t *testing.T) {
	isolatedRegistry(t)
	a, b := realTempDir(t), realTempDir(t)
	target := filepath.Join(t.TempDir(), "env")
	writeFile(t, target, "APP_PORT=9000\n")
	symlinkOrSkip(t, target, filepath.Join(b, ".env"))
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80), b: mainRec(80)}})
	// Through the link b would use 9000; the record (80) must be used instead, so there is a conflict.
	out, err := runCheck(t)
	if err == nil || !strings.Contains(out, "port 80:") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestCheckEscapesUntrustedNames(t *testing.T) {
	isolatedRegistry(t)
	a, b := realTempDir(t), realTempDir(t)
	evil := "X\x1b[2J;rm -rf ~\nY"
	rec := MainRecord{Ports: map[string]int{evil: 80}, Defaults: map[string]int{evil: 80}}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: rec, b: rec, "/gone\x1b[31m": rec}})
	out, err := runCheck(t)
	if err == nil {
		t.Fatalf("no conflict\n%s", out)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "rm -rf ~\n") {
		t.Errorf("control characters reached the output:\n%q", out)
	}
	if want := fmt.Sprintf("set %+q to 81 in the .env of %q", evil, b); !strings.Contains(out, want) {
		t.Errorf("an unsafe name is not only quoted; missing %q in:\n%s", want, out)
	}
}

func TestCheckRejectsArguments(t *testing.T) {
	if err := cmdCheck([]string{"--all"}); err == nil {
		t.Error("an unknown argument is accepted")
	}
}

func TestCheckRefreshesOtherMainsSoUpAvoidsTheirNewPorts(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	// Another repository's main changed its .env to 8081 after it was recorded (as 80); this worktree holds 8081.
	other := realTempDir(t)
	writeFile(t, filepath.Join(other, ".env"), "APP_PORT=8081\n")
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{wt: {"APP_PORT": 8081}},
		Mains:     map[string]MainRecord{other: mainRec(80)},
	})
	out, err := runCheck(t)
	if err == nil || !strings.Contains(out, fmt.Sprintf("fix: run `sail-worktree up` in %q", wt)) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// The suggestion works: up now sees 8081 as used.
	reg, _ := loadRegistry()
	if got := reg.Mains[other].Ports["APP_PORT"]; got != 8081 || !reg.used(wt)[8081] {
		t.Errorf("the other main's record was not refreshed: %+v", reg.Mains[other])
	}
}

func TestCheckReportsTwoVariablesOfOneMain(t *testing.T) {
	isolatedRegistry(t)
	a := realTempDir(t)
	// No .env: both variables fall back to the same default.
	both := map[string]int{"APP_PORT": 8080, "VITE_PORT": 8080}
	rec := MainRecord{Ports: both, Defaults: both}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: rec}})
	out, err := runCheck(t)
	if err == nil {
		t.Fatalf("no conflict\n%s", out)
	}
	for _, want := range []string{
		fmt.Sprintf("keeps it: main worktree %q APP_PORT", a),
		fmt.Sprintf("fix: set VITE_PORT=8081 in the .env of %q", a),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
