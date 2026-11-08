package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal says whether in is really a terminal, by asking the kernel for its
// terminal settings. A file mode cannot answer this: /dev/zero is a character
// device too, and it would let a recipe run with no person present.
func IsTerminal(in any) bool {
	file, ok := in.(*os.File)
	if !ok || file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), termiosRequest)
	return err == nil
}
