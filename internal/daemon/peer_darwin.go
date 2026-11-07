package daemon

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the connecting process's uid (getpeereid, LOCAL_PEERCRED).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var cerr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			cerr = e
			return
		}
		uid = int(cred.Uid)
	})
	if err == nil {
		err = cerr
	}
	return uid, err
}
