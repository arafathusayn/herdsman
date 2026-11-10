package hc_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
)

// privateCore starts a core of the installed hc in a session of its own, with
// its runtime, state, data and config directories under a temporary folder,
// and stops it when the test ends. The user's own core is never touched.
func privateCore(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("hc")
	if err != nil {
		t.Skip("hc is not installed")
	}
	// Under /tmp, because core refuses socket paths over 103 bytes and the
	// holder sockets are longer than the session's.
	dir, err := os.MkdirTemp("/tmp", "hcl")
	if err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("hsl%d", os.Getpid())
	env := []string{"HC_SESSION=" + session}
	for _, kv := range [][2]string{{"XDG_RUNTIME_DIR", "run"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CONFIG_HOME", "config"}} {
		p := filepath.Join(dir, kv[1])
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, kv[0]+"="+p)
	}
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		switch name {
		case "HC_SESSION", "HC_SOCKET", "HC_TERMINAL_ID", "HC_TERMINAL_SOCKET", "XDG_RUNTIME_DIR", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME":
			continue
		}
		env = append(env, e)
	}
	hcCmd := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	if out, err := hcCmd("server", "start", "--json"); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("start a private core: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := hcCmd("server", "stop", "--json"); err != nil {
			t.Errorf("stop the private core: %v\n%s", err, out)
		}
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "run", "herdr-core", session+".sock")
}

// TestLiveCore runs the listed methods against a real core. It is opt-in:
// HERDSMAN_HC_LIVE=1 go test ./internal/hc -run TestLiveCore
func TestLiveCore(t *testing.T) {
	if os.Getenv("HERDSMAN_HC_LIVE") != "1" {
		t.Skip("set HERDSMAN_HC_LIVE=1 to run against a private core of the installed hc")
	}
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
