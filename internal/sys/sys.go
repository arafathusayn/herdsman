// Package sys is the application's view of operating system calls that the
// standard library does not offer. New code reaches golang.org/x/sys through
// this package, so that replacing that dependency changes one directory.
//
// Darwin and Linux are the supported systems.
package sys

import "syscall"

// control runs fn with the descriptor of c, and returns the error of either.
func control(c syscall.Conn, fn func(fd int) error) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := raw.Control(func(fd uintptr) { inner = fn(int(fd)) }); err != nil {
		return err
	}
	return inner
}
