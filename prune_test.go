package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// runPrune runs prune with input as the answer to the prompt and returns the output and what is left of the input.
func runPrune(t *testing.T, input io.Reader, args ...string) (out string, err error) {
	t.Helper()
	old := stdin
	stdin = input
	t.Cleanup(func() { stdin = old })
	b := captureStdout(t)
	err = cmdPrune(args)
	return b.String(), err
}

func registryBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(mustRegistryPath(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustMkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func noLockFile(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
		t.Errorf("the lock file exists or cannot be checked: %v", err)
	}
}

// staleRegistry saves a registry with a usable record of every kind next to unusable ones and returns the usable
// directories.
func staleRegistry(t *testing.T) (live, liveMain, base string) {
	t.Helper()
	base = realTempDir(t)
	live = mustMkdir(t, filepath.Join(base, "live"))
	liveMain = mustMkdir(t, filepath.Join(base, "main"))
	sep := string(filepath.Separator)
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{
			live:                                  {"APP_PORT": 81},
			filepath.Join(base, "gone"):           {"APP_PORT": 82},
			"rel":                                 {"APP_PORT": 83},
			base + sep + "a" + sep + "..":         {"APP_PORT": 84},
			`\\host\share\x`:                      {"APP_PORT": 85},
			filepath.Join(base, "file-not-a-dir"): {"APP_PORT": 86},
		},
		Mains: map[string]MainRecord{
			liveMain: {Ports: map[string]int{"APP_PORT": 80}, Defaults: map[string]int{"APP_PORT": 80},
				Worktrees: []string{live, filepath.Join(base, "gone"), "rel"}},
			filepath.Join(base, "gone-main"): {Ports: map[string]int{"APP_PORT": 90}, Worktrees: []string{live}},
			"rel-main":                       mainRec(91),
		},
	})
	writeFile(t, filepath.Join(base, "file-not-a-dir"), "x")
	return live, liveMain, base
}

func TestPruneRejectsArgumentsBeforeReadingAnything(t *testing.T) {
	isolatedRegistry(t)
	in := strings.NewReader("y\n")
	if _, err := runPrune(t, in, "--all"); err == nil || !strings.Contains(err.Error(), "unknown argument: --all") {
		t.Fatalf("err = %v", err)
	}
	if in.Len() != 2 {
		t.Error("stdin was read")
	}
	if _, err := os.Lstat(mustRegistryPath(t)); !os.IsNotExist(err) {
		t.Errorf("the registry was created: %v", err)
	}
	noLockFile(t)
}

func TestPruneWithNothingToRemove(t *testing.T) {
	isolatedRegistry(t)
	in := strings.NewReader("y\n")
	out, err := runPrune(t, in)
	if err != nil || !strings.Contains(out, "nothing to remove") {
		t.Fatalf("no registry: err = %v\n%s", err, out)
	}
	if _, err := os.Lstat(mustRegistryPath(t)); !os.IsNotExist(err) {
		t.Errorf("the registry was created: %v", err)
	}

	base := realTempDir(t)
	live, main := mustMkdir(t, filepath.Join(base, "wt")), mustMkdir(t, filepath.Join(base, "main"))
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{live: {"APP_PORT": 81}},
		Mains:     map[string]MainRecord{main: {Ports: map[string]int{"APP_PORT": 80}, Worktrees: []string{live}}},
	})
	before := registryBytes(t)
	out, err = runPrune(t, in)
	if err != nil || !strings.Contains(out, "nothing to remove") || strings.Contains(out, "[y/N]") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if in.Len() != 2 {
		t.Error("stdin was read")
	}
	if string(registryBytes(t)) != string(before) {
		t.Error("the registry changed")
	}
	noLockFile(t)
}

func TestPruneWithYesRemovesUnusableRecords(t *testing.T) {
	isolatedRegistry(t)
	live, liveMain, base := staleRegistry(t)
	in := strings.NewReader("n\n")
	out, err := runPrune(t, in, "-y")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if in.Len() != 2 || strings.Contains(out, "[y/N]") {
		t.Error("-y asked or read stdin")
	}
	for _, want := range []string{
		fmt.Sprintf("worktree %q (ports: APP_PORT=82)", filepath.Join(base, "gone")),
		fmt.Sprintf("worktree %q (ports: APP_PORT=83)", "rel"),
		fmt.Sprintf("worktree %q", `\\host\share\x`),
		fmt.Sprintf("worktree %q", filepath.Join(base, "file-not-a-dir")),
		fmt.Sprintf("main worktree %q (ports: APP_PORT=90)", filepath.Join(base, "gone-main")),
		fmt.Sprintf("main worktree %q", "rel-main"),
		fmt.Sprintf("linked worktree %q in the record of main worktree %q", filepath.Join(base, "gone"), liveMain),
		"`docker compose ls -a`",
		"removed 5 worktree record(s), 2 main worktree record(s), 2 linked worktree entr(ies)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, fmt.Sprintf("worktree %q (", live)) || strings.Contains(out, fmt.Sprintf("main worktree %q (", liveMain)) {
		t.Errorf("a usable record is listed:\n%s", out)
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Worktrees) != 1 || reg.Worktrees[live]["APP_PORT"] != 81 {
		t.Errorf("worktrees = %v", reg.Worktrees)
	}
	if rec, ok := reg.Mains[liveMain]; len(reg.Mains) != 1 || !ok || rec.Ports["APP_PORT"] != 80 || !slices.Equal(rec.Worktrees, []string{live}) {
		t.Errorf("mains = %+v", reg.Mains)
	}
	noLockFile(t)
}

func TestPruneAnswers(t *testing.T) {
	for _, ans := range []string{"n\n", "\n", "", "no\n", "yy\n"} {
		isolatedRegistry(t)
		staleRegistry(t)
		before := registryBytes(t)
		out, err := runPrune(t, strings.NewReader(ans))
		if err != nil || !strings.Contains(out, "[y/N]") || !strings.Contains(out, "aborted") {
			t.Errorf("%q: err = %v\n%s", ans, err, out)
		}
		if string(registryBytes(t)) != string(before) {
			t.Errorf("%q: the registry changed", ans)
		}
		noLockFile(t)
	}
	for _, ans := range []string{"y\n", "YES\n", " y "} {
		isolatedRegistry(t)
		live, _, _ := staleRegistry(t)
		out, err := runPrune(t, strings.NewReader(ans))
		if err != nil || !strings.Contains(out, "removed 5 worktree record(s)") {
			t.Errorf("%q: err = %v\n%s", ans, err, out)
		}
		if reg, _ := loadRegistry(); len(reg.Worktrees) != 1 || reg.Worktrees[live] == nil {
			t.Errorf("%q: worktrees = %v", ans, reg.Worktrees)
		}
	}
}

// hookReader runs f once, at the first read, as if another run changed things while the prompt waited.
type hookReader struct {
	f  func()
	in io.Reader
}

func (h *hookReader) Read(p []byte) (int, error) {
	if h.f != nil {
		h.f()
		h.f = nil
	}
	return h.in.Read(p)
}

func TestPruneRespectsChangesBetweenThePromptAndTheLock(t *testing.T) {
	isolatedRegistry(t)
	live, liveMain, base := staleRegistry(t)
	gone, newGone := filepath.Join(base, "gone"), filepath.Join(base, "new-gone")
	var hookErr error
	in := &hookReader{in: strings.NewReader("y\n"), f: func() {
		if _, err := os.Lstat(lockFilePath(t)); !os.IsNotExist(err) {
			t.Errorf("the lock is held while the prompt waits: %v", err)
		}
		mustMkdir(t, gone) // the listed worktree (and linked entry) is back
		reg, err := loadRegistry()
		if err != nil {
			hookErr = err
			return
		}
		delete(reg.Worktrees, "rel")                            // removed by someone else
		delete(reg.Mains, "rel-main")                           // likewise
		reg.Worktrees[newGone] = map[string]int{"APP_PORT": 87} // not listed, so not confirmed
		hookErr = reg.save()
	}}
	out, err := runPrune(t, in)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, want := range []string{
		fmt.Sprintf("kept (changed since the list): worktree %q", gone),
		fmt.Sprintf("kept (changed since the list): linked worktree %q in the record of main worktree %q", gone, liveMain),
		"removed 3 worktree record(s), 1 main worktree record(s), 1 linked worktree entr(ies)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	wts := slices.Sorted(maps.Keys(reg.Worktrees))
	if want := slices.Sorted(slices.Values([]string{live, gone, newGone})); !slices.Equal(wts, want) {
		t.Errorf("worktrees = %v, want %v", wts, want)
	}
	if rec := reg.Mains[liveMain]; len(reg.Mains) != 1 || !slices.Equal(rec.Worktrees, []string{live, gone}) {
		t.Errorf("mains = %+v", reg.Mains)
	}
	noLockFile(t)
}

func TestPruneTreatsAnAliasKeyAsItsTarget(t *testing.T) {
	isolatedRegistry(t)
	base := realTempDir(t)
	target := mustMkdir(t, filepath.Join(base, "real"))
	alias := filepath.Join(base, "alias")
	symlinkOrSkip(t, target, alias)
	saveRegistry(t, &Registry{Worktrees: map[string]map[string]int{alias: {"APP_PORT": 81}}})
	before := registryBytes(t)
	if out, err := runPrune(t, strings.NewReader(""), "-y"); err != nil || !strings.Contains(out, "nothing to remove") {
		t.Fatalf("an alias of an existing directory: err = %v\n%s", err, out)
	}
	if string(registryBytes(t)) != string(before) {
		t.Error("the registry changed")
	}

	// The target is gone: listed. It is back before the lock: kept.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	in := &hookReader{in: strings.NewReader("y\n"), f: func() { mustMkdir(t, target) }}
	out, err := runPrune(t, in)
	if err != nil || !strings.Contains(out, fmt.Sprintf("worktree %q (ports: APP_PORT=81)", alias)) ||
		!strings.Contains(out, fmt.Sprintf("kept (changed since the list): worktree %q", alias)) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if string(registryBytes(t)) != string(before) {
		t.Error("the registry changed although nothing was removed")
	}

	// The target is gone for good: removed.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if out, err := runPrune(t, strings.NewReader(""), "-y"); err != nil || !strings.Contains(out, "removed 1 worktree record(s)") {
		t.Fatalf("a dangling alias: err = %v\n%s", err, out)
	}
	if reg, _ := loadRegistry(); len(reg.Worktrees) != 0 {
		t.Errorf("worktrees = %v", reg.Worktrees)
	}
}

func TestPruneDoesNotFollowALinkedMainKey(t *testing.T) {
	isolatedRegistry(t)
	base := realTempDir(t)
	target := mustMkdir(t, filepath.Join(base, "real"))
	alias := filepath.Join(base, "alias")
	symlinkOrSkip(t, target, alias)
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{alias: mainRec(80)}})
	out, err := runPrune(t, strings.NewReader(""), "-y")
	if err != nil || !strings.Contains(out, "removed 0 worktree record(s), 1 main worktree record(s)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestPruneEscapesUntrustedKeys(t *testing.T) {
	isolatedRegistry(t)
	base := realTempDir(t)
	key := filepath.Join(base, "x\x1b[31my")
	main := mustMkdir(t, filepath.Join(base, "main"))
	saveRegistry(t, &Registry{
		Worktrees: map[string]map[string]int{key: {"P\x1b": 81}},
		Mains:     map[string]MainRecord{main: {Ports: map[string]int{}, Worktrees: []string{key}}},
	})
	out, err := runPrune(t, strings.NewReader(""), "-y")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("a control character reached the output:\n%q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("%q", key)) || !strings.Contains(out, `P\x1b=81`) {
		t.Errorf("output:\n%s", out)
	}
}

func TestPruneWhenTheRegistryIsLocked(t *testing.T) {
	isolatedRegistry(t)
	staleRegistry(t)
	before := registryBytes(t)
	fastLock(t, 50*time.Millisecond, time.Hour)
	lock := plantLock(t, 0)
	out, err := runPrune(t, strings.NewReader(""), "-y")
	if err == nil || !strings.Contains(err.Error(), "nothing was removed (run prune again)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if string(registryBytes(t)) != string(before) {
		t.Error("the registry changed")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the other run's lock is gone: %v", err)
	}
}
