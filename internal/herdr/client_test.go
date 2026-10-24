package herdr_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func TestCheckAcceptsKnownProtocol(t *testing.T) {
	srv := herdrtest.Start(t)
	pong, err := herdr.New(srv.Socket).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pong.Version != "0.9.4" || pong.Protocol != herdr.Protocol {
		t.Fatalf("pong = %+v", pong)
	}
}

func TestCheckRefusesOtherProtocol(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("ping", func(map[string]any) (any, string) {
		return map[string]any{"version": "9.9.9", "protocol": herdr.Protocol + 1}, ""
	})
	_, err := herdr.New(srv.Socket).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("err = %v, want a protocol error naming the version", err)
	}
}

func TestCallReturnsServerError(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("pane.close", func(map[string]any) (any, string) { return nil, "pane_not_found" })
	err := herdr.New(srv.Socket).Call(context.Background(), "pane.close", map[string]any{"pane_id": "x"}, nil)
	he, ok := errors.AsType[*herdr.Error](err)
	if !ok || he.Code != "pane_not_found" {
		t.Fatalf("err = %v, want pane_not_found", err)
	}
}

func TestSnapshotAndReadPane(t *testing.T) {
	srv := herdrtest.Start(t)
	srv.Handle("session.snapshot", func(map[string]any) (any, string) {
		return map[string]any{"snapshot": map[string]any{
			"version": "0.9.4", "protocol": 22,
			"agents": []map[string]any{{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "name": "im-a", "agent_status": "working"}},
		}}, ""
	})
	srv.Handle("pane.read", func(p map[string]any) (any, string) {
		text := "hello"
		switch {
		case p["source"] == "visible" && p["lines"] == nil:
			text = "screen"
		case p["source"] != "recent_unwrapped" || p["lines"] != float64(10):
			return nil, "bad_params"
		}
		if p["format"] == "ansi" && p["strip_ansi"] == false {
			text = "\x1b[31m" + text + "\x1b[0m"
		} else if p["format"] != nil || p["strip_ansi"] != true {
			return nil, "bad_params"
		}
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": text}}, ""
	})
	srv.Handle("pane.get", func(p map[string]any) (any, string) {
		return map[string]any{"type": "pane_info", "pane": map[string]any{
			"pane_id": p["pane_id"], "agent": "claude",
			"scroll": map[string]any{"offset_from_bottom": 3, "max_offset_from_bottom": 270, "viewport_rows": 39},
		}}, ""
	})
	c := herdr.New(srv.Socket)
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || *snap.Agents[0].Name != "im-a" || snap.Agents[0].Status != "working" {
		t.Fatalf("agents = %+v", snap.Agents)
	}
	cases := []struct {
		o    herdr.ReadOptions
		want string
	}{
		{herdr.ReadOptions{Lines: 10}, "hello"},
		{herdr.ReadOptions{Lines: 10, ANSI: true}, "\x1b[31mhello\x1b[0m"},
		{herdr.ReadOptions{Lines: 10, Visible: true, ANSI: true}, "\x1b[31mscreen\x1b[0m"},
	}
	for _, tc := range cases {
		read, err := c.ReadPane(context.Background(), "w1:p1", tc.o)
		if err != nil || read.Text != tc.want {
			t.Fatalf("read %+v = %+v, %v; want %q", tc.o, read, err, tc.want)
		}
	}
	p, err := c.Pane(context.Background(), "w1:p1")
	if err != nil || p.ID != "w1:p1" || p.Scroll == nil || *p.Scroll != (herdr.Scroll{Offset: 3, Max: 270, Rows: 39}) {
		t.Fatalf("pane = %+v (scroll %+v), %v", p, p.Scroll, err)
	}
}

func TestSubscribeDeliversEventsAndStopsOnCancel(t *testing.T) {
	srv := herdrtest.Start(t)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan herdr.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- herdr.New(srv.Socket).Subscribe(ctx, []map[string]any{{"type": "pane.created"}}, func(e herdr.Event) { got <- e })
	}()
	waitFor(t, func() bool { return srv.Subscribers() == 1 })
	srv.Emit("pane_created", map[string]any{"pane": map[string]any{"pane_id": "w1:p2"}})
	select {
	case e := <-got:
		if e.Event != "pane_created" {
			t.Fatalf("event = %q", e.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after cancel")
	}
}

// A peer that never ends its line must not make the client buffer forever.
func TestCallRejectsOversizedLine(t *testing.T) {
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		chunk := []byte(strings.Repeat("x", 1<<20))
		for range 40 {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	err = herdr.New(sock).Call(context.Background(), "ping", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v, want a too-long error", err)
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "")
	cases := []struct {
		name, env, session, envSession, want string
	}{
		{name: "default", want: "/cfg/herdr/herdr.sock"},
		{name: "named", session: "work", want: "/cfg/herdr/sessions/work/herdr.sock"},
		{name: "session env", envSession: "env", want: "/cfg/herdr/sessions/env/herdr.sock"},
		{name: "socket env", env: "/run/h.sock", want: "/run/h.sock"},
		{name: "flag beats socket env", env: "/run/h.sock", session: "work", want: "/cfg/herdr/sessions/work/herdr.sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_SOCKET_PATH", tc.env)
			t.Setenv("HERDR_SESSION", tc.envSession)
			got, err := herdr.SocketPath(tc.session)
			if err != nil || got != tc.want {
				t.Fatalf("SocketPath(%q) = %q, %v; want %q", tc.session, got, err, tc.want)
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
