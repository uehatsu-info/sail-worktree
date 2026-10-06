//go:build unix

package main

import (
	"os"
	"syscall"
)

// openNoFollow は open(2) の O_NOFOLLOW (末尾がシンボリックリンクなら失敗する)。
const openNoFollow = syscall.O_NOFOLLOW

// openNonBlock は open(2) の O_NONBLOCK (FIFO の open でブロックしない)。
const openNonBlock = syscall.O_NONBLOCK

func hasMultipleLinks(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
