package main

import (
	"fmt"
	"net"
)

// portFree は p が全インターフェースと 127.0.0.1 の両方で空いているか確かめる
// (macOS は SO_REUSEADDR により、127.0.0.1 だけに束縛した他プロセスがいても ":p" の束縛に成功し得るため)。
func portFree(p int) bool {
	for _, addr := range []string{fmt.Sprintf(":%d", p), fmt.Sprintf("127.0.0.1:%d", p)} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// allocatePorts は各ポート変数に衝突しないポートを割り当てる。
// 既存割り当て(current)があればそれを優先して再利用する(自身のコンテナが
// 使用中でも維持するため空き確認はしない)。
// 新規は default+1 から順に、他ワークツリーの割当・今回の割当・ホスト上の使用中を避けて探す。
// default 自体はメインワークツリー用に空ける。
func allocatePorts(vars []PortVar, current map[string]int, taken map[int]bool, free func(int) bool) (map[string]int, error) {
	out := map[string]int{}
	inThis := map[int]bool{}
	for _, v := range vars {
		if p, ok := current[v.Name]; ok && !taken[p] && !inThis[p] {
			out[v.Name] = p
			inThis[p] = true
		}
	}
	for _, v := range vars {
		if _, ok := out[v.Name]; ok {
			continue
		}
		found := false
		for p := v.Default + 1; p <= 65535; p++ {
			if taken[p] || inThis[p] || !free(p) {
				continue
			}
			out[v.Name] = p
			inThis[p] = true
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("%s に割り当て可能なポートがありません", v.Name)
		}
	}
	return out, nil
}
