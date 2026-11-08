package tui

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/sys"
)

// Sizes reported when a terminal does not know its own, as a serial line or a
// session without a window size may not.
const (
	fallbackCols = 80
	fallbackRows = 24
)

// FileTerminal is a Terminal on an open file.
type FileTerminal struct {
	fd int
}

// NewTerminal returns the Terminal on f.
func NewTerminal(f *os.File) FileTerminal { return FileTerminal{fd: int(f.Fd())} }

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return sys.IsTerminal(int(f.Fd())) }

// MakeRaw switches to raw mode and returns the function that undoes it.
func (t FileTerminal) MakeRaw() (func() error, error) {
	mode, err := sys.MakeRaw(t.fd)
	if err != nil {
		return nil, err
	}
	return mode.Restore, nil
}

// Size returns the size in cells. A dimension the terminal reports as zero
// takes a usable default.
func (t FileTerminal) Size() (cols, rows int, err error) {
	cols, rows, err = sys.TerminalSize(t.fd)
	if err != nil {
		return 0, 0, err
	}
	if cols == 0 {
		cols = fallbackCols
	}
	if rows == 0 {
		rows = fallbackRows
	}
	return cols, rows, nil
}

// WatchResize signals on the channel when the window changes size, until stop
// is called. Signals that arrive together collapse into one.
func WatchResize() (changes <-chan struct{}, stop func()) {
	signals := make(chan os.Signal, 1)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-signals:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() {
		signal.Stop(signals)
		close(done)
	}
}
