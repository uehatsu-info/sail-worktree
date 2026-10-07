package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
			// The identity is taken from the open file, and the file is closed at once: on Windows an open file
			// cannot be removed. The pid is for humans.
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			fi, serr := f.Stat()
			cerr := f.Close()
			if err := errors.Join(werr, serr, cerr); err != nil {
				os.Remove(lock)
				return nil, fmt.Errorf("cannot write the registry lock %q: %w", lock, err)
			}
			return releaser(lock, fi), nil
		}
		// On Windows a lock whose removal is still pending, or a directory in its place, is "access denied" and not
		// "exists", so there every error is retried (and an old leftover broken) until the deadline. Elsewhere any
		// other error (no permission, no space) cannot go away by waiting.
		retry := errors.Is(err, os.ErrExist) || runtime.GOOS == "windows"
		if retry && breakStale(lock) && time.Now().Before(deadline) {
			continue
		}
		if !retry {
			return nil, fmt.Errorf("cannot create the registry lock %q: %w", lock, err)
		}
		if time.Now().After(deadline) {
			if errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("the registry is locked by another sail-worktree run (%s); lock file %q; "+
					"if none is running, delete it and retry (a lock older than %d seconds is removed automatically)",
					lockHolder(lock), lock, int(lockTiming.stale.Seconds()))
			}
			return nil, fmt.Errorf("cannot create the registry lock %q: %w", lock, err)
		}
		if !noticed && time.Since(start) >= lockTiming.notice {
			noticed = true
			fmt.Fprintf(stderr, "waiting for another sail-worktree run to finish updating the registry "+
				"(lock file %q; a lock older than %d seconds is taken for abandoned)\n", lock, int(lockTiming.stale.Seconds()))
		}
		time.Sleep(lockTiming.poll)
	}
}

// lockHolder describes the pid written in the lock file, for the error message only. The path may hold anything
// (a link, a FIFO), so it is read with readSmallFile and only a number is shown.
func lockHolder(lock string) string {
	if b, err := readSmallFile(lock); err == nil {
		if pid := strings.TrimSpace(string(b)); len(pid) <= 20 {
			if _, err := strconv.ParseUint(pid, 10, 64); err == nil {
				return "pid " + pid
			}
		}
	}
	return "pid unknown"
}

// breakStale removes an abandoned lock and reports whether the caller should try to take the lock again at once.
// A lock is abandoned when its modification time is older than lockTiming.stale or (clock skew, a restored backup)
// well in the future.
func breakStale(lock string) bool {
	fi, err := lstatIdentity(lock)
	if err != nil {
		return os.IsNotExist(err) // gone already: try at once
	}
	if age := time.Since(fi.ModTime()); age <= lockTiming.stale && age >= -5*time.Second {
		return false
	}
	return claimStale(lock, fi)
}

// lstatIdentity is os.Lstat with the file identity already read: on Windows os.SameFile reads it lazily, from the path,
// so it would find nothing once the file has been renamed away. SameFile(fi, fi) forces the read.
func lstatIdentity(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err == nil {
		os.SameFile(fi, fi)
	}
	return fi, err
}

// claimStale removes the lock that was judged stale (judged is its Lstat). The file is first renamed to a unique name
// and checked to be the judged one (same file, same modification time: an inode number can be reused), so a waiter
// that was slow cannot delete the fresh lock another run created meanwhile; that one is given back with a link, which
// fails instead of replacing a lock a third run took in between. Two waiters that break the same lock race on the
// next O_EXCL, and only one wins.
func claimStale(lock string, judged os.FileInfo) bool {
	claimed := fmt.Sprintf("%s.stale-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(lock, claimed); err != nil {
		return os.IsNotExist(err)
	}
	fi, err := lstatIdentity(claimed)
	if err != nil || !os.SameFile(judged, fi) || !fi.ModTime().Equal(judged.ModTime()) {
		os.Link(claimed, lock)
		os.Remove(claimed)
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
			if fi, err := os.Lstat(lock); err == nil && os.SameFile(ours, fi) && fi.ModTime().Equal(ours.ModTime()) {
				// On Windows the removal fails while another run briefly has the file open (to read the pid).
				err := os.Remove(lock)
				for i := 0; err != nil && !os.IsNotExist(err) && runtime.GOOS == "windows" && i < 20; i++ {
					time.Sleep(10 * time.Millisecond)
					err = os.Remove(lock)
				}
			}
		})
	}
}
