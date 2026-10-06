package main

import (
	"bufio"
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
	t = strings.TrimPrefix(t, "export ")
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

func (e *envFile) Set(key, value string) {
	for i, l := range e.lines {
		if k, ok := keyOf(l); ok && k == key {
			e.lines[i] = key + "=" + value
			return
		}
	}
	e.lines = append(e.lines, key+"="+value)
}

func (e *envFile) Write(path string) error {
	s := strings.Join(e.lines, "\n")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return os.WriteFile(path, []byte(s), 0o644)
}
