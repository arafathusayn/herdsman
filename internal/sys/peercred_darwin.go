package sys

import "golang.org/x/sys/unix"

func peerCredentials(fd int) (PeerCred, error) {
	x, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return PeerCred{}, err
	}
	// The kernel always fills the first group with the effective group.
	cred := PeerCred{UID: int(x.Uid), GID: int(x.Groups[0])}
	cred.PID, err = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return cred, err
}
