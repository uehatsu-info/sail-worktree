//go:build !unix

package main

import "os"

// unix 以外ではハードリンク数を調べない。
func hasMultipleLinks(os.FileInfo) bool { return false }
