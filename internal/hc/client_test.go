package hc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
)

func TestSocketPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	cases := []struct {
		name, session                 string
		socket, hcSession, xdg, state string
		want                          string
	}{
		{name: "HC_SOCKET wins", socket: "/s/core.sock", xdg: "/run", want: "/s/core.sock"},
		{name: "a named session ignores HC_SOCKET", session: "work", socket: "/s/core.sock", xdg: "/run", want: "/run/herdr-core/work.sock"},
		{name: "runtime dir", hcSession: "probe", xdg: "/run", want: "/run/herdr-core/probe.sock"},
		{name: "state dir", state: "/state", want: "/state/herdr-core/default/default.sock"},
		{name: "home state dir", hcSession: "s", want: filepath.Join(home, ".local", "state", "herdr-core", "s", "s.sock")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HC_SOCKET", c.socket)
			t.Setenv("HC_SESSION", c.hcSession)
			t.Setenv("XDG_RUNTIME_DIR", c.xdg)
			t.Setenv("XDG_STATE_HOME", c.state)
			got, err := hc.SocketPath(c.session)
			if err != nil || got != c.want {
				t.Fatalf("SocketPath(%q) = %q, %v; want %q", c.session, got, err, c.want)
			}
		})
	}
}

// rawCore answers hello like hc 0.1.20 and hands every later request line to
// fn, which writes what it likes.
func rawCore(t *testing.T, fn func(w net.Conn, line []byte)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hcr")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
		_ = os.RemoveAll(dir)
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req struct {
						ID     uint64 `json:"id"`
						Method string `json:"method"`
					}
					_ = json.Unmarshal(line, &req)
					if req.Method == hc.MethodHello {
						b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
							"generation": 1, "version": "0.1.20", "core": map[string]any{"id": "raw", "name": "raw"},
							"framed": false, "methods": hctest.Methods, "features": hctest.Features,
						}})
						_, _ = c.Write(append(b, '\n'))
						continue
					}
					fn(c, line)
				}
			}()
		}
	}()
	return sock
}

func requestID(line []byte) uint64 {
	var req struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(line, &req)
	return req.ID
}

func answer(w net.Conn, id uint64, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = w.Write(append(b, '\n'))
}

func TestCallEndsAtTheContextDeadline(t *testing.T) {
	sock := rawCore(t, func(net.Conn, []byte) {}) // never answers
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Get(ctx, "t1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get: %v", err)
	}
	// A context without a deadline gets the client's timeout.
	c2, err := hc.Dial(testCtx(t), sock, hc.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := c2.Get(context.Background(), "t1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get without a deadline: %v", err)
	}
}

func TestDialEndsAtTheContextDeadline(t *testing.T) {
	dir, err := os.MkdirTemp("", "hcs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() // accepts by backlog and never answers hello
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := hc.Dial(ctx, filepath.Join(dir, "s.sock"), hc.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial: %v", err)
	}
}

func TestAnswersAreMatchedByID(t *testing.T) {
	var mu sync.Mutex
	var held [][]byte
	sock := rawCore(t, func(w net.Conn, line []byte) {
		mu.Lock()
		defer mu.Unlock()
		held = append(held, line)
		if len(held) < 2 {
			return
		}
		// Answer in reverse order: each call must still get its own answer.
		for i := len(held) - 1; i >= 0; i-- {
			var req struct {
				Params struct {
					Terminal string `json:"terminal"`
				} `json:"params"`
			}
			_ = json.Unmarshal(held[i], &req)
			answer(w, requestID(held[i]), map[string]any{"text": "screen of " + req.Params.Terminal, "complete": true, "how": "screen"})
		}
	})
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for _, term := range []string{"t1", "t2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Read(testCtx(t), term, 0)
			if err != nil || r.Text != "screen of "+term {
				t.Errorf("read %s = %q, %v", term, r.Text, err)
			}
		}()
	}
	wg.Wait()
}

func TestOversizedAnswerEndsTheConnection(t *testing.T) {
	sock := rawCore(t, func(w net.Conn, line []byte) {
		answer(w, requestID(line), map[string]any{"text": strings.Repeat("x", 4096), "complete": true, "how": "screen"})
	})
	c, err := hc.Dial(testCtx(t), sock, hc.Options{MaxFrame: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Read(testCtx(t), "t1", 0); !errors.Is(err, hc.ErrFrameTooLarge) {
		t.Fatalf("read: %v", err)
	}
	<-c.Done()
	if _, err := c.Get(testCtx(t), "t1"); !errors.Is(err, hc.ErrClosed) {
		t.Fatalf("get after the connection ended: %v", err)
	}
}

func TestOversizedRequestIsNotSent(t *testing.T) {
	core := hctest.Start(t)
	c, err := hc.Dial(testCtx(t), core.Socket, hc.Options{MaxFrame: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Label(testCtx(t), "t1", map[string]string{"k": strings.Repeat("v", 2048)}); !errors.Is(err, hc.ErrFrameTooLarge) {
		t.Fatalf("label: %v", err)
	}
	if core.Calls(hc.MethodLabel) != 0 {
		t.Fatal("an oversized request reached core")
	}
	if _, err := c.ServerStatus(testCtx(t)); err != nil {
		t.Fatalf("the connection did not survive a refused request: %v", err)
	}
}

func TestParseErrorWithoutIDEndsTheConnection(t *testing.T) {
	sock := rawCore(t, func(w net.Conn, _ []byte) {
		_, _ = w.Write([]byte(`{"error":{"code":-32700,"data":{"hint":"see hc schema","kind":"parse_error"},"message":"bad"},"id":null,"jsonrpc":"2.0"}` + "\n"))
	})
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Get(testCtx(t), "t1")
	if !errors.Is(err, hc.ErrClosed) || !hc.IsKind(err, hc.KindParseError) {
		t.Fatalf("get: %v", err)
	}
}

func TestMalformedAnswerEndsTheConnection(t *testing.T) {
	sock := rawCore(t, func(w net.Conn, _ []byte) { _, _ = w.Write([]byte("{\"jsonrpc\":\"1.0\"}\n")) })
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var fe *hc.FrameError
	if _, err := c.Get(testCtx(t), "t1"); !errors.As(err, &fe) {
		t.Fatalf("get: %v", err)
	}
}

func TestMissingRequiredFieldInAnAnswer(t *testing.T) {
	sock := rawCore(t, func(w net.Conn, line []byte) {
		answer(w, requestID(line), map[string]any{"key": "esc"}) // no outcome
	})
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var mf *hc.MissingFieldError
	if _, err := c.Interrupt(testCtx(t), "t1", 0); !errors.As(err, &mf) || mf.Field != "outcome" {
		t.Fatalf("interrupt: %v", err)
	}
}

func TestCloseFailsPendingCalls(t *testing.T) {
	sock := rawCore(t, func(net.Conn, []byte) {})
	c, err := hc.Dial(testCtx(t), sock, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Get(testCtx(t), "t1")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = c.Close()
	if err := <-done; !errors.Is(err, hc.ErrClosed) {
		t.Fatalf("pending get after close: %v", err)
	}
}
