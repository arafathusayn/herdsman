package hc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// DefaultMaxFrame bounds one message in either direction. The largest answer
// Herdsman asks for is machine.exec: 8 MB of stdout and 1 MB of stderr, both
// base64, so about 12 MB of JSON. A snapshot of a busy core is far below that.
const DefaultMaxFrame = 16 << 20

// ErrFrameTooLarge is returned when a message is longer than the bound.
var ErrFrameTooLarge = errors.New("hc: message longer than the bound")

// frameKind tells a response from a notification.
type frameKind int

const (
	frameResponse frameKind = iota + 1
	frameNotification
)

// frame is one parsed JSON-RPC 2.0 message from core.
type frame struct {
	kind frameKind
	// id is the request id of a response. nullID marks a response core could
	// not tie to a request (a parse error); it carries an error.
	id     uint64
	nullID bool
	result json.RawMessage
	err    *wireError
	method string
	params json.RawMessage
}

type wireFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  *string         `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *wireError      `json:"error"`
}

type wireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// FrameError is a message that is not a JSON-RPC 2.0 message Herdsman can use.
type FrameError struct{ Reason string }

func (e *FrameError) Error() string { return "hc: bad message: " + e.Reason }

func badFrame(format string, a ...any) error {
	return &FrameError{Reason: fmt.Sprintf(format, a...)}
}

// parseFrame parses one line (with or without its newline). It accepts only
// what core sends: a response with a numeric id and exactly one of result and
// error, or a notification with a method and no id.
func parseFrame(line []byte) (frame, error) {
	line = bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		return frame{}, badFrame("empty line")
	}
	var w wireFrame
	if err := json.Unmarshal(line, &w); err != nil {
		return frame{}, badFrame("not a JSON object: %v", err)
	}
	if w.JSONRPC != "2.0" {
		return frame{}, badFrame("jsonrpc is %q, not 2.0", w.JSONRPC)
	}
	if w.Method != nil {
		if *w.Method == "" {
			return frame{}, badFrame("empty method")
		}
		if len(w.ID) > 0 && string(w.ID) != "null" {
			return frame{}, badFrame("core sent a request (%s), not a notification", *w.Method)
		}
		if len(w.Result) > 0 || w.Error != nil {
			return frame{}, badFrame("notification %s carries a result or an error", *w.Method)
		}
		return frame{kind: frameNotification, method: *w.Method, params: w.Params}, nil
	}
	hasResult := len(w.Result) > 0
	hasError := w.Error != nil
	if hasResult == hasError {
		return frame{}, badFrame("a response needs exactly one of result and error")
	}
	f := frame{kind: frameResponse, result: w.Result, err: w.Error}
	switch {
	case len(w.ID) == 0:
		return frame{}, badFrame("response without an id")
	case string(w.ID) == "null":
		if !hasError {
			return frame{}, badFrame("result with a null id")
		}
		f.nullID = true
	default:
		id, err := strconv.ParseUint(string(w.ID), 10, 64)
		if err != nil {
			return frame{}, badFrame("id %s is not a request id of this client", w.ID)
		}
		f.id = id
	}
	return f, nil
}

// readFrame reads one newline-terminated line of at most max bytes,
// newline included.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > max {
			return nil, ErrFrameTooLarge
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
