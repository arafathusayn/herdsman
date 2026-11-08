// Package mcp serves the herdsman contracts as Model Context Protocol tools.
// Every tool comes from a service descriptor, so a harness that speaks MCP gets
// the same operations, plans and failures as any other client of the daemon.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// latestProtocol is offered to a client that asks for a revision not served.
const latestProtocol = "2025-11-25"

var protocols = []string{latestProtocol, "2025-06-18", "2025-03-26", "2024-11-05"}

// The JSON-RPC error codes this server answers with.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeBusy           = -32000
	codeNotInitialized = -32002
)

// maxInFlight bounds concurrent tool calls. A call over it is refused rather
// than queued, so the reader is always free to take a cancellation.
const maxInFlight = 8

// Caller sends one request, as binary protobuf, to a procedure path of the
// daemon and returns the binary response.
type Caller func(ctx context.Context, procedure string, request []byte) ([]byte, error)

// Options configures a Server.
type Options struct {
	// Call reaches the daemon; required.
	Call Caller
	// Deny returns the deny list in force and is read on every call. An entry
	// matches a tool name, as a path.Match pattern, or a service/method name.
	// With nil or an error, every call is refused.
	Deny func() ([]string, error)
	// Version is reported to clients.
	Version string
}

// Server holds the tools. It keeps no per-client state, so Serve may run more
// than once.
type Server struct {
	options Options
	tools   []*tool
}

// NewServer builds the tools of files.
func NewServer(files []protoreflect.FileDescriptor, options Options) (*Server, error) {
	if options.Call == nil {
		return nil, errors.New("mcp: Options.Call is required")
	}
	tools, err := buildTools(files)
	if err != nil {
		return nil, err
	}
	return &Server{options: options, tools: tools}, nil
}

// Tools is the tool list, sorted by name.
func (s *Server) Tools() []Tool {
	out := make([]Tool, len(s.tools))
	for i, t := range s.tools {
		out[i] = t.Tool
	}
	return out
}

func (s *Server) find(name string) *tool {
	i := slices.IndexFunc(s.tools, func(t *tool) bool { return t.Name == name })
	if i < 0 {
		return nil
	}
	return s.tools[i]
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handle answers one line of input.
func (c *connection) handle(line []byte) {
	if line[0] == '[' {
		c.fail(nil, codeInvalidRequest, "batches are not supported")
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		c.fail(nil, codeParse, "the line is not JSON")
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		c.fail(req.ID, codeInvalidRequest, "not a JSON-RPC 2.0 request")
		return
	}
	if req.ID == nil {
		c.notification(req)
		return
	}
	if string(req.ID) == "null" {
		c.fail(nil, codeInvalidRequest, "a request id may not be null")
		return
	}
	switch req.Method {
	case "initialize":
		c.initialize(req)
	case "ping":
		c.reply(req.ID, struct{}{})
	case "tools/list", "tools/call":
		if !c.initialized {
			c.fail(req.ID, codeNotInitialized, "initialize first")
		} else if req.Method == "tools/list" {
			c.reply(req.ID, map[string]any{"tools": c.server.Tools()})
		} else {
			c.toolsCall(req)
		}
	default:
		c.fail(req.ID, codeMethodNotFound, "no method "+req.Method)
	}
}

func (c *connection) initialize(req request) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		c.fail(req.ID, codeInvalidParams, "initialize takes an object")
		return
	}
	version := latestProtocol
	if slices.Contains(protocols, params.ProtocolVersion) {
		version = params.ProtocolVersion
	}
	c.initialized = true
	c.reply(req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": "herdsman", "version": c.server.options.Version},
	})
}

func (c *connection) notification(req request) {
	if req.Method != "notifications/cancelled" {
		return
	}
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(req.Params, &params) == nil {
		c.cancel(params.RequestID)
	}
}

func (c *connection) toolsCall(req request) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		c.fail(req.ID, codeInvalidParams, "tools/call takes an object")
		return
	}
	t := c.server.find(params.Name)
	if t == nil {
		c.fail(req.ID, codeInvalidParams, "no tool "+params.Name)
		return
	}
	c.spawn(req.ID, t, params.Arguments)
}
