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

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicReplacesAndKeepsMode(t *testing.T) {
	dir := realTempDir(t)
	p := filepath.Join(dir, "registry.json")
	if err := writeFileAtomic(p, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("new file mode = %v", fi.Mode().Perm())
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFileAtomic(p, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two\n" {
		t.Errorf("content = %q", b)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("existing mode was not kept: %v", fi.Mode().Perm())
		}
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "registry.json" {
		t.Errorf("directory holds %v", names)
	}
}

func TestWriteFileAtomicFailureLeavesTargetAndNoTempFile(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "registry.json")
	writeFile(t, filepath.Join(target, "keep"), "x") // a non-empty directory cannot be replaced by a file
	old := renameRetries
	renameRetries = 2
	t.Cleanup(func() { renameRetries = old })
	if err := writeFileAtomic(target, []byte("data")); err == nil {
		t.Fatal("replacing a directory succeeded")
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "registry.json" {
		t.Errorf("directory holds %v", names)
	}
	if b, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(b) != "x" {
		t.Errorf("the target directory was damaged: %q, %v", b, err)
	}
}

func TestWriteFileAtomicWritesThroughASymlink(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "dotfiles", "registry.json")
	writeFile(t, dest, "old\n")
	link := filepath.Join(dir, "registry.json")
	symlinkOrSkip(t, dest, link)
	if err := writeFileAtomic(link, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new\n" {
		t.Errorf("the link target holds %q", b)
	}
	if names := dirNames(t, filepath.Join(dir, "dotfiles")); len(names) != 1 {
		t.Errorf("target directory holds %v", names)
	}
}

func TestWriteFileAtomicCreatesWhatADanglingLinkPointsAt(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "dotfiles", "registry.json")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "registry.json")
	symlinkOrSkip(t, filepath.Join("dotfiles", "registry.json"), link) // relative, and not there yet
	if err := writeFileAtomic(link, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new\n" {
		t.Errorf("the link target holds %q", b)
	}
}

func TestRegistrySaveRoundTrip(t *testing.T) {
	setupWorktreeRepo(t)
	r := &Registry{Worktrees: map[string]map[string]int{"/a": {"APP_PORT": 81}, "/b": {"APP_PORT": 82}}}
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	got, err := loadRegistry()
	if err != nil || len(got.Worktrees) != 2 || got.Worktrees["/b"]["APP_PORT"] != 82 {
		t.Errorf("loaded %+v, %v", got, err)
	}
	if names := dirNames(t, filepath.Dir(mustRegistryPath(t))); len(names) != 1 {
		t.Errorf("config directory holds %v", names)
	}
}

// A race smoke test: a reader never sees a partial file while a writer replaces it (with the old os.WriteFile the
// truncate-then-write window shows up as a parse error). Only outcomes are asserted, and the loop is short so that
// Windows CI stays fast; there a read may also fail because the file is being replaced, which is not a parse error.
func TestRegistrySaveIsNeverSeenPartially(t *testing.T) {
	setupWorktreeRepo(t)
	filler := strings.Repeat("x", 200)
	save := func(i int) error {
		r := &Registry{Worktrees: map[string]map[string]int{}}
		for k := 0; k < 30; k++ {
			r.Worktrees["/"+filler+strconv.Itoa(i)+"/"+strconv.Itoa(k)] = map[string]int{"APP_PORT": 81 + k}
		}
		return r.save()
	}
	if err := save(0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	parseErrors := make(chan error, 1000)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				time.Sleep(time.Millisecond) // do not starve the writer, which Windows CI would turn into a failed replace
				if _, err := loadRegistry(); err != nil && strings.Contains(err.Error(), "failed to parse") {
					select {
					case parseErrors <- err:
					default:
					}
				}
			}
		}()
	}
	for i := 1; i <= 60; i++ {
		if err := save(i); err != nil {
			t.Errorf("save %d: %v", i, err)
			break
		}
	}
	close(done)
	wg.Wait()
	close(parseErrors)
	if err, ok := <-parseErrors; ok {
		t.Errorf("a reader saw a partial registry: %v", err)
	}
}
