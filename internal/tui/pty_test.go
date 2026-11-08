package tui_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/arafathusayn/herdsman/internal/tui"
	"github.com/arafathusayn/herdsman/internal/tui/components"
)

// ioctl runs one request on an open file without changing its blocking mode.
func ioctl(t *testing.T, f *os.File, request uintptr, arg unsafe.Pointer) {
	t.Helper()
	raw, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("ioctl %#x: %v", request, errno)
	}
}

type winsize struct{ rows, cols, x, y uint16 }

func setSize(t *testing.T, master *os.File, cols, rows uint16) {
	t.Helper()
	size := winsize{rows: rows, cols: cols}
	ioctl(t, master, syscall.TIOCSWINSZ, unsafe.Pointer(&size))
}

func termios(t *testing.T, f *os.File) syscall.Termios {
	t.Helper()
	var state syscall.Termios
	ioctl(t, f, getTermios, unsafe.Pointer(&state))
	return state
}

// screen collects what the program writes to the terminal.
type screen struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (s *screen) read(master *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := master.Read(buf)
		s.mu.Lock()
		s.data.Write(buf[:n])
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *screen) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.String()
}

func (s *screen) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(s.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never appeared in %q", text, s.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// session is a program's terminal, with the far side held by the test.
type session struct {
	master, slave *os.File
	screen        *screen
}

func newSession(t *testing.T) *session {
	t.Helper()
	master, slave := openPTY(t)
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	setSize(t, master, 80, 24)
	s := &session{master: master, slave: slave, screen: &screen{}}
	go s.screen.read(master)
	return s
}

func (s *session) options() tui.Options {
	return tui.Options{In: tui.NewInput(s.slave), Out: s.slave, Term: tui.NewTerminal(s.slave)}
}

func (s *session) press(t *testing.T, keys string) {
	t.Helper()
	if _, err := s.master.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

func routeForm() components.Form {
	notEmpty := func(v any) error {
		if v == "" {
			return errors.New("a name is required")
		}
		return nil
	}
	return components.NewForm("New route", tui.Style{Color: true},
		components.Entry{Field: components.NewTextInput("Name", ""), Validate: notEmpty},
		components.Entry{Field: components.NewSelect("Harness", []string{"claude", "codex"}, 0)},
		components.Entry{Field: components.NewConfirm("Web", false)},
	).WithPlan(func(v []any) string { return "create route " + v[0].(string) })
}

const hideCursor = "\x1b[?25l"

func TestFormEndToEndOnAPty(t *testing.T) {
	s := newSession(t)
	before := termios(t, s.slave)
	if !tui.IsTerminal(s.slave) {
		t.Fatal("the pty did not read as a terminal")
	}

	type result struct {
		model tui.Model
		err   error
	}
	done := make(chan result, 1)
	go func() {
		m, err := tui.Run(context.Background(), routeForm(), s.options())
		done <- result{m, err}
	}()

	s.screen.waitFor(t, hideCursor)
	if raw := termios(t, s.slave); raw.Lflag&syscall.ICANON != 0 || raw.Lflag&syscall.ECHO != 0 {
		t.Error("the terminal is not in raw mode while the form runs")
	}
	s.press(t, "\r")
	s.screen.waitFor(t, "a name is required")
	s.press(t, "ship-it\t\x1b[B\ty\r")
	s.screen.waitFor(t, "create route ship-it")
	s.press(t, "\r")

	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the form did not finish: %q", s.screen.String())
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	form := got.model.(components.Form)
	if form.Outcome() != components.Submitted {
		t.Errorf("outcome %v", form.Outcome())
	}
	values := form.Values()
	if values[0] != "ship-it" || values[1] != "codex" || values[2] != true {
		t.Errorf("values %#v", values)
	}
	if after := termios(t, s.slave); after != before {
		t.Errorf("the terminal was not restored:\nbefore %+v\nafter  %+v", before, after)
	}
	s.screen.waitFor(t, "\x1b[?25h")
	if strings.Contains(s.screen.String(), "\x1b[?1049") {
		t.Error("the form used the alternate screen")
	}
}

func TestEscapeCancelsOnAPty(t *testing.T) {
	s := newSession(t)
	done := make(chan tui.Model, 1)
	go func() {
		m, _ := tui.Run(context.Background(), routeForm(), s.options())
		done <- m
	}()
	s.screen.waitFor(t, hideCursor)
	s.press(t, "\x1b")
	select {
	case m := <-done:
		if m.(components.Form).Outcome() != components.Canceled {
			t.Error("Escape did not cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Escape did not end the form")
	}
}

func TestResizeRedrawsOnAPty(t *testing.T) {
	s := newSession(t)
	changes, stop := tui.WatchResize()
	defer stop()
	opts := s.options()
	opts.Resize = changes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tui.Run(ctx, routeForm(), opts)
		done <- err
	}()
	s.screen.waitFor(t, "New route")
	first := len(s.screen.String())
	setSize(t, s.master, 40, 10)
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(s.screen.String()[first:], "New route") {
		if time.Now().After(deadline) {
			t.Fatalf("no redraw after the resize: %q", s.screen.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

func TestTerminalSizeFallsBackWhenUnknown(t *testing.T) {
	s := newSession(t)
	setSize(t, s.master, 0, 0)
	cols, rows, err := tui.NewTerminal(s.slave).Size()
	if err != nil || cols != 80 || rows != 24 {
		t.Errorf("%d %d %v", cols, rows, err)
	}
}

func TestTerminalErrorsOffATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if tui.IsTerminal(f) {
		t.Error("the null device read as a terminal")
	}
	term := tui.NewTerminal(f)
	if _, err := term.MakeRaw(); err == nil {
		t.Error("raw mode succeeded on the null device")
	}
	if _, _, err := term.Size(); err == nil {
		t.Error("size succeeded on the null device")
	}
}

func TestWatchResizeCollapsesSignals(t *testing.T) {
	changes, stop := tui.WatchResize()
	defer stop()
	for range 3 {
		if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-changes:
	case <-time.After(10 * time.Second):
		t.Fatal("no change reported")
	}
}
