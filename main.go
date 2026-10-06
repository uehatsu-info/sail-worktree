package main

import (
	"fmt"
	"os"
)

const usage = `sail-worktree - Laravel Sail 用 git worktree ポート割り当てツール

使い方:
  sail-worktree init            対象プロジェクトに .sail-worktree.json を生成
  sail-worktree up [args...]    .env を生成/修正して sail up を実行 (例: up -d)
  sail-worktree stop            sail stop を実行
  sail-worktree rm [-y]         コンテナ/ネットワーク/ボリューム/ビルドイメージを削除し、ポート割り当てを解放
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "init":
		err = cmdInit()
	case "up":
		err = cmdUp(args)
	case "stop":
		err = cmdStop(args)
	case "rm":
		err = cmdRm(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "不明なコマンド: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}
