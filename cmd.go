package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// runner は外部コマンドの実行。テストで差し替える。
var runner = runCmdEnv

func cmdUp(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	if c.root == c.main {
		return fmt.Errorf("メインワークツリーでは実行できません。作成済みのワークツリー上で実行してください")
	}
	envPath := filepath.Join(c.root, ".env")
	if err := checkOwnEnv(envPath); err != nil {
		return err
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	reg.migrate(c.root) // allocatePorts の前に、旧版の別名のキーを自分の割り当てとして取り込む (保存は up 成功時)
	src := envOwn
	env, err := readEnv(envPath)
	if os.IsNotExist(err) {
		src = envFromMain
		env, err = readEnv(filepath.Join(c.main, ".env"))
		if os.IsNotExist(err) {
			src = envFromMainExample
			env, err = readEnv(filepath.Join(c.main, ".env.example"))
		}
		if err != nil {
			return fmt.Errorf("元になる .env をメインワークツリーから読めません: %w", err)
		}
		fmt.Println(".env を新規作成します (メインワークツリーの .env をコピー)")
	} else if err != nil {
		return err
	}

	if k, ok := env.overrideKey(upOverrideKeys); ok {
		return upOverrideError(k, src)
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

// envSource は up が .env の元にした場所。拒否エラーの案内を変えるために覚えておく。
type envSource int

const (
	envOwn             envSource = iota // このワークツリー自身の .env
	envFromMain                         // メインワークツリーの .env (コピー)
	envFromMainExample                  // メインワークツリーの .env.example (コピー)
)

func upOverrideError(key string, src envSource) error {
	switch src {
	case envOwn:
		return fmt.Errorf(".env に %s があるため up できません (別の compose ファイルを指し得るため)。.env からその行を消してください", key)
	case envFromMain:
		return fmt.Errorf("メインワークツリーの .env に %s があり、それをコピーした .env にも入るため up できません (別の compose ファイルを指し得るため)。"+
			"メインの .env から消す (他のワークツリーの元にも影響します) か、このワークツリーに .env を先に作って、その行を入れずに up してください", key)
	case envFromMainExample:
		return fmt.Errorf("メインワークツリーの .env.example に %s があり、それをコピーした .env にも入るため up できません (別の compose ファイルを指し得るため)。"+
			".env.example から消すか、このワークツリーに .env を先に作って、その行を入れずに up してください", key)
	}
	return fmt.Errorf("不明な .env の出所: %d", src)
}

func cmdStop(args []string) error {
	c, err := loadCtx()
	if err != nil {
		return err
	}
	// stop は .env を書き換えず、止めても取り返しがつくので、up・rm のような拒否はしない。
	// 別のプロジェクトを止め得る場合だけ警告する。
	if c.root != c.main {
		warnStopTarget(c)
	}
	return runSail(c.root, c.cfg, append([]string{"stop"}, args...))
}

// stdin と stderr は確認プロンプトの入力と警告の出力先 (テストで差し替える)。
var (
	stdin  io.Reader = os.Stdin
	stderr io.Writer = os.Stderr
)

// safeProjectName は復旧コマンドに埋め込んでよい名前。.env の値は信頼できないので、
// シェルや docker のオプションとして解釈されない文字種だけを許す (先頭は英数字、長さ上限あり)。
var safeProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// nameMismatchError は rm が拒否したときのエラー。1 行目に主軸の手順 (.env を本来の名前に直す) を置く。
// .env の名前 (got) は信頼できない入力なので %+q で表示し、復旧コマンドには安全な文字種だけの got を埋め込む。
// このワークツリーの名前 (want) は projectName が安全な文字種で作るが、念のため同じ検証で表示を分ける。
func nameMismatchError(got, want string) error {
	shown := fmt.Sprintf("%+q", want)
	if safeProjectName.MatchString(want) {
		shown = want // 引用符なし (そのまま .env に書ける)
	}
	msg := fmt.Sprintf(".env の COMPOSE_PROJECT_NAME を %s に直して、もう一度 rm を実行してください (現在の %+q はこのワークツリーの名前と一致しないため rm できません)。", shown, got)
	if safeProjectName.MatchString(got) {
		msg += fmt.Sprintf("\n古い版が別の名前で作ったプロジェクトを消す場合だけ、次の順に行ってください。"+
			"\n  1. `docker compose ls -a` で、%s が他のワークツリーやプロジェクトのものでなく、このワークツリーのものであることを確認する。"+
			"\n  2. シェルに COMPOSE_* の環境変数があると対象が変わるので、`env | grep '^COMPOSE_'` で確認し、表示された変数を全て unset する。"+
			"\n  3. 次を実行する (-v でボリューム=DB データも消え、取り返しがつきません):"+
			"\n      docker compose -p %s down -v --rmi local --remove-orphans", got, got)
	} else {
		msg += "\n.env の名前は小文字英数字・_・- だけでない (大文字などは compose が使う名前と異なり得る) ため、手動で消すコマンドは示しません。`docker compose ls -a` で対象を確認してください。"
	}
	return fmt.Errorf("%s", msg)
}

// warnStopTarget は .env が別の compose ファイル・プロジェクトを指していそうなとき、stop の前に警告する。
// ベストエフォートで、シェル式による上書き等は検出できない。.env が通常ファイルでなければ何もしない。
func warnStopTarget(c *ctx) {
	env, ok := readEnvIfRegular(filepath.Join(c.root, ".env"))
	if !ok {
		return
	}
	if k, ok := env.overrideKey(upOverrideKeys); ok {
		fmt.Fprintf(stderr, "警告: .env に %s があり、このワークツリー以外の compose プロジェクトを止める可能性があります\n", k)
	}
	if name, ok := env.Get("COMPOSE_PROJECT_NAME"); ok {
		if want := projectName(c.main, c.root); name != want {
			fmt.Fprintf(stderr, "警告: .env の COMPOSE_PROJECT_NAME (%+q) がこのワークツリーの名前 (%+q) と異なり、別のプロジェクトを止める可能性があります\n", name, want)
		}
	}
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
	if k, ok := env.overrideKey(rmOverrideKeys); ok {
		return fmt.Errorf(".env に %s があるため rm できません (別の compose ファイル・サービスを指し得るため)。rm の前に .env からその行を消してください", k)
	}
	proj, ok := env.Get("COMPOSE_PROJECT_NAME")
	if !ok || proj == "" {
		return fmt.Errorf(".env に COMPOSE_PROJECT_NAME がありません")
	}
	// 取り返しのつかない down -v なので、.env の値を信用せず、このワークツリーの名前を再計算して完全一致を要求する
	// (別のプロジェクト・別のワークツリーの名前が残っている、手で書き換えた、ワークツリーを移動した、を拒否する)。
	if want := projectName(c.main, c.root); proj != want {
		return nameMismatchError(proj, want)
	}
	// 拒否の検査は全て確認プロンプトの前に済ませる (y と答えた後に拒否しない)。
	composePath := filepath.Join(c.root, c.cfg.Compose)
	if fi, err := os.Stat(composePath); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("compose ファイルが見つかりません: %s", composePath)
	}
	// ここで読むのは壊れたレジストリを消す前に検出するため (値は使わない)。解放用には docker の後に読み直す。消さないこと。
	if _, err := loadRegistry(); err != nil {
		return err
	}
	if !yes {
		fmt.Printf("プロジェクト %q のコンテナ・ネットワーク・ボリューム(DBデータ含む)・ビルドイメージを削除します。よろしいですか? [y/N] ", proj)
		ans, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			fmt.Println("中止しました")
			return nil
		}
	}
	// プロジェクト名・ディレクトリ・compose ファイルを明示し、環境の COMPOSE_* を外して実行する。
	if err := runner(c.root, cleanEnv(nil), "docker", rmArgs(proj, c.root, composePath)...); err != nil {
		return err
	}
	// docker の実行中に別の up がレジストリを更新していても失わないよう、消した後に読み直して解放する。
	reg, err := loadRegistry()
	if err != nil {
		return fmt.Errorf("docker の削除は完了しましたが、ポート割り当ての記録を読めません: %w", err)
	}
	reg.migrate(c.root)
	delete(reg.Worktrees, c.root)
	if err := reg.save(); err != nil {
		return err
	}
	fmt.Println("ポート割り当てを解放しました")
	return nil
}

// rmArgs は rm が docker に渡す引数。プロジェクト名・ディレクトリ・compose ファイルを全て明示する。
func rmArgs(proj, root, composePath string) []string {
	return []string{"compose", "--project-name", proj, "--project-directory", root,
		"-f", composePath, "down", "-v", "--rmi", "local", "--remove-orphans"}
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
	return runner(root, cleanEnv(drop), sail, args...)
}

// cleanEnv は現在の環境から、COMPOSE_ で始まる全ての変数・SAIL_FILES・drop の変数を外した環境を返す
// (別の compose ファイル・プロジェクトを指し得るため)。DOCKER_HOST 等は意図して使う利用者がいるので外さない。
// これは環境変数の除去で、.env のキーを拒否する upOverrideKeys / rmOverrideKeys とは別の仕組み。
func cleanEnv(drop []string) []string {
	skip := map[string]bool{"SAIL_FILES": true}
	for _, k := range drop {
		skip[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !skip[k] && !strings.HasPrefix(k, "COMPOSE_") {
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
