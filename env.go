package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var composePortRe = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*PORT):-?(\d+)\}`)

// PortVar は compose.yml から検出したポート用の環境変数。
type PortVar struct {
	Name    string `json:"name"`
	Default int    `json:"default"`
}

// detectPortVars は compose.yml の ports 指定 (- '${APP_PORT:-80}:80' 等) から
// ポート変数を出現順に検出する。
func detectPortVars(composeYAML string) []PortVar {
	var vars []PortVar
	seen := map[string]bool{}
	for _, line := range strings.Split(composeYAML, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "-") || strings.HasPrefix(t, "#") {
			continue
		}
		for _, m := range composePortRe.FindAllStringSubmatch(t, -1) {
			if seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			var d int
			for _, c := range m[2] {
				d = d*10 + int(c-'0')
			}
			vars = append(vars, PortVar{Name: m[1], Default: d})
		}
	}
	return vars
}

// envFile は行構造を保ったまま KEY=VALUE を読み書きする .env の表現。
type envFile struct {
	lines []string
}

func readEnv(path string) (*envFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	e := &envFile{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		e.lines = append(e.lines, sc.Text())
	}
	return e, sc.Err()
}

func keyOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	if rest, ok := strings.CutPrefix(t, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		t = strings.TrimSpace(rest)
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(k), true
}

func (e *envFile) Get(key string) (string, bool) {
	for _, l := range e.lines {
		if k, ok := keyOf(l); ok && k == key {
			_, v, _ := strings.Cut(l, "=")
			return strings.Trim(strings.TrimSpace(v), `"'`), true
		}
	}
	return "", false
}

// Set は同じキーの最初の行を置き換え、重複する後ろの行は取り除く (Sail は最後の値・Laravel の Dotenv は
// 最初の値を使うので、重複行が残ると両者の値が割れる)。無ければ追記する。
func (e *envFile) Set(key, value string) {
	out := e.lines[:0:0]
	found := false
	for _, l := range e.lines {
		if k, ok := keyOf(l); ok && k == key {
			if found {
				continue
			}
			found = true
			l = key + "=" + value
		}
		out = append(out, l)
	}
	if !found {
		out = append(out, key+"="+value)
	}
	e.lines = out
}

// Write は .env を書き出す。書き込み前に checkOwnEnv を通す。
// 新規作成は 0600 (APP_KEY・DB_PASSWORD を含むため)。既存ファイルのモードは変えない。
func (e *envFile) Write(path string) error {
	if err := checkOwnEnv(path); err != nil {
		return err
	}
	s := strings.Join(e.lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	// O_TRUNC は検査の前に切り詰めてしまうので、開いた後に検査してから Truncate する。
	// O_NOFOLLOW (unix) で、Lstat から open までの間にシンボリックリンクへ差し替えられても辿らない。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || hasMultipleLinks(fi)) {
		err = fmt.Errorf("%s は通常のファイルでないか、ほかのファイルとハードリンクされています", path)
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteString(s)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkOwnEnv はワークツリー自身の .env を読み書きしてよいか確かめる。
// シンボリックリンク (リンク先のメイン等の .env を書き換えてしまう) とハードリンクを拒否する。
// ファイルが無いのは問題ない (新規作成)。
func checkOwnEnv(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s がシンボリックリンクです。リンク先を書き換えないよう、実ファイルにしてください", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s が通常のファイルではありません", path)
	}
	if hasMultipleLinks(fi) {
		return fmt.Errorf("%s がほかのファイルとハードリンクされています (メインの .env 等を書き換えないよう、実ファイルにしてください)", path)
	}
	return nil
}

// composeOverrideKeys は .env に書くと別の compose ファイル・プロジェクトを指し得るキー。
var composeOverrideKeys = []string{"COMPOSE_FILE", "COMPOSE_PROFILES", "COMPOSE_ENV_FILES", "SAIL_FILES"}

// checkNoComposeOverrides は .env に composeOverrideKeys のいずれかがあれば拒否する。
func (e *envFile) checkNoComposeOverrides() error {
	for _, k := range composeOverrideKeys {
		if _, ok := e.Get(k); ok {
			return fmt.Errorf(".env に %s は書けません (別の compose ファイルを指し得るため)", k)
		}
	}
	return nil
}
