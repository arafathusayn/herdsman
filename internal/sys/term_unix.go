//go:build darwin || linux

package sys

import "golang.org/x/sys/unix"

// RawMode is a terminal switched to raw mode, holding what Restore puts back.
type RawMode struct {
	fd    int
	saved unix.Termios
}

// MakeRaw switches the terminal on fd to raw mode: input arrives byte by byte,
// unechoed and without signal keys, and output is not post-processed.
func MakeRaw(fd int) (*RawMode, error) {
	saved, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *saved
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, reqSetTermios, &raw); err != nil {
		return nil, err
	}
	return &RawMode{fd: fd, saved: *saved}, nil
}

// Restore puts the terminal back as MakeRaw found it.
func (m *RawMode) Restore() error {
	return unix.IoctlSetTermios(m.fd, reqSetTermios, &m.saved)
}

// IsTerminal reports whether fd is a terminal.
func IsTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, reqGetTermios)
	return err == nil
}

// TerminalSize returns the size of the terminal on fd, in cells.
func TerminalSize(fd int) (cols, rows int, err error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}
