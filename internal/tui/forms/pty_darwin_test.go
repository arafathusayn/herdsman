package forms

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY returns a new pseudo-terminal's controlling and terminal sides.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var name [128]byte
	ioctl(t, master, syscall.TIOCPTYGRANT, nil)
	ioctl(t, master, syscall.TIOCPTYUNLK, nil)
	ioctl(t, master, syscall.TIOCPTYGNAME, unsafe.Pointer(&name))
	path := string(name[:bytes.IndexByte(name[:], 0)])
	slave, err = os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return master, slave
}
