package sys

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

// cloneFile creates dst and shares src's blocks with FICLONE, which copies no
// metadata. O_NONBLOCK keeps a source swapped for a FIFO from hanging the open.
func cloneFile(src, dst string, info fs.FileInfo) error {
	in, err := unix.Open(src, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(in)
	out, err := unix.Open(dst, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	err = unix.IoctlFileClone(out, in)
	if err == nil {
		err = unix.Fchmod(out, uint32(info.Mode().Perm()))
	}
	if err == nil {
		mtime := unix.NsecToTimespec(info.ModTime().UnixNano())
		err = unix.UtimesNanoAt(unix.AT_FDCWD, dst, []unix.Timespec{mtime, mtime}, unix.AT_SYMLINK_NOFOLLOW)
	}
	if closeErr := unix.Close(out); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = unix.Unlink(dst)
	}
	return err
}
