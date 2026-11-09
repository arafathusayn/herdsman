package sys

import (
	"errors"
	"syscall"
)

// PeerCred identifies the process at the other end of a Unix socket, as the
// kernel recorded it when the connection was made.
type PeerCred struct {
	UID int // effective user id
	GID int // effective group id
	PID int
}

// PeerCredentials reads the credentials of the peer of a connected Unix socket,
// normally a *net.UnixConn. It fails on anything else.
func PeerCredentials(c syscall.Conn) (PeerCred, error) {
	var cred PeerCred
	err := control(c, func(fd int) (err error) {
		cred, err = peerCredentials(fd)
		return err
	})
	if err != nil {
		return PeerCred{}, err
	}
	return cred, nil
}

// peerFromUcred reads what Linux answers. For a socket with no Unix peer the
// call succeeds with pid 0 and placeholder ids, so that is an error here.
func peerFromUcred(pid int32, uid, gid uint32) (PeerCred, error) {
	if pid == 0 {
		return PeerCred{}, errors.New("sys: the socket has no peer credentials")
	}
	return PeerCred{UID: int(uid), GID: int(gid), PID: int(pid)}, nil
}
