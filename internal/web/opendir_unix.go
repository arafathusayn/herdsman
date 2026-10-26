//go:build unix

package web

import "syscall"

// openDirFlag makes an open fail unless it opens a folder.
const openDirFlag = syscall.O_DIRECTORY

// openNonblockFlag makes an open of a FIFO return at once.
const openNonblockFlag = syscall.O_NONBLOCK
