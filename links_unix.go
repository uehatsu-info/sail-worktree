//go:build unix

package main

import (
	"os"
	"syscall"
)

func hasMultipleLinks(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
