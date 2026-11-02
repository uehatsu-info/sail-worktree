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

// lockTiming holds the timings of the registry lock (tests shrink them). A lock is held for the few seconds up, rm,
// check and prune need to read, update and save the registry, never while sail or docker runs, so one older than stale
// is taken for abandoned (a crashed run); wait is longer than stale, so a waiter outlasts a crashed holder's lock.
// notice is when a waiter says that it waits.
var lockTiming = struct{ poll, wait, stale, notice time.Duration }{
	poll: 50 * time.Millisecond, wait: 75 * time.Second, stale: 60 * time.Second, notice: time.Second,
}

// Seams for the tests: the create of the lock file, the Windows retry rule, and a hook in claimStale between taking
// the claim and checking the lock.
var (
	openLockFile = func(lock string) (*os.File, error) {
		return os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	lockOnWindows = runtime.GOOS == "windows"
	lockStep      = func() {}
)

// lockRegistry takes the lock that serialises the commands that update the registry (up, rm, check and prune; the
// read-only commands never lock). The lock is registry.json.lock next to the registry, created with O_EXCL. A caller
// that updates the registry must read it after locking, so that it sees the update of the run it waited for. unlock is
// idempotent and removes the lock file only if it is still the one this call created (see releaser).
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
		f, err := openLockFile(lock)
		if err == nil {
			// The identity is taken from the open file, and the file is closed at once: on Windows an open file
			// cannot be removed. The pid is for humans.
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			fi, serr := f.Stat()
			cerr := f.Close()
			if err := errors.Join(werr, serr, cerr); err != nil {
				// Not os.Remove(lock): if this run was suspended for longer than lockTiming.stale, its lock may have
				// been broken and the path may hold another run's fresh lock by now. Without an identity the file is
				// left to the stale rule.
				if serr == nil {
					releaser(lock, fi)()
				}
				return nil, fmt.Errorf("cannot write the registry lock %q: %w", lock, err)
			}
			return releaser(lock, fi), nil
		}
		// On Windows a lock whose removal is still pending, or a directory in its place, is "access denied" and not
		// "exists", so there every error is retried (and an old leftover broken) until the deadline, after poll like
		// any wait: with no lock file in the way the error may simply repeat. Elsewhere any other error (no
		// permission, no space) cannot go away by waiting.
		retry := errors.Is(err, os.ErrExist) || lockOnWindows
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

// breakStale removes an abandoned lock and reports whether it did, so that the caller tries to take the lock again at
// once. Anything else, a lock file that is already gone included, is retried after lockTiming.poll.
// A lock is abandoned when its modification time is older than lockTiming.stale or (clock skew, a restored backup)
// well in the future.
func breakStale(lock string) bool {
	fi, err := lstatIdentity(lock)
	if err != nil {
		return false
	}
	if age := time.Since(fi.ModTime()); age <= lockTiming.stale && age >= -5*time.Second {
		return false
	}
	return claimStale(lock, fi)
}

// lstatIdentity is os.Lstat with the file identity already read: on Windows os.SameFile reads it lazily, from the path,
// so it would compare whatever is at the path by then instead of the file that was looked at. SameFile(fi, fi) forces
// the read.
func lstatIdentity(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err == nil {
		os.SameFile(fi, fi)
	}
	return fi, err
}

// claimStale removes the lock that was judged stale (judged is its Lstat) and reports whether it is gone. A path has
// no compare-and-delete, so the check that the path still holds the judged file (same file, same modification time:
// an inode number can be reused) and the removal are made one step by the claim of that lock: every run that removes
// a lock, a breaker here or the owner's unlock, holds its claim, and an O_EXCL create cannot replace a file that is
// still there. A fresh lock another run created meanwhile is therefore never removed or moved, and a second waiter
// that judged the same lock backs off while the first holds the claim.
func claimStale(lock string, judged os.FileInfo) bool {
	release, err := takeClaim(lock, judged)
	if err != nil {
		return false
	}
	defer release()
	lockStep()
	return removeIfSame(lock, judged, 0)
}

// claimPath names the claim of one lock file by its modification time, the same for the owner (its f.Stat) and a
// waiter (its Lstat). A stale lock's time is over lockTiming.stale old or in the future and a fresh one's is now, so
// the judged lock and a fresh one never share a claim. Two fresh locks can, where mtimes are coarse; that only makes a
// run back off (an unlock then leaves its lock for lockTiming.stale), never remove a wrong file, since the removal
// still checks the file itself.
func claimPath(lock string, fi os.FileInfo) string {
	return fmt.Sprintf("%s.claim-%d", lock, fi.ModTime().UnixNano())
}

// takeClaim creates the claim of the lock file fi with O_EXCL (closed at once, like the lock; the pid is for humans)
// and returns its release. A run killed while it holds a claim leaves the file behind, and that one lock is then never
// broken automatically: the timeout error's hand recovery (delete the lock) still works, since the next lock has
// another claim name.
func takeClaim(lock string, fi os.FileInfo) (release func(), err error) {
	c := claimPath(lock, fi)
	f, err := os.OpenFile(c, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Close()
	return func() { os.Remove(c) }, nil
}

// removeIfSame removes lock if it is still the file whose Lstat is fi, and reports whether it is gone. The caller holds
// fi's claim. On Windows a removal fails while another run briefly has the file open (to read the pid); it is tried
// again up to retries times.
func removeIfSame(lock string, fi os.FileInfo, retries int) bool {
	cur, err := lstatIdentity(lock)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || !os.SameFile(fi, cur) || !cur.ModTime().Equal(fi.ModTime()) {
		return false
	}
	err = os.Remove(lock)
	for i := 0; err != nil && !os.IsNotExist(err) && runtime.GOOS == "windows" && i < retries; i++ {
		time.Sleep(10 * time.Millisecond)
		err = os.Remove(lock)
	}
	return err == nil || os.IsNotExist(err)
}

// releaser returns the idempotent unlock for the lock file whose Lstat is ours. It removes the lock only if it is
// still ours, under its claim (see claimStale). A claim that exists already means that a run judged this lock stale
// (this one was suspended for longer than lockTiming.stale) and is removing it, so it is left to that run. A claim
// that cannot be created for another reason (no space, say) does not keep the lock for lockTiming.stale: it is removed
// with the check alone.
func releaser(lock string, ours os.FileInfo) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			release, err := takeClaim(lock, ours)
			if errors.Is(err, os.ErrExist) {
				return
			}
			if err == nil {
				defer release()
			}
			removeIfSame(lock, ours, 20)
		})
	}
}
