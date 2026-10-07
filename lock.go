package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// lockTiming holds the timings of the registry lock (tests shrink them). A lock is held for the few seconds up and rm
// need to read the registry, choose ports and save, never while sail or docker runs, so one older than stale is taken
// for abandoned (a crashed run); wait is longer than stale, so a waiter outlasts a crashed holder's lock. notice is
// when a waiter says that it waits.
var lockTiming = struct{ poll, wait, stale, notice time.Duration }{
	poll: 50 * time.Millisecond, wait: 75 * time.Second, stale: 60 * time.Second, notice: time.Second,
}

// lockRegistry takes the lock that serialises the commands that update the registry (up and rm; the read-only
// commands never lock). The lock is registry.json.lock next to the registry, created with O_EXCL. A caller that
// updates the registry must read it after locking, so that it sees the update of the run it waited for. unlock is
// idempotent and removes the lock file only if it is still the one this call created.
//
// An O_EXCL file was chosen over flock/LockFileEx: it needs no per-OS code and no dependency, at the price of the
// stale rule and of a hand recovery that the error message describes.
func lockRegistry() (unlock func(), err error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	lock := p + ".lock"
	start := time.Now()
	deadline := start.Add(lockTiming.wait)
	noticed := false
	for {
		f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			// Closed at once: on Windows an open file cannot be removed. The pid is for humans.
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			cerr := f.Close()
			fi, serr := os.Lstat(lock)
			if err := errors.Join(werr, cerr, serr); err != nil {
				os.Remove(lock)
				return nil, fmt.Errorf("cannot write the registry lock %q: %w", lock, err)
			}
			return releaser(lock, fi), nil
		}
		if errors.Is(err, os.ErrExist) && breakStale(lock) {
			continue
		}
		// Any other error (on Windows, a lock whose removal is still pending) is retried until the deadline too.
		if time.Now().After(deadline) {
			if errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("the registry is locked by another sail-worktree run (%s); lock file %q; "+
					"if none is running, delete it and retry (a lock older than %s is removed automatically)",
					lockHolder(lock), lock, lockTiming.stale)
			}
			return nil, fmt.Errorf("cannot create the registry lock %q: %w", lock, err)
		}
		if !noticed && time.Since(start) >= lockTiming.notice {
			noticed = true
			fmt.Fprintf(stderr, "waiting for another sail-worktree run to finish updating the registry (lock file %q)\n", lock)
		}
		time.Sleep(lockTiming.poll)
	}
}

// lockHolder describes the pid written in the lock file, for the error message only.
func lockHolder(lock string) string {
	if b, err := os.ReadFile(lock); err == nil {
		if pid := strings.TrimSpace(string(b)); pid != "" && len(pid) <= 20 {
			return "pid " + pid
		}
	}
	return "pid unknown"
}

// breakStale removes an abandoned lock and reports whether the caller should try to take the lock again at once.
// A lock is abandoned when its modification time is older than lockTiming.stale or (clock skew, a restored backup)
// well in the future. The file is claimed by renaming it to a unique name and checked to be the one that was judged,
// so a waiter that was slow cannot remove the fresh lock another run created meanwhile (it puts it back); two waiters
// that break the same lock still race on the next O_EXCL, and only one wins.
func breakStale(lock string) bool {
	fi, err := os.Lstat(lock)
	if err != nil {
		return os.IsNotExist(err) // gone already: try at once
	}
	if age := time.Since(fi.ModTime()); age <= lockTiming.stale && age >= -5*time.Second {
		return false
	}
	claimed := fmt.Sprintf("%s.stale-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(lock, claimed); err != nil {
		return os.IsNotExist(err)
	}
	if fi2, err := os.Lstat(claimed); err != nil || !os.SameFile(fi, fi2) {
		os.Rename(claimed, lock) // not the file we judged: a fresh lock, give it back
		return false
	}
	os.Remove(claimed)
	return true
}

// releaser returns the idempotent unlock for the lock file whose Lstat is ours.
func releaser(lock string, ours os.FileInfo) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if fi, err := os.Lstat(lock); err == nil && os.SameFile(ours, fi) {
				os.Remove(lock)
			}
		})
	}
}
