// Package herdr talks to a running herdr server over its socket API:
// newline-delimited JSON requests, one response line per request, and a
// stream of event lines after events.subscribe.
package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// maxLine bounds one response or event line, so a broken or hostile peer
// cannot make the client buffer without limit. A snapshot of a large session
// is well under 1 MiB.
const maxLine = 32 << 20

// errLineTooLong is returned when a line exceeds maxLine.
var errLineTooLong = errors.New("herdr: response line too long")

// readLine reads one newline-terminated line of at most maxLine bytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			return nil, errLineTooLong
		}
		line = append(line, chunk...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// Protocol is the herdr socket protocol this build was written against.
// A server on another protocol is refused, because the protocol is private
// and its methods change between herdr releases.
const Protocol = 22

// Error is an error response from the herdr server.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// SocketPath resolves the socket of a herdr session the way herdr does:
// HERDR_SOCKET_PATH first, then the named session, then the default session.
func SocketPath(session string) (string, error) {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" && session == "" {
		return p, nil
	}
	if session == "" {
		session = os.Getenv("HERDR_SESSION")
	}
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if session == "" {
		return filepath.Join(dir, "herdr.sock"), nil
	}
	return filepath.Join(dir, "sessions", session, "herdr.sock"), nil
}

func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "herdr"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "herdr"), nil
}

// Client opens one connection per request, like the herdr CLI does, so a
// slow call never blocks another.
type Client struct {
	Socket string
	seq    atomic.Uint64
}

func New(socket string) *Client { return &Client{Socket: socket} }

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("herdr server not reachable at %s: %w", c.Socket, err)
	}
	return conn, nil
}

func (c *Client) send(conn net.Conn, method string, params any) (string, error) {
	if params == nil {
		params = struct{}{}
	}
	id := "herdsman:" + strconv.FormatUint(c.seq.Add(1), 10)
	b, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return "", err
	}
	_, err = conn.Write(append(b, '\n'))
	return id, err
}

// Call sends one request and decodes its result into out (which may be nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if _, err := c.send(conn, method, params); err != nil {
		return err
	}
	line, err := readLine(bufio.NewReaderSize(conn, 1<<16))
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("%s: bad response: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Pong is the reply to ping.
type Pong struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// Check pings the server and refuses a protocol this build does not know.
func (c *Client) Check(ctx context.Context) (Pong, error) {
	var p Pong
	if err := c.Call(ctx, "ping", nil, &p); err != nil {
		return p, err
	}
	if p.Protocol != Protocol {
		return p, fmt.Errorf("herdr %s speaks protocol %d; this herdsman build speaks %d: update herdsman or herdr", p.Version, p.Protocol, Protocol)
	}
	return p, nil
}

// Event is one line of an event subscription.
type Event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// Subscribe starts an event subscription and calls fn for each event until
// ctx ends or the connection drops. It returns the reason the stream ended.
func (c *Client) Subscribe(ctx context.Context, subs []map[string]any, fn func(Event)) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := c.send(conn, "events.subscribe", map[string]any{"subscriptions": subs}); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 1<<16)
	first, err := readLine(r)
	if err != nil {
		return err
	}
	var ack response
	if err := json.Unmarshal(first, &ack); err != nil {
		return err
	}
	if ack.Error != nil {
		return ack.Error
	}
	for {
		line, err := readLine(r)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Event != "" {
			fn(ev)
		}
	}
}
