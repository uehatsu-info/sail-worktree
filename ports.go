package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
)

// portFree は p が全インターフェースと 127.0.0.1 の両方で空いているか確かめる
// (macOS は SO_REUSEADDR により、127.0.0.1 だけに束縛した他プロセスがいても ":p" の束縛に成功し得るため)。
// 全インターフェースの束縛が失敗した (権限エラーを含む) ときは常に「塞がり」とする。
// 127.0.0.1 の束縛の失敗の扱いは loopbackBindBlocked を見ること。
func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	l.Close()
	l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return !loopbackBindBlocked(err, portFreeGOOS)
	}
	l.Close()
	return true
}

// portFreeGOOS は portFree が見る OS (テストで差し替える)。
var portFreeGOOS = runtime.GOOS

// loopbackBindBlocked は 127.0.0.1 への束縛が err で失敗したとき、ポートが塞がっているとみなすか。
//   - 権限エラー: macOS は特権ポート (1024 未満) の 127.0.0.1 への束縛を一般ユーザーに許さないが、
//     Docker は束縛できるので「塞がり」とみなさない。他の OS では塞がりとみなす (Windows の権限エラーは
//     他のプロセスが排他的に使っていることがあるため)。
//   - EADDRNOTAVAIL: 127.0.0.1 が無い環境。ここでは使用中かどうか分からないので、塞がりとみなさない。
//   - それ以外 (EADDRINUSE 等): 塞がり。
func loopbackBindBlocked(err error, goos string) bool {
	switch {
	case errors.Is(err, os.ErrPermission):
		return goos != "darwin"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return false
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
