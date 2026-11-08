package tui_test

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

const getTermios = syscall.TCGETS

// openPTY returns a new pseudo-terminal's controlling and terminal sides.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var unlock, number int32
	ioctl(t, master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	ioctl(t, master, syscall.TIOCGPTN, unsafe.Pointer(&number))
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return master, slave
}
