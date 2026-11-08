package sys

import "golang.org/x/sys/unix"

const (
	reqGetTermios = unix.TIOCGETA
	reqSetTermios = unix.TIOCSETA
)
