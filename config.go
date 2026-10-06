package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	return &c, nil
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
