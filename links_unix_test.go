//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckOwnEnvRefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(real, hard); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnEnv(hard); err == nil {
		t.Error("hard link not refused")
	}
	e := &envFile{lines: []string{"A=2"}}
	if err := e.Write(hard); err == nil {
		t.Error("wrote through a hard link")
	}
	if b, _ := os.ReadFile(real); string(b) != "A=1\n" {
		t.Errorf("link target was rewritten: %q", b)
	}
}

// Even if the Lstat check is bypassed, O_NOFOLLOW does not follow a symbolic link.
func TestOpenNoFollowRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sym")
	symlinkOrSkip(t, real, link)
	if f, err := os.OpenFile(link, os.O_WRONLY|openNoFollow, 0o600); err == nil {
		f.Close()
		t.Error("O_NOFOLLOW followed a symbolic link")
	}
}

// This only checks that a FIFO is rejected at the Lstat stage. A race that swaps the path for a FIFO after Lstat
// cannot be produced deterministically, so O_NONBLOCK and the re-Stat of the opened fd (readEnvIfRegular's second
// line of defense) are not verified by this test.
func TestReadEnvIfRegularDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		// A skip would let the FIFO check pass without running. A filesystem that cannot hold a FIFO (FUSE, a network
		// mount, FAT) under TMPDIR is the likely cause.
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	done := make(chan bool, 1)
	go func() { _, err := readEnvIfRegular(p); done <- err == nil }()
	select {
	case ok := <-done:
		if ok {
			t.Error("read a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}

func TestWriteFileNoFollowRefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(real, filepath.Join(dir, "hard")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileNoFollow(filepath.Join(dir, "hard"), []byte("x"), 0o600); !errors.Is(err, errNotOwnFile) {
		t.Errorf("a hard link is not refused as such: %v", err)
	}
	if b, _ := os.ReadFile(real); string(b) != "keep\n" {
		t.Errorf("link target was rewritten: %q", b)
	}
}

func TestWriteFileNoFollowModes(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "new")
	if err := writeFileNoFollow(created, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(created); fi.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v", fi.Mode().Perm())
	}
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFileNoFollow(existing, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o640 {
		t.Errorf("existing file mode changed to %v", fi.Mode().Perm())
	}
}

func TestWriteFileNoFollowRefusesFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- writeFileNoFollow(p, []byte("x"), 0o600) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("wrote to a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}

// Like the readEnvIfRegular test above, this checks only the Stat before the open (openRegular, #131): a swap for a
// FIFO between that Stat and the open cannot be produced deterministically, so O_NONBLOCK and the Stat of the opened
// file are not verified.
func TestReadSmallFileDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compose.yaml")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := readSmallFile(p); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Errorf("error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}

func TestLockCopesWithAFIFOAtTheLockPath(t *testing.T) {
	setupWorktreeRepo(t)
	fastLock(t, 100*time.Millisecond, time.Hour)
	p := lockFilePath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := lockRegistry(); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pid unknown") {
			t.Errorf("err = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}

func TestLockFailsAtOnceWhereWaitingCannotHelp(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory without the permission")
	}
	setupWorktreeRepo(t)
	fastLock(t, 30*time.Second, time.Hour)
	dir := filepath.Dir(mustRegistryPath(t))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	start := time.Now()
	_, err := lockRegistry()
	if err == nil || !strings.Contains(err.Error(), "cannot create the registry lock") || time.Since(start) > 10*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestReadOwnEnvRefusesHardLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	writeFile(t, real, "A=1\n")
	if err := os.Link(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnEnv(real); !errors.Is(err, errEnvHardLinked) || !strings.Contains(err.Error(), "hard-linked") {
		t.Errorf("err = %v", err)
	}
	// Other readers only refuse links of their own path.
	if _, err := readEnvIfRegular(real); err != nil {
		t.Errorf("readEnvIfRegular: %v", err)
	}
}

func TestUpDoesNotReadAHardLinkSwappedInAfterTheCheck(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	_, err := upWithEnvSwapped(t, wt, func(target, env string) {
		if err := os.Link(target, env); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !errors.Is(err, errEnvHardLinked) || errors.Is(err, errNotOwnFile) ||
		!strings.Contains(err.Error(), "hard-linked") || strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Fatalf("err = %v", err)
	}
}

// readEnv follows links (#123). Since #131 it Stats the path before opening it, so like the readSmallFile test above
// this checks only that Stat: O_NONBLOCK and the Stat of the opened file need a swap between the two.
func TestReadEnvDoesNotBlockOnFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	link := filepath.Join(dir, "link")
	symlinkOrSkip(t, fifo, link)
	for _, p := range []string{fifo, link} {
		done := make(chan error, 1)
		go func() { _, err := readEnv(p); done <- err }()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
				t.Errorf("%s: err = %v", p, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: blocked on a FIFO", p)
		}
	}
}

// up reads the copy source of a new .env before the lock, so a FIFO there must not hang it (#123).
func TestUpDoesNotBlockOnAFIFOAsTheCopySource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		example bool                                   // the FIFO is .env.example, so the main has no .env
		make    func(t *testing.T, main string) string // returns the FIFO
	}{
		{".env", false, func(t *testing.T, main string) string { return filepath.Join(main, ".env") }},
		{"link to a FIFO", false, func(t *testing.T, main string) string {
			fifo := filepath.Join(t.TempDir(), "fifo")
			symlinkOrSkip(t, fifo, filepath.Join(main, ".env"))
			return fifo
		}},
		{".env.example", true, func(t *testing.T, main string) string { return filepath.Join(main, ".env.example") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, wt := setupWorktreeRepo(t)
			writeFakeSail(t, wt)
			fifo := tc.make(t, main)
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
			}
			// The test means something only if up copies: no .env here, and for .env.example none in the main.
			if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
				t.Fatalf("the worktree has a .env: %v", err)
			}
			if tc.example {
				if _, err := os.Lstat(filepath.Join(main, ".env")); !os.IsNotExist(err) {
					t.Fatalf("the main worktree has a .env: %v", err)
				}
			}
			var locked bool
			old := upLock
			upLock = func() (func(), error) {
				locked = true
				return lockRegistry()
			}
			t.Cleanup(func() { upLock = old })
			calls := captureRunner(t)
			captureStdout(t)
			captureStderr(t) // up warns that the main's .env is not a regular file
			done := make(chan error, 1)
			go func() { done <- cmdUp(nil) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second): // longer than the read tests above: up runs git first
				// Release the blocked open and let up finish, so that it does not touch the state the cleanups restore.
				if w, oerr := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); oerr == nil {
					w.Close()
					select {
					case <-done:
					case <-time.After(10 * time.Second):
					}
				}
				t.Fatal("blocked on a FIFO")
			}
			if err == nil || !strings.Contains(err.Error(), "cannot read the source .env") || !strings.Contains(err.Error(), "is not a regular file") {
				t.Errorf("err = %v", err)
			}
			if locked {
				t.Error("the lock was taken")
			}
			if len(*calls) != 0 {
				t.Errorf("commands ran: %v", *calls)
			}
			if _, err := os.Lstat(filepath.Join(wt, ".env")); !os.IsNotExist(err) {
				t.Error(".env was written")
			}
		})
	}
}

// readEnv and readSmallFile follow links but open only a regular file: a character device, directly or through a link,
// is refused before the open, so an open with side effects (a tape rewinds, a terminal) never happens (#131).
func TestReadsRefuseADeviceBeforeOpeningIt(t *testing.T) {
	dir := t.TempDir()
	devLink := filepath.Join(dir, "dev")
	symlinkOrSkip(t, os.DevNull, devLink)
	// The Stat after the open would refuse /dev/null as well; only this FIFO shows that the refusal comes before the
	// open: without permissions, an open fails with EACCES for anyone but root.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("cannot create a FIFO (set TMPDIR to a filesystem that supports them): %v", err)
	}
	if err := os.Chmod(fifo, 0); err != nil {
		t.Fatal(err)
	}
	fifoLink := filepath.Join(dir, "fifo-link")
	symlinkOrSkip(t, fifo, fifoLink)
	paths := []string{os.DevNull, devLink}
	if os.Geteuid() != 0 { // root opens the FIFO despite its mode
		paths = append(paths, fifo, fifoLink)
	}
	reads := map[string]func(string) error{
		"readEnv":       func(p string) error { _, err := readEnv(p); return err },
		"readSmallFile": func(p string) error { _, err := readSmallFile(p); return err },
	}
	for name, read := range reads {
		for _, p := range paths {
			if err := read(p); err == nil || !strings.Contains(err.Error(), strconv.Quote(p)+" is not a regular file") {
				t.Errorf("%s(%s): err = %v", name, p, err)
			}
		}
	}

	// A regular file and a link to it are still read.
	regular := filepath.Join(dir, "regular")
	writeFile(t, regular, "A=1\n")
	regularLink := filepath.Join(dir, "regular-link")
	symlinkOrSkip(t, regular, regularLink)
	for _, p := range []string{regular, regularLink} {
		if e, err := readEnv(p); err != nil || !slices.Equal(e.lines, []string{"A=1"}) {
			t.Errorf("readEnv(%s): %v", p, err)
		}
		if b, err := readSmallFile(p); err != nil || string(b) != "A=1\n" {
			t.Errorf("readSmallFile(%s) = %q, %v", p, b, err)
		}
	}
}
