package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	enterScreen = "\x1b[?25l\x1b[?2004h"
	leaveScreen = "\r\n\x1b[?2004l\x1b[?25h"

	defaultEscDelay = 50 * time.Millisecond
)

// ErrInputClosed is returned when the input ends while a program is running.
var ErrInputClosed = errors.New("tui: input closed")

// Terminal is the part of a terminal the runtime changes or asks about.
type Terminal interface {
	// MakeRaw switches to raw mode and returns the function that undoes it.
	MakeRaw() (restore func() error, err error)
	// Size returns the terminal's size in cells.
	Size() (cols, rows int, err error)
}

// Input reads a stream on one goroutine for the life of the stream. Programs
// that run one after another share it, so a read still waiting when a program
// ends hands its bytes to the next program instead of losing them.
type Input struct {
	chunks chan []byte
	err    error
}

// NewInput starts reading r.
func NewInput(r io.Reader) *Input {
	in := &Input{chunks: make(chan []byte)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				in.chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				in.err = err
				close(in.chunks)
				return
			}
		}
	}()
	return in
}

// Options connects a program to its terminal.
type Options struct {
	In   *Input
	Out  io.Writer
	Term Terminal
	// Resize signals that the terminal's size may have changed. Nil means the
	// size is fixed.
	Resize <-chan struct{}
	// EscDelay is how long a lone Escape waits to become a key. Zero means 50ms.
	EscDelay time.Duration
}

// loop is the state of one running program.
type loop struct {
	model    Model
	render   renderer
	term     Terminal
	msgs     chan Msg
	done     chan struct{}
	finished bool
}

// Run shows m until a command returns Quit, the context ends, or the input
// closes. It returns the last model. The terminal is put back on every path,
// including a panic in the model.
func Run(ctx context.Context, m Model, o Options) (final Model, err error) {
	restore, err := o.Term.MakeRaw()
	if err != nil {
		return m, fmt.Errorf("tui: raw mode: %w", err)
	}
	defer func() { err = errors.Join(err, restore()) }()

	cols, rows, err := o.Term.Size()
	if err != nil {
		return m, fmt.Errorf("tui: terminal size: %w", err)
	}
	if _, err := io.WriteString(o.Out, enterScreen); err != nil {
		return m, err
	}
	defer func() {
		_, leaveErr := io.WriteString(o.Out, leaveScreen)
		err = errors.Join(err, leaveErr)
	}()

	l := &loop{
		model:  m,
		render: renderer{w: o.Out, cols: cols, rows: rows},
		term:   o.Term,
		msgs:   make(chan Msg),
		done:   make(chan struct{}),
	}
	defer close(l.done)
	return l.run(ctx, o, Resize{Cols: cols, Rows: rows})
}

func (l *loop) run(ctx context.Context, o Options, first Resize) (Model, error) {
	delay := o.EscDelay
	if delay == 0 {
		delay = defaultEscDelay
	}
	if err := l.handle(first); err != nil {
		return l.model, err
	}

	var parser Parser
	var esc <-chan time.Time
	for !l.finished {
		var batch []Msg
		select {
		case <-ctx.Done():
			return l.model, ctx.Err()
		case chunk, ok := <-o.In.chunks:
			if !ok {
				return l.model, fmt.Errorf("%w: %w", ErrInputClosed, o.In.err)
			}
			batch = parser.Feed(chunk)
			esc = nil
			if parser.Pending() {
				esc = time.After(delay)
			}
		case <-esc:
			batch = parser.Flush()
			esc = nil
		case <-o.Resize:
			cols, rows, err := l.term.Size()
			if err != nil {
				return l.model, fmt.Errorf("tui: terminal size: %w", err)
			}
			if err := l.render.resize(cols, rows); err != nil {
				return l.model, err
			}
			batch = []Msg{Resize{Cols: cols, Rows: rows}}
		case msg := <-l.msgs:
			batch = []Msg{msg}
		}
		if err := l.handle(batch...); err != nil {
			return l.model, err
		}
	}
	return l.model, nil
}

// handle applies messages and then paints once, so that keys arriving together
// cost one frame.
func (l *loop) handle(msgs ...Msg) error {
	changed := false
	for _, msg := range msgs {
		switch msg := msg.(type) {
		case quitMsg:
			l.finished = true
		case batchMsg:
			for _, cmd := range msg {
				l.dispatch(cmd)
			}
		default:
			next, cmd := l.model.Update(msg)
			l.model = next
			l.dispatch(cmd)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return l.render.paint(l.model.View())
}

// dispatch runs a command away from the loop and queues its result.
func (l *loop) dispatch(cmd Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case l.msgs <- msg:
		case <-l.done:
		}
	}()
}
