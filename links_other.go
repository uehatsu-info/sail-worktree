//go:build !unix

package main

import "os"

// unix 以外では O_NOFOLLOW もハードリンク数の検査もない (Lstat の検査だけ)。
const openNoFollow = 0

// openNonBlock も同様に使わない (FIFO は Lstat と開いた後の Stat で弾く)。
const openNonBlock = 0

func hasMultipleLinks(os.FileInfo) bool { return false }
