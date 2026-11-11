package hc_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
)

// privateCore starts a private core of the installed hc (hclive) and returns
// its socket. The user's own core is never touched.
func privateCore(t *testing.T) string { return hclive.Start(t, hclive.Options{}).Socket }

// TestLiveCore runs the listed methods against a real core. It is opt-in:
// HERDSMAN_HC_LIVE=1 go test ./internal/hc -run TestLiveCore
func TestLiveCore(t *testing.T) {
	sock := privateCore(t)
	ctx := testCtx(t)
	c, err := hc.Dial(ctx, sock, hc.Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	t.Logf("core %s, hc %s, generation %d", c.Core().ID, c.Hello().Version, c.Hello().Generation)
	if _, err := hc.Dial(ctx, sock, hc.Options{ExpectCore: "not-this-core"}); err == nil {
		t.Fatal("a different expected core was accepted")
	}

	st, err := c.ServerStatus(ctx)
	if err != nil || st.Core.ID != c.Core().ID {
		t.Fatalf("server.status = %+v, %v", st, err)
	}

	labels := map[string]string{"herdsman.live": "1"}
	s, err := hc.Subscribe(ctx, sock, hc.Filter{Labels: labels}, hc.Options{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer s.Close()

	term, err := c.Run(ctx, hc.RunRequest{Argv: []string{"sh", "-c", "echo ready; sleep 60"}, Labels: labels, Name: "hs-live"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	u, err := s.Next(ctx)
	if err != nil || u.Event.Type != hc.EventCreated || u.Event.Terminal != term.Address {
		t.Fatalf("first event = %+v, %v", u, err)
	}
	if got, err := c.Get(ctx, "hs-live"); err != nil || got.Address != term.Address || got.PID == nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got, err := c.List(ctx, labels, false); err != nil || len(got.Terminals) != 1 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	var screen hc.ReadResult
	for range 50 {
		if screen, err = c.Read(ctx, term.Address, 10); err == nil && strings.Contains(screen.Text, "ready") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(screen.Text, "ready") {
		t.Fatalf("read = %+v, %v", screen, err)
	}
	if _, err := c.Label(ctx, term.Address, map[string]string{"herdsman.role": "probe"}); err != nil {
		t.Fatalf("label: %v", err)
	}
	if got, err := c.Keep(ctx, term.Address, true); err != nil || !got.Keep {
		t.Fatalf("keep = %+v, %v", got, err)
	}
	if got, err := c.Keep(ctx, term.Address, false); err != nil || got.Keep {
		t.Fatalf("keep off = %+v, %v", got, err)
	}
	// A shell reports no status, so a guarded send types nothing.
	if _, err := c.Send(ctx, term.Address, "echo typed", "idle"); !hc.IsKind(err, hc.KindStatusMismatch) {
		t.Fatalf("send: %v", err)
	}
	if got, err := c.Interrupt(ctx, term.Address, 1); err != nil || got.Key == "" || got.Outcome == "" {
		t.Fatalf("interrupt = %+v, %v", got, err)
	}
	if got, err := c.Exec(ctx, hc.ExecRequest{Argv: []string{"echo", "x"}}); err != nil || string(got.Stdout) != "x\n" {
		t.Fatalf("exec = %+v, %v", got, err)
	}
	if _, err := c.Get(ctx, "t99999"); !hc.IsKind(err, hc.KindNotFound) {
		t.Fatalf("get of a missing terminal: %v", err)
	}
	if _, err := c.Kill(ctx, term.Address); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// The events end with the terminal's end; the cache follows them.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, ok := s.Terminal(term.Address)
		if ok && !got.Live() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never saw the end: %+v", got)
		}
		u, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		t.Logf("event %d %s", u.Seq, u.Event.Type)
	}
	if inc := s.Incomplete(); len(inc) != 0 {
		t.Errorf("events left records incomplete: %v", inc)
	}
}
