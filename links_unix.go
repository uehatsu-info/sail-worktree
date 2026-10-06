//go:build unix

package main

import (
	"os"
	"syscall"
)

// openNoFollow is open(2)'s O_NOFOLLOW (the open fails if the last component is a symbolic link).
const openNoFollow = syscall.O_NOFOLLOW

// openNonBlock is open(2)'s O_NONBLOCK (opening a FIFO does not block).
const openNonBlock = syscall.O_NONBLOCK

func hasMultipleLinks(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
