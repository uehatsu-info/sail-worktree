//go:build unix

package main

import (
	"os"
	"path/filepath"
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
	go func() { _, ok := readEnvIfRegular(p); done <- ok }()
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
	if err := writeFileNoFollow(filepath.Join(dir, "hard"), []byte("x"), 0o600); err == nil {
		t.Error("wrote through a hard link")
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
