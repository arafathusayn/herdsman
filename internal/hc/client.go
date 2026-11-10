// Package hc is Herdsman's own client for herdr-core (the hc program): JSON-RPC
// 2.0, one message per line, on core's Unix socket. It speaks only the methods
// listed in methods.go, refuses a core that lacks one of them, and keeps
// control calls and the event subscription on separate connections.
package hc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Error kinds core puts in data.kind. The list is not closed: core may send
// others, and Error carries them as they come.
const (
	KindNotFound              = "not_found"
	KindUnsupportedGeneration = "unsupported_generation"
	KindUnknownMethod         = "unknown_method"
	KindInvalidParams         = "invalid_params"
	KindParseError            = "parse_error"
	KindStatusMismatch        = "status_mismatch"
	KindBlocked               = "blocked"
	KindFinished              = "finished"
	KindUnavailable           = "unavailable"
	KindNameTaken             = "name_taken"
	KindSpawnFailed           = "spawn_failed"
	KindMachineUnknown        = "machine_unknown"
)

// Error is an error answer from core.
type Error struct {
	Method  string
	Code    int
	Message string
	Kind    string // data.kind, stable across core releases
	Hint    string // data.hint, for people
}

func (e *Error) Error() string {
	s := "hc " + e.Method + ": " + e.Kind + ": " + e.Message
	if e.Hint != "" {
		s += " (" + e.Hint + ")"
	}
	return s
}

// IsKind reports whether err is a core error of that kind.
func IsKind(err error, kind string) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == kind
}

func coreError(method string, w *wireError) *Error {
	e := &Error{Method: method, Code: w.Code, Message: w.Message}
	var data struct {
		Kind string `json:"kind"`
		Hint string `json:"hint"`
	}
	if len(w.Data) > 0 && json.Unmarshal(w.Data, &data) == nil {
		e.Kind, e.Hint = data.Kind, data.Hint
	}
	if e.Kind == "" {
		e.Kind = "unknown"
	}
	return e
}

// ErrClosed is returned by calls on a client whose connection has ended.
var ErrClosed = errors.New("hc: connection closed")

// SocketPath resolves the socket of a core session the way hc does: HC_SOCKET
// first; otherwise the session (the argument, else HC_SESSION, else
// "default") under XDG_RUNTIME_DIR, or under the state directory when
// XDG_RUNTIME_DIR is unset.
func SocketPath(session string) (string, error) {
	if p := os.Getenv("HC_SOCKET"); p != "" && session == "" {
		return p, nil
	}
	if session == "" {
		session = os.Getenv("HC_SESSION")
	}
	if session == "" {
		session = "default"
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "herdr-core", session+".sock"), nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdr-core", session, session+".sock"), nil
}

// Options tune a connection. The zero value takes the defaults.
type Options struct {
	// MaxFrame bounds one message in either direction (DefaultMaxFrame).
	MaxFrame int
	// Timeout ends a call whose context has no deadline (30 s).
	Timeout time.Duration
	// IdleTimeout makes a subscription unverified when nothing, not even a
	// heartbeat, arrives for this long (45 s; core beats every 15 s).
	IdleTimeout time.Duration
	// ExpectCore, when set, is the core id the endpoint was persisted with; a
	// different core is refused with *CoreChangedError.
	ExpectCore string
}

func (o Options) maxFrame() int {
	if o.MaxFrame > 0 {
		return o.MaxFrame
	}
	return DefaultMaxFrame
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 30 * time.Second
}

func (o Options) idleTimeout() time.Duration {
	if o.IdleTimeout > 0 {
		return o.IdleTimeout
	}
	return 45 * time.Second
}

// CoreChangedError is a core whose id is not the one the endpoint was
// persisted with: its state was wiped, or the socket now leads elsewhere.
type CoreChangedError struct{ Want, Got string }

func (e *CoreChangedError) Error() string {
	return fmt.Sprintf("hc: core id is %s, not the recorded %s", e.Got, e.Want)
}

// Client is one control connection. Calls may run at once; answers are
// matched to calls by id.
type Client struct {
	conn     net.Conn
	maxFrame int
	timeout  time.Duration
	hello    HelloResult

	wmu sync.Mutex // one writer at a time

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan frame
	err     error // why the connection ended, once it has
	done    chan struct{}
}

// Dial connects to the core at socket, says hello at the pinned generation
// and checks that core serves every listed method.
func Dial(ctx context.Context, socket string, opts Options) (*Client, error) {
	conn, r, hello, err := open(ctx, socket, opts)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn: conn, maxFrame: opts.maxFrame(), timeout: opts.timeout(), hello: hello,
		nextID: 1, pending: map[uint64]chan frame{}, done: make(chan struct{}),
	}
	go c.readLoop(r)
	return c, nil
}

// open dials, says hello and checks capabilities: the steps every connection
// takes before anything else.
func open(ctx context.Context, socket string, opts Options) (net.Conn, *bufio.Reader, HelloResult, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, nil, HelloResult{}, fmt.Errorf("hc: core not reachable at %s: %w", socket, err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	var hello HelloResult
	err = syncCall(ctx, conn, r, opts, 0, MethodHello, helloParams{Generation: Generation}, &hello)
	var refusal *Error
	if errors.As(err, &refusal) && refusal.Kind == KindUnsupportedGeneration {
		got := "refused it: " + refusal.Message
		if refusal.Hint != "" {
			got += " (" + refusal.Hint + ")"
		}
		err = &GenerationError{Want: Generation, Got: got}
	}
	if err == nil {
		err = checkHello(hello)
	}
	if err == nil && opts.ExpectCore != "" && hello.Core.ID != opts.ExpectCore {
		err = &CoreChangedError{Want: opts.ExpectCore, Got: hello.Core.ID}
	}
	if err != nil {
		_ = conn.Close()
		return nil, nil, HelloResult{}, err
	}
	return conn, r, hello, nil
}

// syncCall writes one request and reads its answer on a connection nobody
// else reads yet: the hello and the subscribe request.
func syncCall(ctx context.Context, conn net.Conn, r *bufio.Reader, opts Options, id uint64, method string, params, out any) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(opts.timeout())
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	defer conn.SetDeadline(time.Time{})
	b, err := encodeRequest(id, method, params, opts.maxFrame())
	if err != nil {
		return err
	}
	if _, err := conn.Write(b); err != nil {
		return ctxErr(ctx, method, err)
	}
	for {
		line, err := readFrame(r, opts.maxFrame())
		if err != nil {
			return ctxErr(ctx, method, err)
		}
		f, err := parseFrame(line)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if f.kind == frameNotification {
			continue // not ours; nothing is subscribed yet
		}
		if f.nullID {
			return coreError(method, f.err)
		}
		if f.id != id {
			return fmt.Errorf("%s: %w", method, badFrame("answer to request %d, not %d", f.id, id))
		}
		return decodeAnswer(method, f, out)
	}
}

func ctxErr(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("hc %s: %w", method, ctx.Err())
	}
	// The connection deadline is the context's (or the default timeout), and
	// it can fire just before the context notices.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("hc %s: %w", method, context.DeadlineExceeded)
	}
	return fmt.Errorf("hc %s: %w", method, err)
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

func encodeRequest(id uint64, method string, params any, max int) ([]byte, error) {
	b, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("hc %s: %w", method, err)
	}
	if len(b)+1 > max {
		return nil, fmt.Errorf("hc %s: %w", method, ErrFrameTooLarge)
	}
	return append(b, '\n'), nil
}

func decodeAnswer(method string, f frame, out any) error {
	if f.err != nil {
		return coreError(method, f.err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(f.result, out); err != nil {
		return fmt.Errorf("hc %s: %w", method, err)
	}
	return nil
}

// Hello returns what core said at hello: its identity, generation, version,
// methods and features.
func (c *Client) Hello() HelloResult { return c.hello }

// Core returns the identity of the core this client is connected to.
func (c *Client) Core() CoreInfo { return c.hello.Core }

// Close ends the connection; pending calls fail with ErrClosed.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) readLoop(r *bufio.Reader) {
	var err error
	for {
		var line []byte
		line, err = readFrame(r, c.maxFrame)
		if err != nil {
			break
		}
		var f frame
		f, err = parseFrame(line)
		if err != nil {
			break
		}
		if f.kind == frameNotification {
			continue // the control connection subscribes to nothing
		}
		if f.nullID {
			// Core could not read a request, so nobody can tell whose it was.
			err = coreError("", f.err)
			break
		}
		c.mu.Lock()
		ch := c.pending[f.id]
		delete(c.pending, f.id)
		c.mu.Unlock()
		if ch != nil {
			ch <- f
		}
	}
	_ = c.conn.Close()
	c.mu.Lock()
	if c.err == nil {
		c.err = fmt.Errorf("%w: %w", ErrClosed, err)
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// call sends one listed method and decodes its result into out. A method not
// on the list is a programming error and never reaches core.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	if !listed(method) || method == MethodHello || method == MethodSubscribe {
		return fmt.Errorf("hc: %s is not a control method this client uses", method)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	ch := make(chan frame, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("hc %s: %w", method, err)
	}
	id := c.nextID
	c.nextID++
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	b, err := encodeRequest(id, method, params, c.maxFrame)
	if err != nil {
		forget()
		return err
	}
	c.wmu.Lock()
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Unix(1, 0)) })
	_, err = c.conn.Write(b)
	stop()
	_ = c.conn.SetWriteDeadline(time.Time{})
	c.wmu.Unlock()
	if err != nil {
		forget()
		// A half-written line leaves the stream unusable.
		_ = c.conn.Close()
		return ctxErr(ctx, method, err)
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return fmt.Errorf("hc %s: %w", method, c.Err())
		}
		return decodeAnswer(method, f, out)
	case <-ctx.Done():
		forget()
		return fmt.Errorf("hc %s: %w", method, ctx.Err())
	}
}
