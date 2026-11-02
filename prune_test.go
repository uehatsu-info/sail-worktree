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
		fmt.Sprintf("main worktree %q (ports: APP_PORT=90): it is not a usable local directory", filepath.Join(base, "gone-main")),
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

// projectDir makes dir a project directory for replacedMain (a .sail-worktree.json in it) and returns it.
func projectDir(t *testing.T, dir string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, configName), "{}\n")
	return dir
}

// aliasMain returns a main key that reaches the project directory real through a symbolic link in the middle of the
// path, as after moving ~/work and leaving a ~/work link behind (#71).
func aliasMain(t *testing.T) (alias, real string) {
	t.Helper()
	base := realTempDir(t)
	real = projectDir(t, mustMkdir(t, filepath.Join(base, "data", "app")))
	symlinkOrSkip(t, filepath.Join(base, "data"), filepath.Join(base, "work"))
	return filepath.Join(base, "work", "app"), real
}

// movedRoot returns the main keys of a project that moved from the repository root into laravel/: the old root has
// no markers any more, the new directory has (#71).
func movedRoot(t *testing.T) (root, sub string) {
	t.Helper()
	root = realTempDir(t)
	sub = projectDir(t, mustMkdir(t, filepath.Join(root, "laravel")))
	writeFile(t, filepath.Join(root, "README.md"), "x") // the rest of the repository stays at the root
	return root, sub
}

func TestReplacedMainRecordsAreWarnedAndPruned(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keys   func(t *testing.T) (stale, live string)
		reason func(stale, live string) string
	}{
		{"alias", aliasMain, func(_, live string) string { return fmt.Sprintf("its real path %q has its own record", live) }},
		{"moved root", movedRoot, func(_, live string) string {
			return fmt.Sprintf("it is no longer a project directory and main worktree %q below it is recorded", live)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedRegistry(t)
			stale, live := tc.keys(t)
			saveRegistry(t, &Registry{Mains: map[string]MainRecord{stale: mainRec(80), live: mainRec(80)}})
			reason := tc.reason(stale, live)
			out, err := runCheck(t)
			if err != nil || strings.Contains(out, "conflicts:") {
				t.Fatalf("a replaced record conflicts: err = %v\n%s", err, out)
			}
			if want := fmt.Sprintf("the record of main worktree %q keeps ports free, but %s: APP_PORT=80", stale, reason); !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
			out, err = runPrune(t, strings.NewReader(""), "-y")
			if err != nil || !strings.Contains(out, "removed 0 worktree record(s), 1 main worktree record(s)") {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if want := fmt.Sprintf("main worktree %q (ports: APP_PORT=80): %s", stale, reason); !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
			reg, _ := loadRegistry()
			if _, ok := reg.Mains[live]; len(reg.Mains) != 1 || !ok {
				t.Errorf("mains = %+v", reg.Mains)
			}
		})
	}
}

// A main record is replaced only when a live record takes its place; without one it keeps holding its ports.
func TestMainRecordsThatAreNotReplaced(t *testing.T) {
	alone := func(stale, _ string) map[string]MainRecord { return map[string]MainRecord{stale: mainRec(80)} }
	both := func(stale, live string) map[string]MainRecord {
		return map[string]MainRecord{stale: mainRec(80), live: mainRec(80)}
	}
	for _, tc := range []struct {
		name  string
		keys  func(t *testing.T) (string, string)
		mains func(stale, live string) map[string]MainRecord
	}{
		{"alias whose real path has no record", aliasMain, alone},
		{"alias whose real path is refused by validKey", func(t *testing.T) (string, string) {
			alias, real := aliasMain(t)
			refuseKeyLinks(t, real)
			return alias, real
		}, both},
		{"old root with no record below it", movedRoot, alone},
		{"old root that is still a project", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			return projectDir(t, root), sub
		}, both},
		{"old root whose marker is a dangling link", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			symlinkOrSkip(t, filepath.Join(root, "missing"), filepath.Join(root, configName))
			return root, sub
		}, both},
		{"old root with artisan and a compose file", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			writeFile(t, filepath.Join(root, artisanName), "")
			writeFile(t, filepath.Join(root, "docker-compose.yml"), "")
			return root, sub
		}, both},
		{"record below has no markers", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			if err := os.Remove(filepath.Join(sub, configName)); err != nil {
				t.Fatal(err)
			}
			return root, sub
		}, both},
		{"record below is not replaced by one above it", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			if err := os.Remove(filepath.Join(sub, configName)); err != nil {
				t.Fatal(err)
			}
			return sub, projectDir(t, root) // a main on a branch without the project in laravel/ for now
		}, both},
		{"record below is refused by validKey", func(t *testing.T) (string, string) {
			root, sub := movedRoot(t)
			refuseKeyLinks(t, sub)
			return root, sub
		}, both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedRegistry(t)
			stale, live := tc.keys(t)
			saveRegistry(t, &Registry{Mains: tc.mains(stale, live)})
			out, _ := runCheck(t)
			if strings.Contains(out, "no longer a project directory") || strings.Contains(out, "has its own record") {
				t.Errorf("a record that nothing replaces was warned about:\n%s", out)
			}
			out, err := runPrune(t, strings.NewReader(""), "-y")
			if err != nil || strings.Contains(out, fmt.Sprintf("main worktree %q (", stale)) {
				t.Errorf("err = %v\n%s", err, out)
			}
		})
	}
}

// replyAfter is a prompt answer that runs fn first, as a user would act while the prompt waits.
type replyAfter struct {
	fn   func()
	done bool
}

func (r *replyAfter) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.fn()
	r.done = true
	return copy(p, "y\n"), nil
}

func TestPruneTestsAReplacedMainAgainAfterThePrompt(t *testing.T) {
	isolatedRegistry(t)
	root, sub := movedRoot(t)
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{root: mainRec(80), sub: mainRec(81)}})
	out, err := runPrune(t, &replyAfter{fn: func() { projectDir(t, root) }})
	if err != nil || !strings.Contains(out, fmt.Sprintf("kept (changed since the list): main worktree %q", root)) ||
		!strings.Contains(out, "removed 0 worktree record(s), 0 main worktree record(s)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if reg, _ := loadRegistry(); len(reg.Mains) != 2 {
		t.Errorf("mains = %+v", reg.Mains)
	}
}

// Under the lock only the registry is compared: a replaced record is kept when it changed since the list (up or check
// recorded it again) or when the record that replaces it is gone.
func TestPruneKeepsAReplacedMainThatChangedBeforeTheLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(root, sub string) *Registry
	}{
		{"record changed", func(root, sub string) *Registry {
			return &Registry{Mains: map[string]MainRecord{root: mainRec(82), sub: mainRec(81)}}
		}},
		{"replacing record gone", func(root, _ string) *Registry {
			return &Registry{Mains: map[string]MainRecord{root: mainRec(80)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedRegistry(t)
			root, sub := movedRoot(t)
			saveRegistry(t, &Registry{Mains: map[string]MainRecord{root: mainRec(80), sub: mainRec(81)}})
			orig := pruneLock
			t.Cleanup(func() { pruneLock = orig })
			pruneLock = func() (func(), error) {
				saveRegistry(t, tc.change(root, sub))
				return orig()
			}
			out, err := runPrune(t, strings.NewReader(""), "-y")
			if err != nil || !strings.Contains(out, fmt.Sprintf("kept (changed since the list): main worktree %q", root)) {
				t.Fatalf("err = %v\n%s", err, out)
			}
			if reg, _ := loadRegistry(); !slices.Contains(slices.Collect(maps.Keys(reg.Mains)), root) {
				t.Errorf("mains = %+v", reg.Mains)
			}
		})
	}
}

// check warns about exactly the linked worktree entries that prune removes (#78).
func TestCheckWarnsAboutTheLinkedEntriesPruneRemoves(t *testing.T) {
	isolatedRegistry(t)
	_, liveMain, base := staleRegistry(t)
	out, _ := runCheck(t)
	var warned []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, " lists linked worktree ") {
			warned = append(warned, l)
		}
	}
	want := []string{filepath.Join(base, "gone"), "rel"}
	if len(warned) != len(want) {
		t.Fatalf("warnings about list entries = %q, want %d\n%s", warned, len(want), out)
	}
	for i, w := range want {
		line := fmt.Sprintf("the record of main worktree %q lists linked worktree %q, which is not a usable local "+
			"directory (the entry holds no ports; prune removes it)", liveMain, w)
		if !strings.Contains(warned[i], line) {
			t.Errorf("warning %d = %q, want %q", i, warned[i], line)
		}
	}
	out, err := runPrune(t, strings.NewReader(""), "-y")
	if err != nil || !strings.Contains(out, fmt.Sprintf("%d linked worktree entr(ies)", len(want))) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	for _, w := range want {
		if !strings.Contains(out, fmt.Sprintf("linked worktree %q in the record of main worktree %q", w, liveMain)) {
			t.Errorf("prune did not list %q:\n%s", w, out)
		}
	}
}

// An entry of a main's list that is an alias of a live worktree is folded, as a worktrees key is: check does not warn
// about it and prune keeps it, also when it is tested again under the lock. A dangling alias is stale for both (#99).
func TestPruneKeepsAnAliasEntryOfALiveWorktree(t *testing.T) {
	isolatedRegistry(t)
	_, links := newLinkedDir(t, "alias")
	base := realTempDir(t)
	main := mustMkdir(t, filepath.Join(base, "main"))
	dangling := filepath.Join(base, "dangling")
	symlinkOrSkip(t, filepath.Join(base, "missing"), dangling)
	rec := mainRec(80)
	rec.Worktrees = []string{links[0], dangling}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{main: rec}})
	out, err := runCheck(t)
	if err != nil || strings.Contains(out, fmt.Sprintf("lists linked worktree %q", links[0])) ||
		!strings.Contains(out, fmt.Sprintf("lists linked worktree %q", dangling)) {
		t.Errorf("err = %v\n%s", err, out)
	}
	out, err = runPrune(t, strings.NewReader(""), "-y")
	if err != nil || !strings.Contains(out, "0 main worktree record(s), 1 linked worktree entr(ies)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Mains[main].Worktrees; !slices.Equal(got, []string{links[0]}) {
		t.Errorf("list = %q", got)
	}
	// The test under the lock agrees with the list: an alias entry listed by an older run is kept.
	done, kept := reg.prune(pruneList{linked: []linkedEntry{{main: main, worktree: links[0]}}})
	if len(done.linked) != 0 || len(kept) != 1 {
		t.Errorf("done = %+v, kept = %q", done, kept)
	}
}

// The list of a replaced main record leaves with it, so check does not warn about its entries one by one either.
func TestCheckDoesNotWarnAboutTheListOfAReplacedMain(t *testing.T) {
	isolatedRegistry(t)
	root, sub := movedRoot(t)
	gone := filepath.Join(root, "gone-wt")
	rec := mainRec(80)
	rec.Worktrees = []string{gone}
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{root: rec, sub: mainRec(81)}})
	out, err := runCheck(t)
	if err != nil || strings.Contains(out, " lists linked worktree ") {
		t.Errorf("err = %v\n%s", err, out)
	}
	out, err = runPrune(t, strings.NewReader(""), "-y")
	if err != nil || !strings.Contains(out, "1 main worktree record(s), 0 linked worktree entr(ies)") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

// A worktree on a branch from before the project moved into laravel/ does not record the main worktree's root again
// after prune removed that record (#98): since #95 up and check record a main only where it has the project.
func TestPrunedReplacedMainIsNotRecordedAgainFromAnOldBranch(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFakeSail(t, wt)
	if err := os.Remove(filepath.Join(main, configName)); err != nil {
		t.Fatal(err)
	}
	sub := projectDir(t, mustMkdir(t, filepath.Join(main, "laravel")))
	writeFile(t, filepath.Join(main, ".env"), "APP_PORT=8123\n") // the source of the worktree's .env, still at the root
	saveRegistry(t, &Registry{Mains: map[string]MainRecord{main: mainRec(80), sub: mainRec(80)}})
	if out, err := runPrune(t, strings.NewReader(""), "-y"); err != nil ||
		!strings.Contains(out, "removed 0 worktree record(s), 1 main worktree record(s)") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	captureRunner(t)
	captureStderr(t)
	if err := cmdUp(nil); err != nil {
		t.Fatal(err)
	}
	out, err := runCheck(t)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if _, ok := reg.Mains[main]; ok || len(reg.Mains) != 1 {
		t.Errorf("mains = %+v, want only %q", reg.Mains, sub)
	}
	if strings.Contains(out, "keeps ports free, but") {
		t.Errorf("check warns about a replaced record again:\n%s", out)
	}
}
