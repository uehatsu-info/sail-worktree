package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// A branch whose configuration lacks a variable keeps it in the main's record; only the main's own configuration drops
// it.
func TestCheckKeepsAMainVariableUnknownToTheBranch(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	rec := MainRecord{Ports: map[string]int{"APP_PORT": 80, "FORWARD_REDIS_PORT": 6380},
		Defaults: map[string]int{"APP_PORT": 80, "FORWARD_REDIS_PORT": 6379}}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{main: rec}})
	// The kept value is not re-read in a linked worktree (a documented limit), so 6390 is not seen yet.
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8080\nFORWARD_REDIS_PORT=6390\n")
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	got := reg.Mains[main]
	if got.Ports["APP_PORT"] != 8080 || got.Ports["FORWARD_REDIS_PORT"] != 6380 || got.Defaults["FORWARD_REDIS_PORT"] != 6379 {
		t.Errorf("check in %q: record = %+v", wt, got)
	}
	t.Chdir(main)
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ = loadRegistry()
	got = reg.Mains[main]
	if _, ok := got.Defaults["FORWARD_REDIS_PORT"]; ok || len(got.Ports) != 1 || got.Ports["APP_PORT"] != 8080 {
		t.Errorf("check in the main worktree: record = %+v", got)
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

func TestCheckDropsInvalidRecordedDefaults(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t) // no .env: the defaults decide
	both := func() map[string]int { return map[string]int{"FORWARD_DB_PORT": 3306, "X_PORT": 70000} }
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: {Ports: both(), Defaults: both()}}})
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rec := reg.Mains[m]
	if len(rec.Ports) != 1 || rec.Ports["FORWARD_DB_PORT"] != 3306 || reg.used("")[70000] {
		t.Errorf("ports after check = %v, 70000 used = %v", rec.Ports, reg.used("")[70000])
	}
	// The recorded defaults themselves are left as they are; only their use is checked.
	if rec.Defaults["X_PORT"] != 70000 {
		t.Errorf("defaults after check = %v", rec.Defaults)
	}
}

func TestCheckReportsWithoutTheLock(t *testing.T) {
	isolatedRegistry(t)
	fastLock(t, 50*time.Millisecond, time.Hour)
	captureStderr(t)
	a, b := realTempDir(t), realTempDir(t)
	// Refreshing would move b to 8090; without the lock the record (80) is reported as it is.
	writeFile(t, filepath.Join(b, ".env"), "APP_PORT=8090\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80), b: mainRec(80)}})
	p := mustRegistryPath(t)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lock := plantLock(t, 0)
	out, err := runCheck(t)
	if err == nil || !strings.Contains(err.Error(), "found 1 problem") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{
		"registry: 2 main worktree record(s)",
		"port 80:",
		"report may be based on records that refreshing would have changed",
		"the registry is locked by another sail-worktree run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("the registry was written without the lock")
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Errorf("the other run's lock is gone: %v", err)
	}

	// A registry broken while check waits for the lock is still an error.
	betweenReadAndLock(t, func() { writeFile(t, p, "{") })
	if out, err := runCheck(t); err == nil || strings.Contains(err.Error(), "problem") {
		t.Errorf("a broken registry is accepted without the lock: %v\n%s", err, out)
	}
}

// check prints its warnings alone when it fails (#75); without any there is no empty heading.
func TestReportPrintWarningsWithoutWarnings(t *testing.T) {
	var b strings.Builder
	(&report{w: &b}).printWarnings()
	if b.String() != "" {
		t.Errorf("an empty list printed %q", b.String())
	}
}

// Only a lock that another run holds is reported around; a lock that cannot be created at all (no permission, a
// read-only file system) would keep every run from recording, so check fails instead of passing with a warning (#63).
func TestCheckFailsWhenTheLockCannotBeCreated(t *testing.T) {
	isolatedRegistry(t)
	captureStderr(t)
	a := realTempDir(t)
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80)}})
	p := mustRegistryPath(t)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Through the real lockRegistry, so that the error it returns for a create that fails is checked too.
	oldOpen, oldWin := openLockFile, lockOnWindows
	t.Cleanup(func() { openLockFile, lockOnWindows = oldOpen, oldWin })
	openLockFile = func(string) (*os.File, error) { return nil, os.ErrPermission }
	lockOnWindows = false // a Windows run would retry it until the wait ends
	out, err := runCheck(t)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want the lock error\n%s", err, out)
	}
	if strings.Contains(out, "registry:") || strings.Contains(out, "nothing was recorded") {
		t.Errorf("check reported as if the lock were only busy:\n%s", out)
	}
	// The cwd is outside a repository, so a warning was collected before the lock failed; it is printed (#75).
	if !strings.Contains(out, "warnings:\n  - no main worktree was recorded") {
		t.Errorf("the collected warnings are not printed:\n%s", out)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Error("the registry was written without the lock")
	}
}

// betweenReadAndLock runs fn after check has read the mains' .env and before it takes the lock.
func betweenReadAndLock(t *testing.T, fn func()) {
	t.Helper()
	old := checkLock
	checkLock = func() (func(), error) {
		fn()
		return lockRegistry()
	}
	t.Cleanup(func() { checkLock = old })
}

// betweenUpReadAndLock is betweenReadAndLock for up: fn runs after up has read the main's .env, found alias keys and
// read the source of a new .env, and before it locks.
func betweenUpReadAndLock(t *testing.T, fn func()) {
	t.Helper()
	old := upLock
	upLock = func() (func(), error) {
		fn()
		return lockRegistry()
	}
	t.Cleanup(func() { upLock = old })
}

func TestCheckReadsMainEnvBeforeTheLock(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t)
	writeFile(t, filepath.Join(m, ".env"), "APP_PORT=8090\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(80)}})
	// A change after the read is not seen by this run: the .env is not read again under the lock.
	betweenReadAndLock(t, func() { writeFile(t, filepath.Join(m, ".env"), "APP_PORT=8091\n") })
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if got := reg.Mains[m].Ports["APP_PORT"]; got != 8090 {
		t.Errorf("APP_PORT = %d, want 8090 (the read before the lock)", got)
	}
}

func TestCheckDoesNotRefreshARecordChangedBeforeTheLock(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t)
	writeFile(t, filepath.Join(m, ".env"), "APP_PORT=8090\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(80)}})
	// Another run records 9000 after check read the .env (8090): that record is newer and must stay.
	betweenReadAndLock(t, func() { saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(9000)}}) })
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if got := reg.Mains[m].Ports["APP_PORT"]; got != 9000 {
		t.Errorf("APP_PORT = %d, want 9000 (the record changed meanwhile)", got)
	}
}

// The current main follows the same rule as the others (#70): an up that records a newer .env while check waits for the
// lock keeps its record, and check still adds the worktree it runs in.
func TestCheckKeepsTheCurrentMainRecordChangedBeforeTheLock(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8090\n")
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}},
		Mains: map[string]MainRecord{main: mainRec(80)}})
	betweenReadAndLock(t, func() {
		writeFile(t, filepath.Join(main, ".env"), "APP_PORT=9000\n")
		saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}},
			Mains: map[string]MainRecord{main: mainRec(9000)}})
	})
	if out, err := runCheck(t); err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if rec := reg.Mains[main]; rec.Ports["APP_PORT"] != 9000 || rec.Defaults["APP_PORT"] != 80 ||
		len(rec.Worktrees) != 1 || rec.Worktrees[0] != wt {
		t.Errorf("record = %+v, want APP_PORT 9000 (the record changed meanwhile) and the worktree listed", rec)
	}
}

// A record that appeared while check waited is newer as well; one that is still the base, or that is gone (pruned
// meanwhile), takes the read, which is then all there is to know.
func TestCheckRecordsTheCurrentMainUnlessARecordAppeared(t *testing.T) {
	for _, tc := range []struct {
		name         string
		before, wait *MainRecord // nil: no record
		want         int
	}{
		{"unchanged", &MainRecord{Ports: map[string]int{"APP_PORT": 80}, Defaults: map[string]int{"APP_PORT": 80}}, nil, 8090},
		{"appeared", nil, &MainRecord{Ports: map[string]int{"APP_PORT": 9000}, Defaults: map[string]int{"APP_PORT": 80}}, 9000},
		{"gone", &MainRecord{Ports: map[string]int{"APP_PORT": 80}, Defaults: map[string]int{"APP_PORT": 80}}, &MainRecord{}, 8090},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, _ := setupWorktreeRepo(t)
			t.Chdir(main)
			writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8090\n")
			reg := &Registry{Mains: map[string]MainRecord{}}
			if tc.before != nil {
				reg.Mains[main] = *tc.before
			}
			saveRegistry(t, reg)
			if tc.wait != nil {
				betweenReadAndLock(t, func() {
					reg := &Registry{Mains: map[string]MainRecord{}}
					if tc.wait.Ports != nil {
						reg.Mains[main] = *tc.wait
					}
					saveRegistry(t, reg)
				})
			}
			if out, err := runCheck(t); err != nil {
				t.Fatalf("err = %v\n%s", err, out)
			}
			got, _ := loadRegistry()
			if p := got.Mains[main].Ports["APP_PORT"]; p != tc.want {
				t.Errorf("APP_PORT = %d, want %d", p, tc.want)
			}
		})
	}
}

func TestCheckFindsTheMainOfAWorktreeListedUnderAnAlias(t *testing.T) {
	real, links := newLinkedDir(t, "alias")
	main, wt2 := realTempDir(t), realTempDir(t)
	unclean := wt2 + string(filepath.Separator) + "." // fails validKey: must not be resolved to wt2
	reg := &Registry{
		Worktrees: map[string]map[string]int{real: {"APP_PORT": 8123}, wt2: {"APP_PORT": 8123}},
		Mains: map[string]MainRecord{main: {Ports: map[string]int{"APP_PORT": 8123}, Defaults: map[string]int{},
			Worktrees: []string{links[0], unclean}}},
	}
	b := captureStdout(t)
	r := &report{w: stdout}
	checkConflicts(r, reg)
	out := b.String()
	if want := fmt.Sprintf("worktree %q APP_PORT (of %q)", real, main); !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if want := fmt.Sprintf("worktree %q APP_PORT", wt2); !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if bad := fmt.Sprintf("worktree %q APP_PORT (of", wt2); strings.Contains(out, bad) {
		t.Errorf("an invalid list entry was resolved:\n%s", out)
	}
	// The alias entry is folded, so it is not warned about (prune keeps it, #99); the invalid entry is.
	warnings := strings.Join(r.warnings, "\n")
	if bad := fmt.Sprintf("lists linked worktree %q", links[0]); strings.Contains(warnings, bad) {
		t.Errorf("the alias entry was warned about:\n%s", warnings)
	}
	if want := fmt.Sprintf("lists linked worktree %q", unclean); !strings.Contains(warnings, want) {
		t.Errorf("missing %q in:\n%s", want, warnings)
	}
	// The fold is a view: the record itself is not changed.
	if got := reg.Mains[main].Worktrees; len(got) != 2 || got[0] != links[0] || got[1] != unclean {
		t.Errorf("the record was changed: %q", got)
	}
}

// A worktree listed by a replaced main record and by the record that replaced it is named after the live one, whatever
// the key order: the replaced key (base/work/app) sorts after the live one (base/data/app) (#99).
func TestCheckNamesTheLiveMainOfAWorktree(t *testing.T) {
	stale, live := aliasMain(t)
	wt := realTempDir(t)
	staleRec, liveRec := mainRec(80), mainRec(81)
	staleRec.Worktrees, liveRec.Worktrees = []string{wt}, []string{wt}
	reg := &Registry{
		Worktrees: map[string]map[string]int{wt: {"APP_PORT": 81}},
		Mains:     map[string]MainRecord{stale: staleRec, live: liveRec},
	}
	b := captureStdout(t)
	checkConflicts(&report{w: stdout}, reg)
	out := b.String()
	if want := fmt.Sprintf("worktree %q APP_PORT (of %q)", wt, live); !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
}

func TestCheckSuggestionSkipsPortsOfAliasKeys(t *testing.T) {
	isolatedRegistry(t)
	real, links := newLinkedDir(t, "alias")
	a, b := realTempDir(t), realTempDir(t)
	// The folded view keeps only the real key's 81, but up still treats the alias key's 82 as taken.
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{real: {"APP_PORT": 81}, links[0]: {"APP_PORT": 82}},
		Mains:     map[string]MainRecord{a: mainRec(80), b: mainRec(80)},
	})
	out, err := runCheck(t)
	if err == nil {
		t.Fatalf("no error\n%s", out)
	}
	// Holders are sorted by directory and the first one keeps the port, so the fix goes to the larger one.
	if want := fmt.Sprintf("set APP_PORT=83 in the .env of %q", max(a, b)); !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
}

// dupWarning is the warning check gives for a port variable set on more than one line of a main's .env.
func dupWarning(dir, name string) string {
	return fmt.Sprintf("the .env of main worktree %q sets %s on more than one line", dir, name)
}

func TestCheckWarnsAboutADuplicatedPortKey(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t)
	writeFile(t, filepath.Join(m, ".env"), "APP_PORT=8091\nAPP_NAME=x\nexport APP_PORT=8092\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(80)}})
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("a duplicated key changed the exit status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warnings:") || !strings.Contains(out, dupWarning(m, "APP_PORT")) {
		t.Errorf("no warning about APP_PORT in:\n%s", out)
	}
	// The directory may contain any digits, so it is removed before looking for the values.
	if rest := strings.ReplaceAll(out, strconv.Quote(m), ""); strings.Contains(rest, "8091") || strings.Contains(rest, "8092") {
		t.Errorf("a value of the duplicated key was printed:\n%s", out)
	}
}

func TestCheckDoesNotWarnAboutOtherKeysOrOneLine(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t)
	writeFile(t, filepath.Join(m, ".env"), "APP_NAME=a\nAPP_PORT=8090\n# APP_PORT=8091\nAPP_NAME=b\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(80)}})
	out, err := runCheck(t)
	if err != nil || strings.Contains(out, "more than one line") {
		t.Errorf("err = %v, unexpected warning in:\n%s", err, out)
	}
}

func TestCheckDoesNotReadASymlinkedMainEnvForDuplicates(t *testing.T) {
	isolatedRegistry(t)
	a, b := realTempDir(t), realTempDir(t)
	writeFile(t, filepath.Join(a, ".env"), "APP_PORT=8090\nAPP_PORT=8091\n")
	target := filepath.Join(t.TempDir(), "env")
	writeFile(t, target, "APP_PORT=8092\nAPP_PORT=8093\n")
	symlinkOrSkip(t, target, filepath.Join(b, ".env"))
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{a: mainRec(80), b: mainRec(81)}})
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// a is the control: the same setup with a regular file warns.
	if !strings.Contains(out, dupWarning(a, "APP_PORT")) {
		t.Errorf("no warning for the regular .env in:\n%s", out)
	}
	if strings.Contains(out, dupWarning(b, "APP_PORT")) {
		t.Errorf("the .env was read through a link:\n%s", out)
	}
}

func TestCheckWarnsAboutDuplicatesWithoutTheLock(t *testing.T) {
	isolatedRegistry(t)
	fastLock(t, 50*time.Millisecond, time.Hour)
	captureStderr(t) // the lock wait message
	m := realTempDir(t)
	writeFile(t, filepath.Join(m, ".env"), "APP_PORT=8090\nAPP_PORT=8091\n")
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: mainRec(80)}})
	plantLock(t, 0)
	out, err := runCheck(t)
	if err != nil || !strings.Contains(out, dupWarning(m, "APP_PORT")) {
		t.Errorf("err = %v, no warning in:\n%s", err, out)
	}
}

func TestCheckEscapesADuplicatedPortName(t *testing.T) {
	isolatedRegistry(t)
	m := realTempDir(t)
	evil := "X\x1b[2J"
	writeFile(t, filepath.Join(m, ".env"), evil+"=1\n"+evil+"=2\n")
	rec := MainRecord{Ports: map[string]int{evil: 80}, Defaults: map[string]int{evil: 80}}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{m: rec}})
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") || !strings.Contains(out, dupWarning(m, cell(evil))) {
		t.Errorf("the name is not escaped or not named:\n%q", out)
	}
}

// check records the main worktree of the project it runs in, found as up finds it: at the repository root of a
// project in a subdirectory it records nothing and says where to run it (#76).
func TestCheckAtTheRootOfASubdirectoryProjectSaysWhereToRun(t *testing.T) {
	main, _ := setupSubdirWorktreeRepo(t, false)
	t.Chdir(main)
	captureStderr(t)
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{
		"  - no main worktree was recorded: no Laravel project found",
		"\n    a project found in: \"laravel\"",
		"\n    check records the main worktree of the project it runs in: run it in the project directory or below it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if reg, _ := loadRegistry(); len(reg.Mains) != 0 {
		t.Errorf("mains = %+v", reg.Mains)
	}
}

func TestCheckInABareLayoutRecordsNoMain(t *testing.T) {
	bare, _, _ := setupBareWorktreeRepo(t)
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "is bare, so there is no main worktree checkout") || strings.Contains(out, "recorded main worktree") {
		t.Errorf("output:\n%s", out)
	}
	reg, _ := loadRegistry()
	if _, ok := reg.Mains[bare]; ok || len(reg.Mains) != 0 {
		t.Errorf("mains = %v", reg.Mains)
	}
}
