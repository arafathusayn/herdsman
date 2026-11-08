package sys

import "syscall"

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
