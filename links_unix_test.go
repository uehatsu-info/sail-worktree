//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCheckOwnEnvRejectsHardLink(t *testing.T) {
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
		t.Error("ハードリンクを拒否していない")
	}
	e := &envFile{lines: []string{"A=2"}}
	if err := e.Write(hard); err == nil {
		t.Error("ハードリンクへ書いた")
	}
	if b, _ := os.ReadFile(real); string(b) != "A=1\n" {
		t.Errorf("リンク先が書き換わった: %q", b)
	}
}

// Lstat の検査をすり抜けても、O_NOFOLLOW でシンボリックリンクを辿らない。
func TestOpenNoFollowRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sym")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(link, os.O_WRONLY|openNoFollow, 0o600); err == nil {
		f.Close()
		t.Error("O_NOFOLLOW がシンボリックリンクを辿った")
	}
}

func TestReadEnvIfRegularDoesNotBlockOnFIFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan bool, 1)
	go func() { _, ok := readEnvIfRegular(p); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Error("FIFO を読んだ")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("FIFO でブロックした")
	}
}
