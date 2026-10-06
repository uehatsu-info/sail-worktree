package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sampleCompose = `services:
    laravel.test:
        ports:
            - '${APP_PORT:-80}:80'
            - '${VITE_PORT:-5173}:${VITE_PORT:-5173}'
    mysql:
        ports:
            - '${FORWARD_DB_PORT:-3306}:3306'
`

func TestDetectPortVars(t *testing.T) {
	v := detectPortVars(sampleCompose)
	want := []PortVar{{"APP_PORT", 80}, {"VITE_PORT", 5173}, {"FORWARD_DB_PORT", 3306}}
	if len(v) != len(want) {
		t.Fatalf("got %v", v)
	}
	for i := range want {
		if v[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, v[i], want[i])
		}
	}
}

func TestAllocate(t *testing.T) {
	vars := []PortVar{{"APP_PORT", 80}, {"DB", 3306}}
	free := func(p int) bool { return p != 81 }
	got, err := allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{82: true}, free)
	if err != nil {
		t.Fatal(err)
	}
	if got["DB"] != 3310 || got["APP_PORT"] != 83 {
		t.Errorf("got %v", got)
	}
	// 他ワークツリーに取られた既存割当は再割当てされる
	got, _ = allocatePorts(vars, map[string]int{"DB": 3310}, map[int]bool{3310: true}, func(int) bool { return true })
	if got["DB"] != 3307 {
		t.Errorf("got %v", got)
	}
}

func TestEnvSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	e := &envFile{lines: []string{"# c", "APP_PORT=80", "APP_URL=\"http://localhost\""}}
	e.Set("APP_PORT", "81")
	e.Set("NEW", "1")
	if v, _ := e.Get("APP_URL"); v != "http://localhost" {
		t.Errorf("APP_URL=%q", v)
	}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	r, _ := readEnv(p)
	if v, _ := r.Get("APP_PORT"); v != "81" {
		t.Errorf("APP_PORT=%q", v)
	}
	if v, _ := r.Get("NEW"); v != "1" {
		t.Errorf("NEW=%q", v)
	}
}

func TestSessionCookieName(t *testing.T) {
	if got := sessionCookieName("app-wt-abc123"); got != "app-wt-abc123-session" {
		t.Errorf("got %q", got)
	}
}

func TestEnvSetCollapsesDuplicates(t *testing.T) {
	e := &envFile{lines: []string{"APP_PORT=80", "X=1", "export APP_PORT=90", "APP_PORT=95"}}
	e.Set("APP_PORT", "81")
	want := []string{"APP_PORT=81", "X=1"}
	if strings.Join(e.lines, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", e.lines, want)
	}
}

func TestEnvWriteMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows にはファイルのパーミッションの 0600 がない")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	e := &envFile{lines: []string{"A=1"}}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("新規作成のモード=%v", fi.Mode().Perm())
	}
	// 既存ファイルのモードは変えない
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := e.Write(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("既存のモードが変わった=%v", fi.Mode().Perm())
	}
}

func TestCheckOwnEnv(t *testing.T) {
	dir := t.TempDir()
	if err := checkOwnEnv(filepath.Join(dir, ".env")); err != nil {
		t.Errorf("無いのは許す: %v", err)
	}
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnEnv(real); err != nil {
		t.Errorf("通常ファイルは許す: %v", err)
	}
	link := filepath.Join(dir, "sym")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := checkOwnEnv(link); err == nil {
		t.Error("シンボリックリンクを拒否していない")
	}
	e := &envFile{lines: []string{"A=2"}}
	if err := e.Write(link); err == nil {
		t.Error("シンボリックリンクへ書いた")
	}
	if b, _ := os.ReadFile(real); string(b) != "A=1\n" {
		t.Errorf("リンク先が書き換わった: %q", b)
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestCheckNoComposeOverrides(t *testing.T) {
	if err := (&envFile{lines: []string{"A=1", "# COMPOSE_FILE=x"}}).checkNoComposeOverrides(); err != nil {
		t.Errorf("コメントは許す: %v", err)
	}
	for _, k := range composeOverrideKeys {
		if err := (&envFile{lines: []string{k + "=x"}}).checkNoComposeOverrides(); err == nil {
			t.Errorf("%s を拒否していない", k)
		}
	}
}

func TestCleanEnv(t *testing.T) {
	t.Setenv("COMPOSE_FILE", "/evil.yaml")
	t.Setenv("COMPOSE_PROJECT_NAME", "other")
	t.Setenv("COMPOSE_PATH_SEPARATOR", ";")
	t.Setenv("SAIL_FILES", "x")
	t.Setenv("APP_PORT", "9999")
	t.Setenv("KEEP_ME", "1")
	got := strings.Join(cleanEnv([]string{"APP_PORT"}), "\n")
	for _, k := range []string{"COMPOSE_FILE=", "COMPOSE_PROJECT_NAME=", "COMPOSE_PATH_SEPARATOR=", "SAIL_FILES=", "APP_PORT="} {
		if strings.Contains(got, k) {
			t.Errorf("%s が残っている", k)
		}
	}
	if !strings.Contains(got, "KEEP_ME=1") {
		t.Error("無関係な変数まで外れた")
	}
}

func TestPortFreeDetectsLoopbackOnly(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Errorf("127.0.0.1 だけに束縛されたポート %d を空きと判定した", port)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// メインと worktree を作って worktree に移動する。
// 実ユーザーのレジストリ・設定を読み書きしないよう、HOME と XDG_CONFIG_HOME を一時ディレクトリへ向ける。
func setupWorktreeRepo(t *testing.T) (main, wt string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main = filepath.Join(base, "app")
	wt = filepath.Join(base, "app-feat")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-q", "-b", "main")
	cfg := `{"compose":"compose.yaml","port_vars":[{"name":"APP_PORT","default":80}]}`
	if err := os.WriteFile(filepath.Join(main, configName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "add", ".")
	runGit(t, main, "commit", "-q", "-m", "init")
	runGit(t, main, "worktree", "add", "-q", wt, "-b", "feat")
	t.Chdir(wt)
	return main, wt
}

func TestRmRefusesMismatchedProjectName(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	env := "COMPOSE_PROJECT_NAME=other-project\n"
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdRm([]string{"-y"})
	if err == nil || !strings.Contains(err.Error(), "一致しません") {
		t.Errorf("名前の不一致を拒否していない: %v", err)
	}
}

func TestRmAndUpRefuseComposeOverrides(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt)
	env := "COMPOSE_PROJECT_NAME=" + proj + "\nCOMPOSE_FILE=/evil.yaml\n"
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdRm([]string{"-y"}); err == nil || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("rm が COMPOSE_FILE を拒否していない: %v", err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("up が COMPOSE_FILE を拒否していない: %v", err)
	}
}

func TestUpRefusesSymlinkEnv(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	target := filepath.Join(main, ".env")
	if err := os.WriteFile(target, []byte("APP_URL=http://localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(wt, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := cmdUp(nil); err == nil || !strings.Contains(err.Error(), "シンボリックリンク") {
		t.Errorf("up がシンボリックリンクを拒否していない: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "APP_URL=http://localhost\n" {
		t.Errorf("メインの .env が書き換わった: %q", b)
	}
}

func TestProjectNameIsStableAcrossSymlinkedPaths(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	link := filepath.Join(filepath.Dir(wt), "via-link")
	if err := os.Symlink(wt, link); err != nil {
		t.Fatal(err)
	}
	root, err := worktreeRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	if root != wt {
		t.Errorf("実パスにそろっていない: %q != %q", root, wt)
	}
	if projectName(main, root) != projectName(main, wt) {
		t.Error("呼び出し経路でプロジェクト名が変わる")
	}
}

func TestKeyOfHandlesExportWithTab(t *testing.T) {
	for _, l := range []string{"export COMPOSE_FILE=x", "export\tCOMPOSE_FILE=x", "  export   COMPOSE_FILE = x"} {
		if k, ok := keyOf(l); !ok || k != "COMPOSE_FILE" {
			t.Errorf("%q: key=%q ok=%v", l, k, ok)
		}
	}
	if k, _ := keyOf("exported=1"); k != "exported" {
		t.Errorf("export で始まるだけのキー: %q", k)
	}
}

func TestLoadConfigRejectsUnsafeCompose(t *testing.T) {
	for _, c := range []string{"", "/etc/compose.yaml", "../compose.yaml", "a/../../compose.yaml", "."} {
		dir := t.TempDir()
		b := `{"compose":` + strconvQuote(c) + `,"port_vars":[]}`
		if err := os.WriteFile(filepath.Join(dir, configName), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(dir); err == nil {
			t.Errorf("compose=%q を拒否していない", c)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configName), []byte(`{"compose":"docker/compose.yaml","port_vars":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir); err != nil {
		t.Errorf("相対パスは許す: %v", err)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type call struct {
	dir  string
	env  []string
	name string
	args []string
}

// runner を差し替えて呼び出しを記録する (グローバル変数・t.Setenv と t.Chdir も使うので、並列にはできない)。
func captureRunner(t *testing.T) *[]call {
	t.Helper()
	var calls []call
	old := runner
	runner = func(dir string, env []string, name string, args ...string) error {
		calls = append(calls, call{dir, env, name, args})
		return nil
	}
	t.Cleanup(func() { runner = old })
	return &calls
}

func TestRmPassesPinnedArguments(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	proj := projectName(main, wt)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_PROJECT_NAME="+proj+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPOSE_FILE", "/evil.yaml")
	calls := captureRunner(t)
	if err := cmdRm([]string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("呼び出し数=%d", len(*calls))
	}
	c := (*calls)[0]
	want := strings.Join(rmArgs(proj, wt, filepath.Join(wt, "compose.yaml")), " ")
	if c.name != "docker" || strings.Join(c.args, " ") != want {
		t.Errorf("引数=%v", c.args)
	}
	for _, f := range []string{"--project-name " + proj, "--project-directory " + wt, "-f " + filepath.Join(wt, "compose.yaml")} {
		if !strings.Contains(want, f) {
			t.Errorf("%q が固定されていない: %s", f, want)
		}
	}
	if strings.Contains(strings.Join(c.env, "\n"), "COMPOSE_FILE=") {
		t.Error("COMPOSE_FILE が環境に残っている")
	}
}

func TestUpWritesEnvAndCleansSailEnvironment(t *testing.T) {
	main, wt := setupWorktreeRepo(t)
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("APP_URL=http://localhost\nSESSION_COOKIE=old\nSESSION_COOKIE=older\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "vendor", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "vendor", "bin", "sail"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PORT", "9999")
	t.Setenv("COMPOSE_PROFILES", "x")
	calls := captureRunner(t)
	if err := cmdUp([]string{"-d"}); err != nil {
		t.Fatal(err)
	}
	e, err := readEnv(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	proj := projectName(main, wt)
	if v, _ := e.Get("COMPOSE_PROJECT_NAME"); v != proj {
		t.Errorf("COMPOSE_PROJECT_NAME=%q", v)
	}
	if v, _ := e.Get("SESSION_COOKIE"); v != proj+"-session" {
		t.Errorf("SESSION_COOKIE=%q", v)
	}
	n := 0
	for _, l := range e.lines {
		if k, _ := keyOf(l); k == "SESSION_COOKIE" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("SESSION_COOKIE の行数=%d", n)
	}
	if port, _ := e.Get("APP_PORT"); port == "" || port == "80" || port == "9999" {
		t.Errorf("APP_PORT=%q", port)
	}
	if u, _ := e.Get("APP_URL"); !strings.HasPrefix(u, "http://localhost:") {
		t.Errorf("APP_URL=%q", u)
	}
	if fi, _ := os.Stat(filepath.Join(wt, ".env")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf(".env のモード=%v", fi.Mode().Perm())
	}
	if len(*calls) != 1 || (*calls)[0].args[0] != "up" || (*calls)[0].args[1] != "-d" {
		t.Fatalf("sail の呼び出し=%v", *calls)
	}
	env := strings.Join((*calls)[0].env, "\n")
	if strings.Contains(env, "APP_PORT=") || strings.Contains(env, "COMPOSE_PROFILES=") {
		t.Error("ポート変数・COMPOSE_* が sail の環境に残っている")
	}
}

func TestStopRefusesComposeOverrides(t *testing.T) {
	_, wt := setupWorktreeRepo(t)
	if err := os.WriteFile(filepath.Join(wt, ".env"), []byte("COMPOSE_FILE=/evil.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := captureRunner(t)
	if err := cmdStop(nil); err == nil || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("stop が COMPOSE_FILE を拒否していない: %v", err)
	}
	if len(*calls) != 0 {
		t.Error("拒否したのに実行した")
	}
}
