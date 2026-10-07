package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRegistry writes the registry file (the test points the config directory at a temporary one).
func writeRegistry(t *testing.T, m map[string]map[string]int) {
	t.Helper()
	p, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(Registry{Worktrees: m})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(b))
}

// newRepoWithWorktree creates a repository with a linked worktree below base and returns both paths.
func newRepoWithWorktree(t *testing.T, base, name, branch string) (main, wt string) {
	t.Helper()
	main = filepath.Join(base, name)
	wt = filepath.Join(base, name+"-"+branch)
	writeFile(t, filepath.Join(main, "README.md"), name+"\n")
	runGit(t, main, "init", "-q", "-b", "main")
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-q", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", branch)
	return main, wt
}

func runPs(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	o, e := captureStdout(t), captureStderr(t)
	err = cmdPs(args)
	return o.String(), e.String(), err
}

func psJSON(t *testing.T, args ...string) []entry {
	t.Helper()
	out, _, err := runPs(t, append([]string{"--json"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	var es []entry
	if err := json.Unmarshal([]byte(out), &es); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return es
}

func TestParseWorktreeList(t *testing.T) {
	base := realTempDir(t)
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	gone := filepath.Join(base, "gone")
	for _, d := range []string{a, b, c} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	slash := filepath.ToSlash
	out := "worktree " + slash(a) + "\nHEAD 1111\nbranch refs/heads/main\n\n" +
		"worktree " + slash(b) + "\nHEAD 2222\nbranch refs/heads/feature/x\n\n" +
		"worktree " + slash(c) + "\nHEAD 3333\ndetached\nlocked\n\n" +
		"worktree " + slash(gone) + "\nHEAD 4444\nbranch refs/heads/old\nprunable gitdir file points to non-existent location\n\n" +
		"worktree \"" + slash(base) + "/caf\\303\\251\"\nHEAD 5555\nbranch refs/heads/q\n\n" +
		"worktree " + slash(filepath.Join(base, "bare")) + "\nbare\n"
	got := parseWorktreeList(out)
	want := []worktreeInfo{
		{Path: a, Branch: "main"},
		{Path: b, Branch: "feature/x"},
		{Path: c, Detached: true},
		{Path: gone, Branch: "old", Prunable: true},
		{Path: filepath.Join(base, "bare"), Bare: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// A listing without any prunable line (git before 2.31) still parses.
	if l := parseWorktreeList("worktree " + slash(a) + "\nHEAD 1\nbranch refs/heads/main\n"); len(l) != 1 || l[0].Prunable {
		t.Errorf("old git listing = %+v", l)
	}
	if l := parseWorktreeList(""); len(l) != 0 {
		t.Errorf("empty listing = %+v", l)
	}
}

func TestListWorktreesIgnoresGitDirVariables(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	other := realTempDir(t)
	otherMain, _ := newRepoWithWorktree(t, other, "other", "x")
	t.Setenv("GIT_DIR", filepath.Join(otherMain, ".git"))
	t.Setenv("GIT_WORK_TREE", otherMain)
	l, err := listWorktrees(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[0].Path != main || l[1].Path != wt {
		t.Errorf("list = %+v", l)
	}
}

func TestFoldRegistry(t *testing.T) {
	base := realTempDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	link2 := filepath.Join(base, "alink")
	symlinkOrSkip(t, real, link)
	symlinkOrSkip(t, real, link2)
	r := &Registry{Worktrees: map[string]map[string]int{
		link:  {"APP_PORT": 81},
		link2: {"APP_PORT": 82},
	}}
	got := foldRegistry(r)
	if len(got) != 1 || got[real]["APP_PORT"] != 82 { // the smallest alias key ("alink") is adopted
		t.Errorf("alias only: %v", got)
	}
	r.Worktrees[real] = map[string]int{"APP_PORT": 83}
	got = foldRegistry(r)
	if len(got) != 1 || got[real]["APP_PORT"] != 83 { // an entry under the real path wins
		t.Errorf("real path present: %v", got)
	}
	if len(r.Worktrees) != 3 {
		t.Errorf("the registry itself was changed: %v", r.Worktrees)
	}
}

func TestPsListsTheCurrentRepository(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	otherMain, otherWt := newRepoWithWorktree(t, realTempDir(t), "other", "x")
	_ = otherMain
	writeRegistry(t, map[string]map[string]int{
		wt:      {"APP_PORT": 81, "FORWARD_DB_PORT": 3307},
		otherWt: {"APP_PORT": 82},
	})
	out, errOut, err := runPs(t)
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Errorf("stderr = %q", errOut)
	}
	for _, s := range []string{"WORKTREE", "app-feat", "feat", "APP_PORT=81 FORWARD_DB_PORT=3307", wt} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "other-x") || strings.Contains(out, "APP_PORT=82") {
		t.Errorf("another repository is listed:\n%s", out)
	}

	es := psJSON(t)
	if len(es) != 1 {
		t.Fatalf("entries = %+v", es)
	}
	e := es[0]
	if e.Dir != wt || e.Worktree != "app-feat" || e.Subdir != "." || e.Branch != "feat" || e.State != "-" ||
		e.Name != projectName(main, wt, wt) || e.Ports["FORWARD_DB_PORT"] != 3307 {
		t.Errorf("entry = %+v", e)
	}

	all := psJSON(t, "--all")
	if len(all) != 2 {
		t.Fatalf("--all entries = %+v", all)
	}
	var found bool
	for _, e := range all {
		if e.Dir == otherWt {
			found = true
			if e.Worktree != "other-x" || e.Branch != "x" || e.Name != projectName(otherMain, otherWt, otherWt) {
				t.Errorf("other entry = %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("--all lacks the other repository: %+v", all)
	}
}

func TestPsKeepsProjectsOfOneRepositoryApart(t *testing.T) {
	main, wt := setupSubdirWorktreeRepo(t, false)
	writeRegistry(t, map[string]map[string]int{
		filepath.Join(wt, "laravel"): {"APP_PORT": 81},
		filepath.Join(wt, "admin"):   {"APP_PORT": 82},
	})
	for _, d := range []string{"laravel", "admin"} {
		if err := os.MkdirAll(filepath.Join(wt, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	es := psJSON(t)
	if len(es) != 2 || es[0].Subdir != "admin" || es[1].Subdir != "laravel" {
		t.Fatalf("entries = %+v", es)
	}
	if want := projectName(main, wt, filepath.Join(wt, "laravel")); es[1].Name != want {
		t.Errorf("name = %q, want %q", es[1].Name, want)
	}
	if es[0].Name == es[1].Name {
		t.Error("two projects share a compose project name")
	}
}

func TestPsShowsGoneWorktrees(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	gone := filepath.Join(filepath.Dir(wt), "app-gone")
	runGit(t, main, "worktree", "add", "-q", gone, "-b", "gone")
	pruned := filepath.Join(filepath.Dir(wt), "app-pruned")
	runGit(t, main, "worktree", "add", "-q", pruned, "-b", "pruned")
	writeRegistry(t, map[string]map[string]int{
		wt:     {"APP_PORT": 81},
		gone:   {"APP_PORT": 82},
		pruned: {"APP_PORT": 83},
	})
	if err := os.RemoveAll(gone); err != nil { // still listed by git (prunable)
		t.Fatal(err)
	}
	runGit(t, main, "worktree", "remove", "--force", pruned) // no longer listed

	out, errOut, err := runPs(t)
	if err != nil {
		t.Fatal(err)
	}
	es := psJSON(t)
	states := map[string]string{}
	for _, e := range es {
		states[e.Dir] = e.State
	}
	if len(es) != 2 || states[wt] != "-" || states[gone] != stateStale {
		t.Errorf("entries = %+v\n%s", es, out)
	}
	if !strings.Contains(errOut, "1 registry entries could not be attributed") {
		t.Errorf("stderr = %q", errOut)
	}

	all := psJSON(t, "--all")
	states = map[string]string{}
	for _, e := range all {
		states[e.Dir] = e.State
	}
	if len(all) != 3 || states[pruned] != stateStale {
		t.Errorf("--all entries = %+v", all)
	}
}

func TestPsFoldsAliasKeysInMemoryOnly(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	link := filepath.Join(realTempDir(t), "link")
	symlinkOrSkip(t, wt, link)
	writeRegistry(t, map[string]map[string]int{link: {"APP_PORT": 81}})
	p, _ := registryPath()
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	es := psJSON(t)
	if len(es) != 1 || es[0].Dir != wt || es[0].Ports["APP_PORT"] != 81 {
		t.Errorf("entries = %+v", es)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Errorf("the registry was rewritten:\n%s", after)
	}
}

func TestPsInvalidKeysAreNeverUsed(t *testing.T) {
	setupWorktreeRepo(t)
	writeRegistry(t, map[string]map[string]int{
		"":              {"APP_PORT": 81},
		"relative/path": {"APP_PORT": 82},
	})
	out, errOut, err := runPs(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "relative") || !strings.Contains(errOut, "2 registry entries") {
		t.Errorf("stdout = %q, stderr = %q", out, errOut)
	}
	es := psJSON(t, "--all")
	if len(es) != 2 || es[0].State != stateStale || es[1].State != stateStale || es[0].Worktree != "" {
		t.Errorf("entries = %+v", es)
	}
}

func TestPsEmptyAndBrokenRegistry(t *testing.T) {
	setupWorktreeRepo(t)
	out, _, err := runPs(t)
	if err != nil || !strings.Contains(out, "no registry entries") {
		t.Errorf("empty: %q, %v", out, err)
	}
	out, _, err = runPs(t, "--json")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Errorf("empty JSON: %q, %v", out, err)
	}
	p, _ := registryPath()
	writeFile(t, p, "{not json")
	for _, args := range [][]string{nil, {"--all"}, {"--json"}} {
		if _, _, err := runPs(t, args...); err == nil || !strings.Contains(err.Error(), "failed to parse") {
			t.Errorf("broken registry, args %v: err = %v", args, err)
		}
	}
}

func TestPsOutsideARepository(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Chdir(realTempDir(t))
	if _, _, err := runPs(t); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := runPs(t, "--all"); err != nil {
		t.Errorf("--all outside a repository: %v", err)
	}
}

func TestPsRejectsUnknownArguments(t *testing.T) {
	setupWorktreeRepo(t)
	if _, _, err := runPs(t, "--bogus"); err == nil || !strings.Contains(err.Error(), "unknown argument: --bogus") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := runPs(t, "extra"); err == nil {
		t.Error("a positional argument was accepted")
	}
}

func TestPsEscapesUntrustedText(t *testing.T) {
	setupWorktreeRepo(t)
	key := filepath.Join(string(filepath.Separator), "x", "a\u202eb\nc\x1b[31m")
	writeRegistry(t, map[string]map[string]int{key: {"EVIL\x1b_PORT": 81}})
	out, _, err := runPs(t, "--all")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"\u202e", "\x1b"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%q", bad, out)
		}
	}
	if !strings.Contains(out, `\u202e`) || !strings.Contains(out, `\n`) || len(strings.Split(strings.TrimSpace(out), "\n")) != 2 {
		t.Errorf("output = %q", out)
	}
}

func TestPsWritesNothing(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	writeFile(t, filepath.Join(wt, ".env"), "APP_KEY=base64:secret\n")
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, filepath.Join(main, "nope"): {"APP_PORT": 82}})
	snap := func() string {
		var b strings.Builder
		for _, root := range []string{wt, filepath.Dir(mustRegistryPath(t))} {
			filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err != nil || strings.Contains(p, ".git") {
					return nil
				}
				b.WriteString(p + " " + fi.ModTime().String() + " " + fi.Mode().String() + "\n")
				if fi.Mode().IsRegular() {
					c, _ := os.ReadFile(p)
					b.Write(c)
				}
				return nil
			})
		}
		return b.String()
	}
	before := snap()
	time.Sleep(10 * time.Millisecond)
	for _, args := range [][]string{nil, {"--all"}, {"--json"}, {"--all", "--json"}} {
		if _, _, err := runPs(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	if after := snap(); after != before {
		t.Errorf("files changed:\n%s\n---\n%s", before, after)
	}
}

func mustRegistryPath(t *testing.T) string {
	t.Helper()
	p, err := registryPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunOutput(t *testing.T) {
	out, err := runOutput("", nil, 10*time.Second, "git", "--version")
	if err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Errorf("git --version: %q, %v", out, err)
	}
	if _, err := runOutput("", nil, 10*time.Second, "sail-worktree-no-such-binary"); err == nil {
		t.Error("a missing binary was not an error")
	}
	w := &cappedBuffer{max: 4}
	if n, err := w.Write([]byte("ab")); n != 2 || err != nil || w.over {
		t.Errorf("first write: %d, %v, over=%v", n, err, w.over)
	}
	if n, err := w.Write([]byte("cdef")); n != 4 || err != nil || !w.over || w.buf.String() != "abcd" {
		t.Errorf("second write: %d, %v, over=%v, %q", n, err, w.over, w.buf.String())
	}
}
