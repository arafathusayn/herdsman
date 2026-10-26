// Package herdrtest runs a fake herdr socket server for tests. It answers
// from a scripted snapshot and records every request, so tests never reach
// the user's live herdr session.
package herdrtest

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Request is one recorded request.
type Request struct {
	Method string
	Params map[string]any
}

// Handler answers one method; it returns the result object or an error code.
type Handler func(params map[string]any) (result any, errCode string)

type Server struct {
	Socket string

	mu       sync.Mutex
	requests []Request
	handlers map[string]Handler
	subs     []net.Conn
	ln       net.Listener
	wg       sync.WaitGroup
}

// Start listens on a fresh socket that is closed when the test ends. The
// directory is short because macOS limits socket paths to 104 bytes.
func Start(t testing.TB) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Socket: sock, ln: ln, handlers: map[string]Handler{}}
	s.Handle("ping", func(map[string]any) (any, string) {
		return map[string]any{"type": "pong", "version": "0.9.4", "protocol": 22}, ""
	})
	s.Handle("events.subscribe", func(map[string]any) (any, string) {
		return map[string]any{"type": "subscription_started"}, ""
	})
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for _, c := range s.subs {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		_ = os.RemoveAll(dir)
	})
	return s
}

// Handle sets the answer for a method.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Requests returns a copy of the requests so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Methods returns the method of each request so far, skipping reads that
// pollers repeat (snapshot, ping, subscribe, pane.read).
func (s *Server) Methods() []string {
	var out []string
	for _, r := range s.Requests() {
		switch r.Method {
		case "session.snapshot", "ping", "events.subscribe", "pane.read":
			continue
		}
		out = append(out, r.Method)
	}
	return out
}

// Emit sends one event line to every open subscription.
func (s *Server) Emit(event string, data any) {
	b, _ := json.Marshal(map[string]any{"event": event, "data": data})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.subs {
		_, _ = c.Write(append(b, '\n'))
	}
}

// Subscribers returns how many subscriptions are open.
func (s *Server) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.conn(c)
	}
}

func (s *Server) conn(c net.Conn) {
	defer s.wg.Done()
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = c.Close()
		return
	}
	var req struct {
		ID     string         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		_ = c.Close()
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: req.Method, Params: req.Params})
	h := s.handlers[req.Method]
	s.mu.Unlock()

	resp := map[string]any{"id": req.ID}
	if h == nil {
		resp["error"] = map[string]string{"code": "unknown_method", "message": req.Method}
	} else if result, code := h(req.Params); code != "" {
		resp["error"] = map[string]string{"code": code, "message": code}
	} else {
		resp["result"] = result
	}
	b, _ := json.Marshal(resp)
	if _, err := c.Write(append(b, '\n')); err != nil {
		_ = c.Close()
		return
	}
	if req.Method == "events.subscribe" {
		s.mu.Lock()
		s.subs = append(s.subs, c)
		s.mu.Unlock()
		// Hold the connection until the client hangs up.
		_, _ = r.ReadBytes('\n')
		s.mu.Lock()
		for i, x := range s.subs {
			if x == c {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
	_ = c.Close()
}

// ForgetRequests drops the request log, so a long loop test measures the
// code under test, not the fake's own record.
func (s *Server) ForgetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// Hangup closes every open subscription, as a restarting server would, so
// tests can drive the client's reconnects.
func (s *Server) Hangup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.subs {
		_ = c.Close()
	}
}
