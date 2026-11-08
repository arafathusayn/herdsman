package sys

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

// cloneFile keeps the mode and times through clonefile itself.
func cloneFile(src, dst string, _ fs.FileInfo) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW|unix.CLONE_NOOWNERCOPY)
}
