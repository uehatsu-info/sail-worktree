package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func cwd() (string, error) { return os.Getwd() }

func cmdInit() error {
	dir, err := cwd()
	if err != nil {
		return err
	}
	root, err := worktreeRoot(dir)
	if err != nil {
		return fmt.Errorf("git リポジトリ内で実行してください: %w", err)
	}
	compose, err := findCompose(root)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, compose))
	if err != nil {
		return err
	}
	vars := detectPortVars(string(b))
	if len(vars) == 0 {
		return fmt.Errorf("%s にポート変数 (${XXX_PORT:-1234}) が見つかりません", compose)
	}
	data, _ := json.MarshalIndent(Config{Compose: compose, PortVars: vars}, "", "  ")
	path := filepath.Join(root, configName)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s を作成しました (コミットして全ワークツリーで共有してください)\n", path)
	for _, v := range vars {
		fmt.Printf("  %s (デフォルト %d)\n", v.Name, v.Default)
	}
	return nil
}

// ctx は worktree 内で実行するコマンド共通の前提情報。
type ctx struct {
	root, main string
	cfg        *Config
}

func loadCtx() (*ctx, error) {
	dir, err := cwd()
	if err != nil {
		return nil, err
	}
	root, err := worktreeRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("git リポジトリ内で実行してください: %w", err)
	}
	main, err := mainWorktree(root)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(root)
	if err != nil {
		return nil, err
	}
	return &ctx{root: filepath.Clean(root), main: main, cfg: cfg}, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

func projectName(main, root string) string {
	sum := sha1.Sum([]byte(root))
	slug := nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(main)+"-"+filepath.Base(root)), "-")
	return strings.Trim(slug, "-_") + "-" + hex.EncodeToString(sum[:])[:6]
}

// sessionCookieName はワークツリーごとのセッション Cookie 名。localhost はポートが違っても Cookie を共有するので、
// プロジェクト名から作って他のワークツリー・プロジェクトとログインが混ざらないようにする。
func sessionCookieName(proj string) string { return proj + "-session" }

func cmdUp(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	if c.root == c.main {
		return fmt.Errorf("メインワークツリーでは実行できません。作成済みのワークツリー上で実行してください")
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	envPath := filepath.Join(c.root, ".env")
	if err := checkOwnEnv(envPath); err != nil {
		return err
	}
	env, err := readEnv(envPath)
	if os.IsNotExist(err) {
		env, err = readEnv(filepath.Join(c.main, ".env"))
		if os.IsNotExist(err) {
			env, err = readEnv(filepath.Join(c.main, ".env.example"))
		}
		if err != nil {
			return fmt.Errorf("元になる .env をメインワークツリーから読めません: %w", err)
		}
		fmt.Println(".env を新規作成します (メインワークツリーの .env をコピー)")
	} else if err != nil {
		return err
	}

	if err := env.checkNoComposeOverrides(); err != nil {
		return err
	}
	ports, err := allocatePorts(c.cfg.PortVars, reg.Worktrees[c.root], reg.used(c.root), portFree)
	if err != nil {
		return err
	}
	for _, v := range c.cfg.PortVars {
		env.Set(v.Name, strconv.Itoa(ports[v.Name]))
	}
	proj := projectName(c.main, c.root)
	env.Set("COMPOSE_PROJECT_NAME", proj)
	env.Set("SESSION_COOKIE", sessionCookieName(proj))
	if appURL, ok := env.Get("APP_URL"); ok {
		if u, err := url.Parse(appURL); err == nil && u.Hostname() != "" && ports["APP_PORT"] != 0 {
			u.Host = u.Hostname() + ":" + strconv.Itoa(ports["APP_PORT"])
			env.Set("APP_URL", u.String())
		}
	}
	if err := env.Write(envPath); err != nil {
		return err
	}
	reg.Worktrees[c.root] = ports
	if err := reg.save(); err != nil {
		return err
	}
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %s=%d\n", n, ports[n])
	}
	return runSail(c.root, c.cfg, append([]string{"up"}, args...))
}

func cmdStop(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	return runSail(c.root, c.cfg, append([]string{"stop"}, args...))
}

func cmdRm(args []string) error {
	yes := false
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			yes = true
		} else {
			return fmt.Errorf("不明な引数: %s", a)
		}
	}
	c, err := loadCtx()
	if err != nil {
		return err
	}
	if c.root == c.main {
		return fmt.Errorf("メインワークツリーでは実行できません")
	}
	envPath := filepath.Join(c.root, ".env")
	if err := checkOwnEnv(envPath); err != nil {
		return err
	}
	env, err := readEnv(envPath)
	if err != nil {
		return fmt.Errorf(".env を読めません (up 済みのワークツリーで実行してください): %w", err)
	}
	if err := env.checkNoComposeOverrides(); err != nil {
		return err
	}
	proj, ok := env.Get("COMPOSE_PROJECT_NAME")
	if !ok || proj == "" {
		return fmt.Errorf(".env に COMPOSE_PROJECT_NAME がありません")
	}
	// 取り返しのつかない down -v なので、.env の値を信用せず、このワークツリーの名前を再計算して完全一致を要求する
	// (別のプロジェクト・別のワークツリーの名前が残っている、手で書き換えた、ワークツリーを移動した、を拒否する)。
	if want := projectName(c.main, c.root); proj != want {
		return fmt.Errorf(".env の COMPOSE_PROJECT_NAME (%q) がこのワークツリーの名前 (%q) と一致しません。消しません", proj, want)
	}
	if !yes {
		fmt.Printf("プロジェクト %q のコンテナ・ネットワーク・ボリューム(DBデータ含む)・ビルドイメージを削除します。よろしいですか? [y/N] ", proj)
		ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Println("中止しました")
			return nil
		}
	}
	// プロジェクト名・ディレクトリ・compose ファイルを明示し、環境の COMPOSE_* を外して実行する。
	err = runCmdEnv(c.root, cleanEnv(nil), "docker", "compose", "--project-name", proj, "--project-directory", c.root,
		"-f", filepath.Join(c.root, c.cfg.Compose), "down", "-v", "--rmi", "local", "--remove-orphans")
	if err != nil {
		return err
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	delete(reg.Worktrees, c.root)
	if err := reg.save(); err != nil {
		return err
	}
	fmt.Println("ポート割り当てを解放しました")
	return nil
}

func runSail(root string, cfg *Config, args []string) error {
	sail := filepath.Join(root, "vendor", "bin", "sail")
	if _, err := os.Stat(sail); err != nil {
		return fmt.Errorf("%s がありません。`composer install` を実行してください", sail)
	}
	// シェルのポート変数は .env より優先されるので、割り当てたポートとずれないよう外す。
	drop := make([]string, 0, len(cfg.PortVars))
	for _, v := range cfg.PortVars {
		drop = append(drop, v.Name)
	}
	return runCmdEnv(root, cleanEnv(drop), sail, args...)
}

// cleanEnv は現在の環境から、別の compose ファイル・プロジェクトを指し得る変数と drop の変数を外した環境を返す。
func cleanEnv(drop []string) []string {
	skip := map[string]bool{"COMPOSE_PROJECT_NAME": true}
	for _, k := range composeOverrideKeys {
		skip[k] = true
	}
	for _, k := range drop {
		skip[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !skip[k] {
			out = append(out, kv)
		}
	}
	return out
}

func runCmd(dir, name string, args ...string) error {
	return runCmdEnv(dir, nil, name, args...)
}

func runCmdEnv(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
