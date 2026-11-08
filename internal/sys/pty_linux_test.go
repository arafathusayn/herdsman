package sys

import (
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// replicaPath unlocks the controlling side and names the replica device.
func replicaPath(t *testing.T, fd int) string {
	t.Helper()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return "/dev/pts/" + strconv.Itoa(n)
}
