//go:build !unix

package main

import "os"

// unix 以外では O_NOFOLLOW もハードリンク数の検査もない (Lstat の検査だけ)。
const openNoFollow = 0

const openNonBlock = 0

func hasMultipleLinks(os.FileInfo) bool { return false }
