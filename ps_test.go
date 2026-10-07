package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	// A skipped main record would shift every index, so the listing is dropped as a whole.
	quotedMain := "worktree \"" + slash(base) + "/caf\\303\\251\"\nHEAD 1\nbranch refs/heads/main\n\nworktree " + slash(b) + "\nHEAD 2\nbranch refs/heads/x\n"
	if l := parseWorktreeList(quotedMain); l != nil {
		t.Errorf("listing with a skipped main record = %+v", l)
	}
	// A listing without any prunable line (git before 2.31) still parses.
	if l := parseWorktreeList("worktree " + slash(a) + "\nHEAD 1\nbranch refs/heads/main\n"); len(l) != 1 || l[0].Prunable {
		t.Errorf("old git listing = %+v", l)
	}
	if l := parseWorktreeList(""); len(l) != 0 {
		t.Errorf("empty listing = %+v", l)
	}
}

func TestLooseRealPath(t *testing.T) {
	base := realTempDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	symlinkOrSkip(t, real, link)
	if got, want := looseRealPath(filepath.Join(link, "gone", "deeper")), filepath.Join(real, "gone", "deeper"); got != want {
		t.Errorf("looseRealPath = %q, want %q", got, want)
	}
	if got := looseRealPath(real); got != real {
		t.Errorf("existing path = %q", got)
	}
}

func TestAttribute(t *testing.T) {
	base := realTempDir(t)
	main := filepath.Join(base, "a")
	repos := [][]worktreeInfo{{
		{Path: main},
		{Path: filepath.Join(base, "a-wt")},
		{Path: filepath.Join(main, ".worktrees", "nested")},
	}}
	cases := []struct {
		key  string
		wt   int
		want bool
	}{
		{filepath.Join(base, "a-wt"), 1, true},
		{filepath.Join(base, "a-wt", "laravel"), 1, true},
		{filepath.Join(base, "a-wt2", "laravel"), 0, false},           // a sibling with the same prefix
		{filepath.Join(main, ".worktrees", "nested", "app"), 2, true}, // the longest match wins
		{filepath.Join(main, "app"), 0, true},                         // inside the main worktree itself
		{filepath.Join(base, "elsewhere"), 0, false},
		{"relative", 0, false},
	}
	for _, c := range cases {
		_, wt, ok := attribute(c.key, repos)
		if ok != c.want || ok && wt != c.wt {
			t.Errorf("attribute(%q) = %d, %v; want %d, %v", c.key, wt, ok, c.wt, c.want)
		}
	}
}

func TestFoldRegistryMatchesMigrate(t *testing.T) {
	base := realTempDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	b, c := filepath.Join(base, "b"), filepath.Join(base, "c")
	symlinkOrSkip(t, real, b)
	symlinkOrSkip(t, real, c)
	for _, withReal := range []bool{false, true} {
		m := map[string]map[string]int{b: {"APP_PORT": 81}, c: {"APP_PORT": 82}}
		if withReal {
			m[real] = map[string]int{"APP_PORT": 83}
		}
		folded := foldRegistry(&Registry{Worktrees: m})
		migrated := &Registry{Worktrees: map[string]map[string]int{}}
		for k, v := range m {
			migrated.Worktrees[k] = v
		}
		migrated.migrate(real)
		if len(folded) != 1 || len(migrated.Worktrees) != 1 || folded[real]["APP_PORT"] != migrated.Worktrees[real]["APP_PORT"] {
			t.Errorf("withReal=%v: folded %v, migrated %v", withReal, folded, migrated.Worktrees)
		}
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
	if !strings.Contains(errOut, "registry entries not listed: 1") {
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
	if strings.Contains(out, "relative") || !strings.Contains(errOut, "registry entries not listed: 2") {
		t.Errorf("stdout = %q, stderr = %q", out, errOut)
	}
	es := psJSON(t, "--all")
	if len(es) != 2 {
		t.Fatalf("entries = %+v", es)
	}
	for _, e := range es {
		if e.State != stateStale || e.Worktree != "" || e.Name != "" {
			t.Errorf("entry = %+v", e)
		}
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
		for _, root := range []string{main, wt, filepath.Dir(mustRegistryPath(t))} {
			filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if fi.IsDir() && fi.Name() == ".git" {
					return filepath.SkipDir
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

func TestPsNestedWorktreeAndUnattributedEntries(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	nested := filepath.Join(main, ".worktrees", "nested")
	runGit(t, main, "worktree", "add", "-q", nested, "-b", "nested")
	plain := filepath.Join(realTempDir(t), "plain") // exists, but is not in any repository
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, map[string]map[string]int{
		wt:     {"APP_PORT": 81},
		nested: {"APP_PORT": 82},
		plain:  {"APP_PORT": 83},
	})
	states := map[string]entry{}
	for _, e := range psJSON(t) {
		states[e.Dir] = e
	}
	if len(states) != 2 || states[nested].Worktree != "nested" || states[nested].Branch != "nested" || states[nested].State != stateNone {
		t.Errorf("default entries = %+v", states)
	}
	states = map[string]entry{}
	for _, e := range psJSON(t, "--all") {
		states[e.Dir] = e
	}
	if len(states) != 3 || states[plain].State != stateUnattributed || states[plain].Worktree != "" {
		t.Errorf("--all entries = %+v", states)
	}
}

func TestPsJSONEscapesFormatCharacters(t *testing.T) {
	setupWorktreeRepo(t)
	key := filepath.Join(string(filepath.Separator), "x", "a\u202eb")
	writeRegistry(t, map[string]map[string]int{key: {"APP_PORT": 81}})
	out, _, err := runPs(t, "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(out, '\u202e') {
		t.Errorf("raw format character in %q", out)
	}
	var es []entry
	if err := json.Unmarshal([]byte(out), &es); err != nil || len(es) != 1 || !strings.Contains(es[0].Dir, "\u202e") {
		t.Errorf("round trip: %v, %+v", err, es)
	}
}

func TestPsAllAttributesOtherRepositoriesDespiteGitDir(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	_, otherWt := newRepoWithWorktree(t, realTempDir(t), "other", "x")
	writeRegistry(t, map[string]map[string]int{wt: {"APP_PORT": 81}, otherWt: {"APP_PORT": 82}})
	gitDir, err := gitOut(wt, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	// The user's own GIT_DIR still decides which repository is the current one, but it must not leak into the
	// queries made in the other entries' directories.
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_WORK_TREE", wt)
	got := map[string]entry{}
	for _, e := range psJSON(t, "--all") {
		got[e.Dir] = e
	}
	if len(got) != 2 || got[otherWt].Worktree != "other-x" || got[wt].Worktree != "app-feat" {
		t.Errorf("entries = %+v", got)
	}
}

// TestHelperProcess is the child of TestRunOutput (it is not a test of its own).
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("SW_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "env":
		for _, kv := range os.Environ() {
			os.Stdout.WriteString(kv + "\n")
		}
	case "stdin":
		n, _ := io.Copy(io.Discard, os.Stdin)
		os.Stdout.WriteString(strconv.FormatInt(n, 10))
	case "big":
		os.Stdout.Write(bytes.Repeat([]byte("x"), maxOutput+1))
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func TestRunOutputChildBehavior(t *testing.T) {
	t.Setenv("SW_PARENT_MARK", "leaked")
	run := func(mode string, timeout time.Duration) ([]byte, error) {
		return runOutput("", []string{"SW_HELPER=" + mode}, timeout, os.Args[0], "-test.run=^TestHelperProcess$")
	}
	out, err := run("env", 20*time.Second)
	if err != nil || !strings.Contains(string(out), "SW_HELPER=env") || strings.Contains(string(out), "SW_PARENT_MARK") {
		t.Errorf("env: %q, %v", out, err)
	}
	if out, err := run("stdin", 20*time.Second); err != nil || !strings.HasPrefix(string(out), "0") {
		t.Errorf("stdin: %q, %v", out, err)
	}
	if _, err := run("big", 20*time.Second); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("big output: err = %v", err)
	}
	start := time.Now()
	if _, err := run("sleep", 300*time.Millisecond); err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("timeout: err = %v after %v", err, time.Since(start))
	}
}
