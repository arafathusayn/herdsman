package message

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// Events opens event subscriptions: the hint transport that replaces
// the old event script (mkfifo plus nc -U). Queue scans, never events, decide
// correctness; an event only wakes a scan early.
type Events interface {
	Open(ctx context.Context, subs []map[string]any) (Stream, error)
}

// Stream is one open subscription.
type Stream interface {
	// Next waits up to d for the next event line. errTimeout: none came.
	// Any other error: the stream ended; open a new one.
	Next(d time.Duration) (string, error)
	Close()
}

var errTimeout = errors.New("event wait timed out")

// herdrEvents subscribes through the herdr socket.
type herdrEvents struct{ c *herdr.Client }

type herdrStream struct {
	cancel context.CancelFunc
	lines  chan string
	done   chan error
}

func (h herdrEvents) Open(ctx context.Context, subs []map[string]any) (Stream, error) {
	if h.c == nil || h.c.Socket == "" {
		return nil, errors.New("no herdr socket")
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &herdrStream{cancel: cancel, lines: make(chan string, 1024), done: make(chan error, 1)}
	go func() {
		err := h.c.Subscribe(sctx, subs, func(ev herdr.Event) {
			select {
			case s.lines <- `{"event":` + quote(ev.Event) + `,"data":` + string(ev.Data) + `}`:
			default: // a full buffer drops a hint; the sweep still runs
			}
		})
		s.done <- err
	}()
	// The subscription is up once the server acknowledged it: herdr's
	// Subscribe has no start hook, so a short grace period stands in for the
	// script's one-second read of subscription_started.
	select {
	case err := <-s.done:
		cancel()
		if err == nil {
			err = errors.New("subscription ended")
		}
		return nil, err
	case <-time.After(50 * time.Millisecond):
	}
	return s, nil
}

func quote(s string) string {
	b, _ := compact(s)
	return string(b)
}

func (s *herdrStream) Next(d time.Duration) (string, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case l := <-s.lines:
		return l, nil
	case err := <-s.done:
		s.done <- err
		if err == nil {
			err = errors.New("subscription ended")
		}
		return "", err
	case <-t.C:
		return "", errTimeout
	}
}

func (s *herdrStream) Close() { s.cancel() }

func (e *Env) events() Events {
	if e.Events != nil {
		return e.Events
	}
	return herdrEvents{c: e.Client}
}

// hint classifies an event line as events_next does: 0 a hint for the
// channel (a message ring, a count, a closed or exited pane), 3 another
// event (ignored), 2 not an event (the stream is dropped).
func hint(line string) int {
	switch {
	case strings.Contains(line, "message_out"), strings.Contains(line, "messages"),
		strings.Contains(line, "pane_closed"), strings.Contains(line, "pane_exited"),
		strings.Contains(line, "pane.closed"), strings.Contains(line, "pane.exited"):
	case strings.Contains(line, `"event"`):
		return 3
	default:
		return 2
	}
	m, ok := decodeObject(line)
	if !ok {
		return 2
	}
	if _, ok := m["event"].(string); !ok {
		return 2
	}
	return 0
}

// eventPane is .data.pane.pane_id (or .data.pane_id) of an event line, and the ring token
// .data.pane.tokens.message_out.
func eventPane(line string) (pane, ring string) {
	m, _ := decodeObject(line)
	p, _ := m["data"].(map[string]any)
	pp, _ := p["pane"].(map[string]any)
	pane, _ = pp["pane_id"].(string)
	if pane == "" {
		// pane.agent_status_changed names its pane at .data.pane_id.
		pane, _ = p["pane_id"].(string)
	}
	toks, _ := pp["tokens"].(map[string]any)
	ring, _ = toks["message_out"].(string)
	return pane, ring
}
