package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastLock shrinks the lock timings for one test and restores them afterwards.
func fastLock(t *testing.T, wait, stale time.Duration) {
	t.Helper()
	old := lockTiming
	lockTiming.poll, lockTiming.wait, lockTiming.stale, lockTiming.notice = 2*time.Millisecond, wait, stale, 10*time.Millisecond
	t.Cleanup(func() { lockTiming = old })
}

func lockFilePath(t *testing.T) string {
	t.Helper()
	return mustRegistryPath(t) + ".lock"
}

// plantLock creates a lock file that belongs to nobody in this test, as if another run held it. age is how long ago it
// was last modified.
func plantLock(t *testing.T, age time.Duration) string {
	t.Helper()
	p := lockFilePath(t)
	writeFile(t, p, "4242\n")
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLockAcquireAndRelease(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, time.Second, time.Hour)
	unlock, err := lockRegistry()
	if err != nil {
		t.Fatal(err)
	}
	p := lockFilePath(t)
	b, err := os.ReadFile(p)
	if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("lock mode = %v", fi.Mode().Perm())
		}
	}
	unlock()
	unlock() // idempotent
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("the lock file is still there: %v", err)
	}
}

func TestLockWaitsForTheHolder(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, 30*time.Second, time.Hour)
	first, err := lockRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		u, err := lockRegistry()
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		got <- u
	}()
	select {
	case <-got:
		t.Fatal("the second lock was taken while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	first()
	select {
	case u := <-got:
		u()
	case <-time.After(20 * time.Second):
		t.Fatal("the second lock was not taken after the release")
	}
}

func TestLockTimeoutNamesTheLockFileAndSaysItOnce(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, 80*time.Millisecond, time.Hour)
	p := plantLock(t, 0)
	errOut := captureStderr(t)
	_, err := lockRegistry()
	if err == nil {
		t.Fatal("a held lock was taken")
	}
	for _, want := range []string{"locked by another sail-worktree run", "pid 4242", strconv.Quote(p), "delete it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if n := strings.Count(errOut.String(), "waiting for another sail-worktree run"); n != 1 {
		t.Errorf("the waiting notice was printed %d times: %q", n, errOut.String())
	}
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("a live lock was removed: %v", err)
	}
}

func TestLockBreaksAnAbandonedLock(t *testing.T) {
	for name, age := range map[string]time.Duration{"old": 2 * time.Hour, "future": -2 * time.Hour} {
		t.Run(name, func(t *testing.T) {
			setupWorktreeRepo(t)
			fastLock(t, 5*time.Second, time.Minute)
			p := plantLock(t, age)
			unlock, err := lockRegistry()
			if err != nil {
				t.Fatalf("the abandoned lock was not broken: %v", err)
			}
			if b, _ := os.ReadFile(p); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
				t.Errorf("lock file = %q", b)
			}
			unlock()
			if names := dirNames(t, filepath.Dir(p)); len(names) != 0 {
				t.Errorf("config directory holds %v", names)
			}
		})
	}
}

func TestUnlockLeavesAForeignLock(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, time.Second, time.Hour)
	unlock, err := lockRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// Ours was broken as stale and somebody else took the lock meanwhile.
	p := lockFilePath(t)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, "999\n")
	unlock()
	if b, err := os.ReadFile(p); err != nil || strings.TrimSpace(string(b)) != "999" {
		t.Errorf("the foreign lock was removed: %q, %v", b, err)
	}
}

func TestLockSerialisesReadModifyWrite(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, 60*time.Second, time.Hour)
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := lockRegistry()
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			r, err := loadRegistry()
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(2 * time.Millisecond) // widen the window in which an unlocked update would be lost
			r.Worktrees["/wt"+strconv.Itoa(i)] = map[string]int{"APP_PORT": 81 + i}
			if err := r.save(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	r, err := loadRegistry()
	if err != nil || len(r.Worktrees) != n {
		t.Errorf("%d entries after %d updates: %v", len(r.Worktrees), n, err)
	}
	if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
		t.Errorf("the lock file is still there: %v", err)
	}
}

func upSetup(t *testing.T) (wt string) {
	t.Helper()
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(main, ".env"), "APP_URL=http://localhost\n")
	writeFakeSail(t, wt)
	captureStdout(t)
	captureStderr(t)
	return wt
}

func TestUpDoesNothingWhileTheRegistryIsLocked(t *testing.T) {
	wt := upSetup(t)
	fastLock(t, 50*time.Millisecond, time.Hour)
	p := plantLock(t, 0)
	calls := captureRunner(t)
	err := cmdUp(nil)
	if err == nil || !strings.Contains(err.Error(), "locked by another sail-worktree run") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env was written: %v", err)
	}
	if _, err := os.Lstat(mustRegistryPath(t)); !os.IsNotExist(err) {
		t.Errorf("the registry was written: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("sail ran: %v", *calls)
	}
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("the foreign lock was removed: %v", err)
	}
}

func TestUpReleasesTheLockBeforeSailRuns(t *testing.T) {
	upSetup(t)
	fastLock(t, time.Second, time.Hour)
	lockedDuringSail := true
	old := runner
	runner = func(string, []string, string, ...string) error {
		_, err := os.Lstat(lockFilePath(t))
		lockedDuringSail = err == nil
		return nil
	}
	t.Cleanup(func() { runner = old })
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	if lockedDuringSail {
		t.Error("the registry lock was held while sail ran")
	}
	if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
		t.Errorf("the lock file is still there: %v", err)
	}
}

func TestUpReleasesTheLockWhenItFails(t *testing.T) {
	cases := map[string]func(t *testing.T, wt string){
		"override key": func(t *testing.T, wt string) { writeFile(t, filepath.Join(wt, ".env"), "COMPOSE_FILE=/x.yaml\n") },
		"no sail":      func(t *testing.T, wt string) { os.RemoveAll(filepath.Join(wt, "vendor")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			wt := upSetup(t)
			fastLock(t, time.Second, time.Hour)
			mutate(t, wt)
			calls := captureRunner(t)
			if err := cmdUp(nil); err == nil {
				t.Fatal("up succeeded")
			}
			if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
				t.Errorf("the lock file is still there: %v", err)
			}
			if _, err := os.Lstat(mustRegistryPath(t)); !os.IsNotExist(err) {
				t.Errorf("the registry was saved: %v", err)
			}
			if len(*calls) != 0 {
				t.Errorf("sail ran")
			}
		})
	}
}

// rmSetup is a worktree that gets through every rm check, with a registry entry for it.
func rmSetup(t *testing.T) (wt string) {
	t.Helper()
	wt = rmWorktree(t, "compose.yaml")
	writeFile(t, filepath.Join(wt, "compose.yaml"), "services: {}\n")
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}})
	captureStdout(t)
	captureStderr(t)
	return wt
}

func TestRmLocksOnlyAfterDockerAndReleases(t *testing.T) {
	wt := rmSetup(t)
	fastLock(t, time.Second, time.Hour)
	lockedDuringDocker := true
	old := runner
	runner = func(string, []string, string, ...string) error {
		_, err := os.Lstat(lockFilePath(t))
		lockedDuringDocker = err == nil
		return nil
	}
	t.Cleanup(func() { runner = old })
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if lockedDuringDocker {
		t.Error("the registry lock was held while docker ran")
	}
	if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
		t.Errorf("the lock file is still there: %v", err)
	}
	if r, err := loadRegistry(); err != nil || len(r.Worktrees[wt]) != 0 {
		t.Errorf("the ports were not released: %v, %v", r, err)
	}
}

func TestRmWhenTheRegistryIsLockedAfterDocker(t *testing.T) {
	wt := rmSetup(t)
	fastLock(t, 50*time.Millisecond, time.Hour)
	dockerRuns := 0
	old := runner
	runner = func(string, []string, string, ...string) error {
		dockerRuns++
		plantLock(t, 0) // another run takes the lock while docker works
		return nil
	}
	t.Cleanup(func() { runner = old })
	err := cmdRm([]string{"-y"})
	if err == nil || !strings.Contains(err.Error(), "docker finished removing") ||
		!strings.Contains(err.Error(), "run rm again") || !strings.Contains(err.Error(), "locked by another") {
		t.Fatalf("err = %v", err)
	}
	if dockerRuns != 1 {
		t.Errorf("docker ran %d times", dockerRuns)
	}
	if r, err := loadRegistry(); err != nil || r.Worktrees[wt]["APP_PORT"] != 81 {
		t.Errorf("the registry changed: %v, %v", r, err)
	}
	if _, err := os.Lstat(lockFilePath(t)); err != nil {
		t.Errorf("the foreign lock was removed: %v", err)
	}
}

func TestReadOnlyCommandsIgnoreTheLock(t *testing.T) {
	statusSetup(t, "")
	portsFakes(t, nil, nil)
	fastLock(t, 20*time.Millisecond, time.Hour)
	p := plantLock(t, 0)
	if _, _, err := runPs(t); err != nil {
		t.Errorf("ps: %v", err)
	}
	if _, _, err := runPorts(t); err != nil {
		t.Errorf("ports: %v", err)
	}
	if _, _, err := runStatus(t); err != nil {
		t.Errorf("status: %v", err)
	}
	if b, err := os.ReadFile(p); err != nil || strings.TrimSpace(string(b)) != "4242" {
		t.Errorf("the lock file changed: %q, %v", b, err)
	}
}
