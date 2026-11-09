package events

import (
	"context"
	"maps"
	"net"
	"net/http"
	"sync"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/ws"
)

// DialOptions say how to open a stream.
type DialOptions struct {
	// URL is the ws:// address of Path.
	URL string
	// DialContext replaces the TCP dialer, for the daemon's Unix socket.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// JSON asks for text frames instead of binary protobuf.
	JSON bool
	// Header adds request headers.
	Header http.Header
}

// Stream is a client's side of one connection. One goroutine may call Recv
// while others send.
type Stream struct {
	conn *ws.Conn
	json bool

	mu      sync.Mutex
	cursors map[string]string
}

// Dial opens a stream.
func Dial(ctx context.Context, opts DialOptions) (*Stream, error) {
	options := ws.ClientOptions{Header: opts.Header, DialContext: opts.DialContext}
	if opts.JSON {
		options.Subprotocols = []string{SubprotocolJSON}
	}
	conn, err := ws.Dial(ctx, opts.URL, options)
	if err != nil {
		return nil, err
	}
	return &Stream{conn: conn, json: opts.JSON, cursors: map[string]string{}}, nil
}

// Send writes one frame.
func (s *Stream) Send(frame *daemonv1.StreamFrame) error {
	op, data, err := encodeFrame(frame, s.json)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(op, data)
}

// Subscribe starts a topic after a cursor; an empty cursor starts from the
// oldest event the topic retains. It also answers a Lagged frame: subscribe
// again with that frame's cursor.
func (s *Stream) Subscribe(topic, cursor string) error {
	return s.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Subscribe{
		Subscribe: &daemonv1.Subscribe{Topic: topic, Cursor: cursor}}})
}

// Unsubscribe stops a topic.
func (s *Stream) Unsubscribe(topic string) error {
	return s.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Unsubscribe{
		Unsubscribe: &daemonv1.Unsubscribe{Topic: topic}}})
}

// Ping asks the server to answer with the same nonce.
func (s *Stream) Ping(nonce string) error {
	return s.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{Nonce: nonce}}})
}

// Recv returns the next frame and remembers each topic's position.
func (s *Stream) Recv() (*daemonv1.StreamFrame, error) {
	op, data, err := s.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	frame, err := decodeFrame(op, data)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if event := frame.GetEvent(); event != nil {
		s.cursors[event.GetTopic()] = event.GetCursor()
	}
	if lagged := frame.GetLagged(); lagged != nil {
		s.cursors[lagged.GetTopic()] = lagged.GetCursor()
	}
	return frame, nil
}

// Cursor is the position after the last frame received for a topic. A new
// connection resumes by subscribing from it.
func (s *Stream) Cursor(topic string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[topic]
}

// Cursors is every topic's position.
func (s *Stream) Cursors() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.cursors)
}

// Close ends the connection.
func (s *Stream) Close() error {
	return s.conn.Close(CloseNormal, "")
}
