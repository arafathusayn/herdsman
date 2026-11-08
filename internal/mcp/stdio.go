package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// maxLine bounds one message; the daemon would refuse a larger body anyway.
const maxLine = rpc.MaxBody

// connection is one client on one pair of streams.
type connection struct {
	server *Server
	ctx    context.Context
	// initialized is touched only by the reader goroutine.
	initialized bool

	writeMu  sync.Mutex
	out      io.Writer
	writeErr error

	callsMu sync.Mutex
	calls   map[string]context.CancelFunc
	wg      sync.WaitGroup
}

// Serve answers newline-delimited JSON-RPC from in until it ends. Calls still
// running then are cancelled, since nobody is left to read their answers.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	c := &connection{server: s, ctx: ctx, out: out, calls: map[string]context.CancelFunc{}}
	err := c.read(bufio.NewReader(in))
	stop()
	c.wg.Wait()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return errors.Join(err, c.writeErr)
}

func (c *connection) read(in *bufio.Reader) error {
	for {
		line, tooLong, err := readLine(in)
		switch line = bytes.TrimSpace(line); {
		case tooLong:
			c.fail(nil, codeInvalidRequest, "the message is too large")
		case len(line) > 0:
			c.handle(line)
		}
		if err != nil {
			return err
		}
	}
}

// readLine returns one line. A line over the limit is consumed and reported
// rather than kept, so a long message cannot take the process's memory. The
// line and an error come together when input ends without a newline.
func readLine(in *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		part, err := in.ReadSlice('\n')
		if !tooLong {
			if tooLong = len(line)+len(part) > maxLine; tooLong {
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, tooLong, err
		}
	}
}

func (c *connection) write(r response) {
	r.JSONRPC = "2.0"
	// Results are maps and structs of JSON-safe values and raw JSON built here.
	data, _ := json.Marshal(r)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.out.Write(append(data, '\n')); err != nil && c.writeErr == nil {
		c.writeErr = err
	}
}

func (c *connection) reply(id json.RawMessage, result any) {
	c.write(response{ID: id, Result: result})
}

func (c *connection) fail(id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	c.write(response{ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (c *connection) cancel(id json.RawMessage) {
	c.callsMu.Lock()
	defer c.callsMu.Unlock()
	if stop, ok := c.calls[string(id)]; ok {
		stop()
	}
}

// spawn runs a tool call beside the reader, so a slow call does not hold up a
// ping or its own cancellation.
func (c *connection) spawn(id json.RawMessage, t *tool, arguments json.RawMessage) {
	key := string(id)
	c.callsMu.Lock()
	_, duplicate := c.calls[key]
	busy := len(c.calls) >= maxInFlight
	var ctx context.Context
	var stop context.CancelFunc
	if !duplicate && !busy {
		ctx, stop = context.WithCancel(c.ctx)
		c.calls[key] = stop
	}
	c.callsMu.Unlock()
	switch {
	case duplicate:
		c.fail(id, codeInvalidRequest, "this id is already in use by a running call")
		return
	case busy:
		c.fail(id, codeBusy, "too many calls are running")
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer stop()
		result := c.server.callTool(ctx, t, arguments)
		c.callsMu.Lock()
		delete(c.calls, key)
		c.callsMu.Unlock()
		// A cancelled request gets no answer.
		if ctx.Err() == nil {
			c.reply(id, result)
		}
	}()
}
