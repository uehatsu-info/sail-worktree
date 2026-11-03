//go:build !unix

package main

import "os"

// Outside unix there is neither O_NOFOLLOW nor a hard link count check (only the Lstat check).
const openNoFollow = 0

// O_NONBLOCK is not available either; a FIFO is rejected by the Lstat or Stat before opening and by the Stat after.
const openNonBlock = 0

// Nor is O_NOCTTY (there is no controlling terminal to acquire).
const openNoCTTY = 0

func hasMultipleLinks(os.FileInfo) bool { return false }
