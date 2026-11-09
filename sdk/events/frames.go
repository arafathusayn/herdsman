// Package events is the client and the wire form of the daemon's live updates:
// a WebSocket of StreamFrame messages, and a unary long poll over the same
// topics and cursors.
package events

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/ws"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// Path is where the daemon upgrades a request to the stream.
	Path = rpc.BasePath + daemonv1.EventServiceName + "/Stream"
	// SubprotocolJSON selects JSON text frames; without it frames are binary
	// protobuf.
	SubprotocolJSON = "herdsman.json"
	// TopicRegistry carries the full list of routes each time it changes.
	TopicRegistry = "registry"
)

// Close codes a server ends a stream with.
const (
	CloseNormal         uint16 = ws.CloseNormal
	CloseGoingAway      uint16 = ws.CloseGoingAway
	ClosePolicyViolated uint16 = 1008
)

// maxClientFrame bounds what a server reads from one client message; clients
// only send short control frames.
const maxClientFrame = 64 << 10

const maxTopicLength = 256

var (
	topicPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]*(/[A-Za-z0-9._:-]+)*$`)
	cursorPattern = regexp.MustCompile(`^([0-9a-f]{32})-([0-9]{1,18})$`)
)

// RouteTopic is the topic of one route's events.
func RouteTopic(route string) string { return "route/" + route }

// ValidTopic reports whether a topic name is well formed.
func ValidTopic(topic string) bool {
	return len(topic) <= maxTopicLength && topicPattern.MatchString(topic)
}

// FormatCursor is the cursor of a position in a topic.
func FormatCursor(epoch string, seq int64) string {
	return epoch + "-" + strconv.FormatInt(seq, 10)
}

// ParseCursor reads a cursor made by FormatCursor.
func ParseCursor(cursor string) (epoch string, seq int64, ok bool) {
	match := cursorPattern.FindStringSubmatch(cursor)
	if match == nil {
		return "", 0, false
	}
	// At most 18 digits always fit an int64.
	seq, _ = strconv.ParseInt(match[2], 10, 64)
	return match[1], seq, true
}

// encodeFrame is one frame in the connection's encoding.
func encodeFrame(frame *daemonv1.StreamFrame, json bool) (ws.Opcode, []byte, error) {
	if json {
		data, err := protojson.Marshal(frame)
		return ws.OpText, data, err
	}
	data, err := proto.Marshal(frame)
	return ws.OpBinary, data, err
}

// decodeFrame reads one message by its own opcode. A field or frame kind from
// a later contract decodes to nothing rather than failing.
func decodeFrame(op ws.Opcode, data []byte) (*daemonv1.StreamFrame, error) {
	frame := new(daemonv1.StreamFrame)
	var err error
	if op == ws.OpText {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, frame)
	} else {
		err = proto.Unmarshal(data, frame)
	}
	if err != nil {
		return nil, errors.New("events: undecodable frame: " + err.Error())
	}
	return frame, nil
}

// AcceptOptions configure Accept.
type AcceptOptions struct {
	// CheckOrigin decides whether a request's origin may connect. Nil accepts
	// a request with no Origin or one matching its host.
	CheckOrigin func(*http.Request) bool
	// WriteTimeout bounds each frame write; zero takes the default.
	WriteTimeout time.Duration
}

// Conn is the server's side of one stream.
type Conn struct {
	conn *ws.Conn
	json bool
}

// Accept upgrades a request to a stream. On failure it has already answered.
func Accept(w http.ResponseWriter, r *http.Request, opts AcceptOptions) (*Conn, error) {
	conn, err := ws.Upgrade(w, r, ws.ServerOptions{
		Options: ws.Options{
			MaxMessageSize: maxClientFrame,
			WriteTimeout:   opts.WriteTimeout,
			Subprotocols:   []string{SubprotocolJSON},
		},
		CheckOrigin: opts.CheckOrigin,
	})
	if err != nil {
		return nil, err
	}
	return &Conn{conn: conn, json: conn.Subprotocol() == SubprotocolJSON}, nil
}

// Read returns the next frame from the client.
func (c *Conn) Read() (*daemonv1.StreamFrame, error) {
	op, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return decodeFrame(op, data)
}

// Write sends one frame to the client. Writes are serialized.
func (c *Conn) Write(frame *daemonv1.StreamFrame) error {
	op, data, err := encodeFrame(frame, c.json)
	if err != nil {
		return err
	}
	return c.conn.WriteMessage(op, data)
}

// Close ends the stream with a WebSocket close code.
func (c *Conn) Close(code uint16, reason string) error {
	return c.conn.Close(code, reason)
}
