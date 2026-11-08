package sys

import "golang.org/x/sys/unix"

func peerCredentials(fd int) (PeerCred, error) {
	u, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return PeerCred{}, err
	}
	return PeerCred{UID: int(u.Uid), GID: int(u.Gid), PID: int(u.Pid)}, nil
}
