package hc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
)

// A route's endpoint comes from the config or the default session, never from
// a surrounding agent's HC_SOCKET, HC_SESSION or HC_TERMINAL_ID.
func TestSessionSocketNeverReadsAnAgentsScope(t *testing.T) {
	env := map[string]string{"HC_SOCKET": "/agent/core.sock", "HC_SESSION": "agent", "HC_TERMINAL_ID": "t9", "XDG_RUNTIME_DIR": "/run"}
	getenv := func(k string) string { return env[k] }
	for session, want := range map[string]string{hc.DefaultSession: "/run/herdr-core/default.sock", "work": "/run/herdr-core/work.sock"} {
		got, err := hc.SessionSocket(session, getenv)
		if err != nil || got != want {
			t.Errorf("SessionSocket(%q) = %q, %v; want %q", session, got, err, want)
		}
	}
	delete(env, "XDG_RUNTIME_DIR")
	env["HOME"] = "/home/u"
	if got, err := hc.SessionSocket("work", getenv); err != nil || got != "/home/u/.local/state/herdr-core/work/work.sock" {
		t.Errorf("state dir: %q %v", got, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := hc.SessionSocket(bad, getenv); err == nil {
			t.Errorf("session %q was accepted", bad)
		}
	}
}

// connect_timeout bounds the dial and the hello: a socket that accepts and
// never answers is given up on in that time.
func TestConnectTimeoutBoundsTheHello(t *testing.T) {
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "mute.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	start := time.Now()
	_, err = hc.Dial(context.Background(), socket, hc.Options{ConnectTimeout: 150 * time.Millisecond, Timeout: time.Minute})
	if err == nil {
		t.Fatal("a mute core answered")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the hello waited %s with a 150ms connect timeout", took)
	}
}
