//go:build unix

package web

import "syscall"

// openDirFlag makes an open fail unless it opens a folder.
const openDirFlag = syscall.O_DIRECTORY
