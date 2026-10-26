package daemon

import (
	"net"
	"os"
)

// peerOf reads a connection's peer user (a test replaces it).
var peerOf = peerUID

// peerListener refuses every connection whose peer runs as another user
// (3.3). It proves the same user only (8).
type peerListener struct {
	net.Listener
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			continue
		}
		uid, err := peerOf(uc)
		if err != nil || uid != os.Getuid() {
			_ = c.Close()
			continue
		}
		return c, nil
	}
}
