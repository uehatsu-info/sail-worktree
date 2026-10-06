package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const configName = ".sail-worktree.json"

// Config はプロジェクト直下に置く設定。リポジトリにコミットして全ワークツリーで共有する。
type Config struct {
	Compose  string    `json:"compose"`
	PortVars []PortVar `json:"port_vars"`
}

func loadConfig(root string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(root, configName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s がありません。先に `sail-worktree init` を実行してください", configName)
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s の解析に失敗: %w", configName, err)
	}
	if unsafeComposePath(c.Compose) {
		return nil, fmt.Errorf("%s の compose (%q) はワークツリー内の相対パスにしてください", configName, c.Compose)
	}
	return &c, nil
}

// unsafeComposePath は compose の値がワークツリー内の相対パスでないとき true を返す。rm の -f に渡るので、
// 空・絶対パス・.. を含むものに加えて、Windows でドライブやサーバーを指す形 ("C:x"、"\\srv\x") と、
// ドライブ文字の無いルート指定 ("/x"、"\x": filepath.IsAbs は false になる) も拒否する。
func unsafeComposePath(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return true
	}
	cl := filepath.Clean(p)
	return cl == "." || cl == ".." || strings.HasPrefix(cl, ".."+string(filepath.Separator))
}

func findCompose(root string) (string, error) {
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		if _, err := os.Stat(filepath.Join(root, n)); err == nil {
			return n, nil
		}
	}
	return "", fmt.Errorf("compose.yml が見つかりません: %s", root)
}

// Registry はワークツリーごとに割り当て済みのポートを記録する (ユーザー全体で共有)。
type Registry struct {
	Worktrees map[string]map[string]int `json:"worktrees"`
}

func registryPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "sail-worktree", "registry.json"), nil
}

func loadRegistry() (*Registry, error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	r := &Registry{Worktrees: map[string]map[string]int{}}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("%s の解析に失敗: %w", p, err)
	}
	if r.Worktrees == nil {
		r.Worktrees = map[string]map[string]int{}
	}
	return r, nil
}

func (r *Registry) save() error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// migrate は、root と同じ実体を指す別名のキー (旧版がシンボリックリンク経由のパスで記録したもの) を
// root に統合する。メモリ上だけの操作で、保存は呼び出し側が成功時に行う。root に既存のエントリがあればそちらを優先し、
// 無ければ別名のうち辞書順で最小のキーのエントリを採る (別名のポートは used に残らない)。
// realPath にできない (消えた) パスのキーは触らない。
func (r *Registry) migrate(root string) {
	var aliases []string
	for k := range r.Worktrees {
		if k == root {
			continue
		}
		if rp, err := realPath(k); err == nil && rp == root {
			aliases = append(aliases, k)
		}
	}
	sort.Strings(aliases)
	for _, k := range aliases {
		if _, ok := r.Worktrees[root]; !ok {
			r.Worktrees[root] = r.Worktrees[k]
		}
		delete(r.Worktrees, k)
	}
}

// used は other 以外のワークツリーに割り当て済みのポート集合を返す。
func (r *Registry) used(except string) map[int]bool {
	m := map[int]bool{}
	for wt, ports := range r.Worktrees {
		if wt == except {
			continue
		}
		for _, p := range ports {
			m[p] = true
		}
	}
	return m
}
