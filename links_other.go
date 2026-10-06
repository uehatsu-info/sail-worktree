//go:build !unix

package main

import "os"

// Outside unix there is neither O_NOFOLLOW nor a hard link count check (only the Lstat check).
const openNoFollow = 0

// openNonBlock is not used either (a FIFO is rejected by Lstat and by the Stat after opening).
const openNonBlock = 0

func hasMultipleLinks(os.FileInfo) bool { return false }
