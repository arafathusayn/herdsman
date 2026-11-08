package sys

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// replicaPath readies the controlling side and names the replica device.
func replicaPath(t *testing.T, fd int) string {
	t.Helper()
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		t.Fatal(err)
	}
	var name [128]byte
	//lint:ignore SA1019 x/sys has no wrapper for this request, which fills a buffer
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatal(errno)
	}
	return unix.ByteSliceToString(name[:])
}
