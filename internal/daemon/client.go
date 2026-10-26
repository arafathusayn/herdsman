package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a client of the daemon socket: the CLI's message and daemon
// commands, and herdsman web.
type Client struct {
	Socket  string
	Version string
	// Party identity, for party requests (5.1).
	Pane       string
	Name       string
	Credential string

	hc      *http.Client
	checked bool
}

// DownError is a daemon that does not answer on its socket.
type DownError struct{ Err error }

func (e *DownError) Error() string { return "the herdsman daemon does not answer: " + e.Err.Error() }

// NewClient makes a client of the daemon at socket.
func NewClient(socket, version string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:       2,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	return &Client{Socket: socket, Version: version, hc: &http.Client{Transport: tr}}
}

// Close frees the client's idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://herdsman"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set(HeaderVersion, c.Version)
	req.Header.Set(HeaderProtocol, strconv.Itoa(Protocol))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Pane != "" {
		req.Header.Set(HeaderPane, c.Pane)
	}
	if c.Name != "" {
		req.Header.Set(HeaderName, c.Name)
	}
	if c.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential)
	}
	return req, nil
}

// Hello asks the daemon its version; it always answers (5.6).
func (c *Client) Hello(ctx context.Context) (Hello, error) {
	var h Hello
	req, err := c.request(ctx, "GET", "/v1/hello", nil)
	if err != nil {
		return h, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return h, &DownError{Err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return h, &DownError{Err: err}
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, Errf("version_malformed", "the daemon's hello is not readable")
	}
	return h, nil
}

// Check is the client's version check before its first request (5.6).
func (c *Client) Check(ctx context.Context) error {
	if c.checked {
		return nil
	}
	h, err := c.Hello(ctx)
	if err != nil {
		return err
	}
	if e := ClientCheck(c.Version, Protocol, h); e != nil {
		return e
	}
	c.checked = true
	return nil
}

// Do sends one request with a JSON body (in, nil for none) and decodes
// the answer into out. A daemon error is an *APIError; a daemon that does
// not answer is a *DownError.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) (int, error) {
	if err := c.Check(ctx); err != nil {
		return 0, err
	}
	var body []byte
	if in != nil {
		b, err := encode(in)
		if err != nil {
			return 0, err
		}
		body = b
	}
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, &DownError{Err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, &DownError{Err: err}
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, parseError(resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("unreadable answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func parseError(status int, b []byte) *APIError {
	var eb errorBody
	if json.Unmarshal(b, &eb) == nil && eb.Error != nil && eb.Error.Code != "" {
		eb.Error.Status = status
		return eb.Error
	}
	e := Errf("internal", "daemon answered HTTP %d", status)
	e.Status = status
	return e
}

// SSEFrame is one frame read from a stream.
type SSEFrame struct {
	ID    string
	Event string
	Data  []byte
}

// EventReader reads frames from one open stream.
type EventReader struct {
	body io.ReadCloser
	rd   *bufio.Reader
}

// Close ends the stream.
func (r *EventReader) Close() { _ = r.body.Close() }

// Next reads the next frame; comment lines (pings) are skipped.
func (r *EventReader) Next() (SSEFrame, error) {
	var f SSEFrame
	for {
		line, err := r.rd.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if f.Event != "" || f.Data != nil {
				return f, nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id: "):
			f.ID = line[4:]
		case strings.HasPrefix(line, "event: "):
			f.Event = line[7:]
		case strings.HasPrefix(line, "data: "):
			f.Data = append(f.Data, line[6:]...)
		}
	}
}

// Events opens a stream (a party's route stream with an optional cursor,
// or the view stream).
func (c *Client) Events(ctx context.Context, path, cursor string) (*EventReader, error) {
	if err := c.Check(ctx); err != nil {
		return nil, err
	}
	req, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &DownError{Err: err}
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, parseError(resp.StatusCode, b)
	}
	return &EventReader{body: resp.Body, rd: bufio.NewReaderSize(resp.Body, 4096)}, nil
}

// IsDown reports whether err is a daemon that does not answer.
func IsDown(err error) bool {
	var d *DownError
	return errors.As(err, &d)
}

// AsAPIError returns err as a daemon error.
func AsAPIError(err error) (*APIError, bool) {
	var e *APIError
	ok := errors.As(err, &e)
	return e, ok
}
