package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Limits and timeouts used when Options leaves a field unset.
const (
	DefaultMaxMessageSize = 16 << 20
	DefaultMaxFragments   = 1024

	defaultWriteTimeout     = 30 * time.Second
	defaultCloseTimeout     = 5 * time.Second
	defaultHandshakeTimeout = 10 * time.Second

	// lingerBytes bounds what is discarded while waiting for the peer to close.
	lingerBytes = 1 << 20
)

// ErrClosed reports a write after the close handshake started.
var ErrClosed = errors.New("ws: connection closed")

// Options are the settings shared by both roles. A field at or below zero
// takes its default, so a limit can never be switched off.
type Options struct {
	// MaxMessageSize bounds one reassembled message, in bytes.
	MaxMessageSize int
	// MaxFragments bounds the data frames of one message.
	MaxFragments int
	// WriteTimeout bounds each frame write.
	WriteTimeout time.Duration
	// CloseTimeout bounds the close handshake and the wait for the peer to
	// drop the connection.
	CloseTimeout time.Duration
	// HandshakeTimeout bounds the opening handshake.
	HandshakeTimeout time.Duration
	// Subprotocols are offered by a client and, in order of preference,
	// accepted by a server.
	Subprotocols []string
	// OnPong runs on the reading goroutine for each pong. The slice is valid
	// only during the call.
	OnPong func(payload []byte)
}

func (o Options) withDefaults() Options {
	if o.MaxMessageSize <= 0 {
		o.MaxMessageSize = DefaultMaxMessageSize
	}
	if o.MaxFragments <= 0 {
		o.MaxFragments = DefaultMaxFragments
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = defaultWriteTimeout
	}
	if o.CloseTimeout <= 0 {
		o.CloseTimeout = defaultCloseTimeout
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = defaultHandshakeTimeout
	}
	return o
}

// Conn is one WebSocket connection. One goroutine may read while others write;
// writes are serialized. Close may be called from any goroutine.
type Conn struct {
	conn        net.Conn
	client      bool
	opts        Options
	subprotocol string
	closing     atomic.Bool
	stop        sync.Once

	// rmu guards the read side.
	rmu  sync.Mutex
	br   *bufio.Reader
	rerr error
	ctrl [maxControlPayload]byte

	// wmu guards the write side.
	wmu       sync.Mutex
	bw        *bufio.Writer
	werr      error
	closeSent bool
	scratch   [4096]byte
	hdr       [14]byte
}

func newConn(conn net.Conn, br *bufio.Reader, client bool, opts Options, subprotocol string) *Conn {
	return &Conn{
		conn:        conn,
		client:      client,
		opts:        opts.withDefaults(),
		subprotocol: subprotocol,
		br:          br,
		bw:          bufio.NewWriter(conn),
	}
}

// Subprotocol returns the negotiated subprotocol, or "".
func (c *Conn) Subprotocol() string { return c.subprotocol }

// SetReadDeadline sets the deadline for reads. A read that times out ends the
// connection, because a frame may be half consumed.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// ReadMessage returns the next complete message and owns no reference to the
// returned slice. Pings are answered and pongs delivered while it runs. Every
// error is final and repeats on later calls: *CloseError when the peer closed,
// *ProtocolError when the peer broke the protocol.
func (c *Conn) ReadMessage() (Opcode, []byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.rerr != nil {
		return 0, nil, c.rerr
	}
	op, data, err := c.readMessage()
	if err != nil {
		c.rerr = c.finishRead(err)
		return 0, nil, c.rerr
	}
	return op, data, nil
}

func (c *Conn) readMessage() (Opcode, []byte, error) {
	var (
		msg       bytes.Buffer
		first     Opcode
		fragments int
		text      utf8Validator
	)
	for {
		h, err := c.readHeader()
		if err != nil {
			return 0, nil, err
		}
		if h.op.control() {
			if err := c.handleControl(h); err != nil {
				return 0, nil, err
			}
			continue
		}
		if h.op == OpContinuation && first == OpContinuation {
			return 0, nil, protocolErr("continuation without a message")
		}
		if h.op != OpContinuation && first != OpContinuation {
			return 0, nil, protocolErr("new message inside a fragmented message")
		}
		if h.op != OpContinuation {
			first = h.op
		}
		fragments++
		if fragments > c.opts.MaxFragments {
			return 0, nil, &ProtocolError{Code: CloseMessageTooBig, Reason: "too many fragments"}
		}
		if h.length > uint64(c.opts.MaxMessageSize-msg.Len()) {
			return 0, nil, &ProtocolError{Code: CloseMessageTooBig, Reason: "message too big"}
		}

		// ReadFrom grows with the bytes that arrive, so a length the peer
		// claims but never sends costs nothing.
		start := msg.Len()
		n, err := msg.ReadFrom(io.LimitReader(c.br, int64(h.length)))
		if err != nil {
			return 0, nil, fmt.Errorf("ws: read: %w", err)
		}
		if uint64(n) < h.length {
			return 0, nil, fmt.Errorf("ws: read: %w", io.ErrUnexpectedEOF)
		}
		body := msg.Bytes()[start:]
		if h.masked {
			mask(body, h.key, 0)
		}
		if first == OpText && !text.feed(body) {
			return 0, nil, &ProtocolError{Code: CloseInvalidPayload, Reason: "invalid UTF-8"}
		}
		if h.fin {
			if first == OpText && !text.complete() {
				return 0, nil, &ProtocolError{Code: CloseInvalidPayload, Reason: "invalid UTF-8"}
			}
			return first, msg.Bytes(), nil
		}
	}
}

func (c *Conn) readHeader() (header, error) {
	b, err := c.br.Peek(2)
	if err != nil {
		return header{}, fmt.Errorf("ws: read: %w", err)
	}
	h, n, err := parseHeader(b)
	if errors.Is(err, errShortHeader) {
		if b, err = c.br.Peek(headerSize(b[1])); err != nil {
			return header{}, fmt.Errorf("ws: read: %w", err)
		}
		h, n, err = parseHeader(b)
	}
	if err != nil {
		return header{}, err
	}
	c.br.Discard(n)
	// A client's frames are masked and a server's are not, in both directions
	// without exception (RFC 6455, section 5.1).
	if h.masked == c.client {
		return header{}, protocolErr("wrong masking for this role")
	}
	return h, nil
}

func (c *Conn) handleControl(h header) error {
	payload := c.ctrl[:h.length]
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return fmt.Errorf("ws: read: %w", err)
	}
	if h.masked {
		mask(payload, h.key, 0)
	}
	switch h.op {
	case OpPing:
		if err := c.writeFrame(OpPong, payload); err != nil && !errors.Is(err, ErrClosed) {
			return err
		}
	case OpPong:
		if c.opts.OnPong != nil {
			c.opts.OnPong(payload)
		}
	default:
		return c.handleClose(payload)
	}
	return nil
}

func (c *Conn) handleClose(payload []byte) error {
	ce := &CloseError{Code: CloseNoStatus}
	var echo []byte
	if len(payload) > 0 {
		if len(payload) == 1 {
			return protocolErr("close frame with one byte")
		}
		ce.Code = binary.BigEndian.Uint16(payload)
		if !validCloseCode(ce.Code) {
			return protocolErr("invalid close code")
		}
		if !utf8.Valid(payload[2:]) {
			return &ProtocolError{Code: CloseInvalidPayload, Reason: "invalid close reason"}
		}
		ce.Reason = string(payload[2:])
		echo = payload[:2]
	}
	c.beginClosing()
	// A failed echo changes nothing: the connection ends either way.
	_ = c.writeFrame(OpClose, echo)
	return ce
}

// finishRead tears the connection down after a final read error.
func (c *Conn) finishRead(err error) error {
	var pe *ProtocolError
	var ce *CloseError
	switch {
	case errors.As(err, &pe):
		c.beginClosing()
		_ = c.writeFrame(OpClose, closeBody(pe.Code, pe.Reason))
		c.shutdown()
	case errors.As(err, &ce):
		c.shutdown()
	default:
		c.stop.Do(func() { c.conn.Close() })
	}
	return err
}

// beginClosing bounds every remaining write by the close timeout.
func (c *Conn) beginClosing() {
	c.closing.Store(true)
	c.conn.SetWriteDeadline(time.Now().Add(c.opts.CloseTimeout))
}

// shutdown half-closes, then waits a bounded time and byte count for the peer
// to finish, so unread input cannot turn into a reset that destroys the close
// frame. The caller holds rmu.
func (c *Conn) shutdown() {
	c.stop.Do(func() {
		if cw, ok := c.conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		c.conn.SetReadDeadline(time.Now().Add(c.opts.CloseTimeout))
		io.CopyN(io.Discard, c.br, lingerBytes)
		c.conn.Close()
	})
}

// WriteMessage sends one text or binary message as a single frame. Text must
// be valid UTF-8. data is not modified.
func (c *Conn) WriteMessage(op Opcode, data []byte) error {
	if op != OpText && op != OpBinary {
		return fmt.Errorf("ws: WriteMessage: opcode %d is not a data opcode", op)
	}
	if op == OpText && !utf8.Valid(data) {
		return errors.New("ws: WriteMessage: text is not valid UTF-8")
	}
	return c.writeFrame(op, data)
}

// Ping sends a ping with at most 125 bytes of payload.
func (c *Conn) Ping(payload []byte) error {
	if len(payload) > maxControlPayload {
		return errors.New("ws: Ping: payload over 125 bytes")
	}
	return c.writeFrame(OpPing, payload)
}

func (c *Conn) writeFrame(op Opcode, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.werr != nil {
		return c.werr
	}
	if c.closeSent {
		return ErrClosed
	}
	if op == OpClose {
		c.closeSent = true
	}
	if !c.closing.Load() {
		c.conn.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
	}
	// bufio.Writer keeps its first error, so only Flush needs checking. A
	// failed write leaves a partial frame on the wire, hence the sticky werr.
	var key [4]byte
	if c.client {
		rand.Read(key[:])
	}
	c.bw.Write(appendHeader(c.hdr[:0], true, op, c.client, key, uint64(len(payload))))
	if c.client {
		pos := 0
		for rest := payload; len(rest) > 0; {
			n := copy(c.scratch[:], rest)
			pos = mask(c.scratch[:n], key, pos)
			c.bw.Write(c.scratch[:n])
			rest = rest[n:]
		}
	} else {
		c.bw.Write(payload)
	}
	if err := c.bw.Flush(); err != nil {
		c.werr = fmt.Errorf("ws: write: %w", err)
		return c.werr
	}
	return nil
}

func closeBody(code uint16, reason string) []byte {
	return append(binary.BigEndian.AppendUint16(nil, code), reason...)
}

// Close runs the closing handshake: it sends a close frame, reads until the
// peer answers or CloseTimeout passes, and releases the connection. Input that
// arrives meanwhile is discarded. It is safe to call more than once.
func (c *Conn) Close(code uint16, reason string) error {
	if !validCloseCode(code) {
		return fmt.Errorf("ws: Close: code %d cannot be sent", code)
	}
	if len(reason) > maxCloseReason || !utf8.ValidString(reason) {
		return errors.New("ws: Close: reason must be valid UTF-8 of at most 123 bytes")
	}
	c.beginClosing()
	err := c.writeFrame(OpClose, closeBody(code, reason))
	c.conn.SetReadDeadline(time.Now().Add(c.opts.CloseTimeout))

	c.rmu.Lock()
	defer c.rmu.Unlock()
	for c.rerr == nil {
		if _, _, rerr := c.readMessage(); rerr != nil {
			c.rerr = c.finishRead(rerr)
		}
	}
	if errors.Is(err, ErrClosed) {
		return nil
	}
	return err
}
