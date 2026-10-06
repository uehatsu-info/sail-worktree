//go:build !unix

package main

import "os"

// Outside unix there is neither O_NOFOLLOW nor a hard link count check (only the Lstat check).
const openNoFollow = 0

// O_NONBLOCK is not available either; a FIFO is rejected by the Lstat and by the Stat after opening.
const openNonBlock = 0

func hasMultipleLinks(os.FileInfo) bool { return false }
