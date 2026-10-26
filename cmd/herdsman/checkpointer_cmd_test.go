package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

func TestCheckpointerCommandLine(t *testing.T) {
	out, _, _ := call(t, "help")
	if !strings.Contains(out, "\n  checkpointer ") {
		t.Error("help does not list checkpointer")
	}
	t.Setenv("TARGET", "")
	for _, args := range [][]string{{"checkpointer", "--now"}, {"checkpointer", "--once", "x"}} {
		if _, e, code := call(t, args...); code != 2 || !strings.HasPrefix(e, "usage: TARGET=<pane> herdsman checkpointer") {
			t.Errorf("%v: %d %q", args, code, e)
		}
	}
	if _, e, code := call(t, "checkpointer", "--probe"); code != 2 || e != "herdsman checkpointer: TARGET: required, not set (got \"\")\n" {
		t.Errorf("no TARGET: %d %q", code, e)
	}
	t.Setenv("TARGET", "w9:p9")
	t.Setenv("HERDSMAN_ORCHESTRATOR", "o")
	t.Setenv("HERDSMAN_INBOX", "")
	// A missing inbox is the shared required line on every path that needs
	// one: once, the loop and the probe in adjutant mode, and the guard;
	// empty and unset alike, nothing on stdout, exit 2.
	for _, unset := range []bool{false, true} {
		if unset {
			if err := os.Unsetenv("HERDSMAN_INBOX"); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"checkpointer", "--once"}, {"checkpointer"}, {"checkpointer", "--probe"},
			{"checkpointer", "guard", "o", "scratch-pane"}} {
			if out, e, code := call(t, args...); code != 2 || out != "" || e != "herdsman checkpointer: HERDSMAN_INBOX: required, not set (got \"\")\n" {
				t.Errorf("no inbox (unset %v) %v: %d %q %q", unset, args, code, out, e)
			}
		}
	}
	t.Setenv("HERDSMAN_INBOX", "")
	t.Setenv("HERDSMAN_ORCHESTRATOR", "")
	t.Setenv("INTERVAL", "soon")
	if _, e, code := call(t, "checkpointer"); code != 2 || !strings.HasPrefix(e, "herdsman checkpointer: INTERVAL: ") || !strings.HasSuffix(e, "(got \"soon\")\n") {
		t.Errorf("bad INTERVAL: %d %q", code, e)
	}
	out, _, code := call(t, "checkpointer", "--help")
	if code != 0 || !strings.Contains(out, "Exit codes:") {
		t.Errorf("help: %d %q", code, out)
	}
}

// The guard and the checkpointer through the binary, on a fake herdr; the
// zsh pane-run form of checkpointer-tests.sh: a prompt with a space
// reaches the target whole.
func TestCheckpointerCommands(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	t.Setenv("HERDR_SOCKET_PATH", srv.Socket)
	box := inboxtest.New(t)
	box.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	fx.SetAgent("receiver-pane", herdrtest.Agent("receiver-pane", "receiver-session"))
	for _, kv := range box.Env() {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	out, e, code := call(t, "checkpointer", "guard", inboxtest.Orchestrator, "receiver-pane")
	if out != "OK route-orchestrator receiver-pane\n" || e != "" || code != 0 {
		t.Errorf("guard: %q %q %d", out, e, code)
	}
	if _, e, code := call(t, "checkpointer", "guard", inboxtest.Orchestrator, "sender-pane"); code != 2 || e != "pane is not the approved orchestrator pane; no input\n" {
		t.Errorf("guard wrong pane: %q %d", e, code)
	}

	dir := t.TempDir()
	tr := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"assistant","message":{"usage":{"input_tokens":5}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TARGET", "receiver-pane")
	t.Setenv("TRANSCRIPT", tr)
	t.Setenv("LOG", filepath.Join(dir, "log"))
	t.Setenv("PROMPT", "/save-memory now")
	t.Setenv("LIMIT", "1")
	out, _, code = call(t, "checkpointer", "--probe")
	if code != 0 || !strings.Contains(out, tr+" context=5") {
		t.Errorf("probe: %d %q", code, out)
	}
	out, _, code = call(t, "checkpointer", "--probe", "--json")
	if code != 0 || out != `{"why":"TRANSCRIPT is set; the file is used whatever the session id says","hint":"","file":"`+tr+`","context":5}`+"\n" {
		t.Errorf("probe --json: %d %q", code, out)
	}
	t.Setenv("TRANSCRIPT", filepath.Join(dir, "none.jsonl"))
	out, _, _ = call(t, "checkpointer", "--probe", "--json")
	if !strings.Contains(out, `"file":"","context":null}`) {
		t.Errorf("probe --json, no file: %q", out)
	}
	write := func(text string) {
		if err := os.WriteFile(tr, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRANSCRIPT", tr)
	write("bad\n")
	out, _, _ = call(t, "checkpointer", "--probe", "--json")
	if !strings.Contains(out, `"context":null}`) {
		t.Errorf("probe --json, no reading: %q", out)
	}
	write(`{"type":"assistant","message":{"usage":{"input_tokens":5}}}` + "\n")
	checkpointerWorkingTimeout = 20 * time.Millisecond
	defer func() { checkpointerWorkingTimeout = 0 }()
	out, _, code = call(t, "checkpointer", "--once")
	if code != 0 || !strings.Contains(out, "sending /compact") {
		t.Errorf("once: %d %q", code, out)
	}
	var prompts []string
	for _, q := range srv.Requests() {
		if q.Method == "agent.prompt" {
			prompts = append(prompts, q.Params["text"].(string))
		}
	}
	if strings.Join(prompts, "|") != "/save-memory now|/compact" {
		t.Errorf("zsh pane-run form keeps a quoted prompt: %q", prompts)
	}

	// Inbox settings are checked at start and passed on canonical: an
	// approved checkpoint with LOCK_WAIT=06 runs; a bad HEALTH_AFTER is one
	// line and exit 2, for the checkpointer and its guard.
	t.Setenv("HERDSMAN_ORCHESTRATOR", inboxtest.Orchestrator)
	t.Setenv("LOCK_WAIT", "06")
	if out, e, code := call(t, "checkpointer", "--once"); code != 0 || !strings.Contains(out, "sending /compact") {
		t.Errorf("LOCK_WAIT=06: %d %q %q", code, out, e)
	}
	t.Setenv("LOCK_WAIT", "")
	t.Setenv("HEALTH_AFTER", "bad\nline")
	for _, args := range [][]string{{"checkpointer", "--once"}, {"checkpointer", "guard", inboxtest.Orchestrator, "receiver-pane"}} {
		if _, e, code := call(t, args...); code != 2 || e != "herdsman checkpointer: HEALTH_AFTER: not a whole number (got \"bad\\nline\")\n" {
			t.Errorf("%v HEALTH_AFTER: %d %q", args, code, e)
		}
	}
	t.Setenv("HEALTH_AFTER", "")
	t.Setenv("HERDSMAN_ORCHESTRATOR", "")

	// A signal ends a run: --once blocked on a busy orchestrator, and the
	// loop after its first checkpoint; each exits 128+n.
	signalAfter := func(ready func() bool) {
		go func() {
			for !ready() {
				time.Sleep(5 * time.Millisecond)
			}
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		}()
	}
	fx.Edit("receiver-pane", "agent_status", "blocked")
	signalAfter(func() bool { return srv.Subscribers() > 0 })
	out, _, code = call(t, "checkpointer", "--once")
	if code != 129 || !strings.Contains(out, "agent wait failed: context canceled") {
		t.Errorf("once interrupted: %d %q", code, out)
	}
	fx.Edit("receiver-pane", "agent_status", "idle")

	// A signal during the send in adjutant mode only cancels the run: the
	// input lock stays held until the send has returned, then is freed.
	t.Setenv("HERDSMAN_ORCHESTRATOR", inboxtest.Orchestrator)
	lock := filepath.Join(os.Getenv("HERDSMAN_INBOX"), inbox.MutationLock)
	freed, held := false, false
	checkpointerPrompt = func(ctx context.Context, _, _ string) error {
		_, err := os.Stat(lock)
		held = err == nil
		_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(lock); err != nil {
				freed = true
			}
		}
		return ctx.Err()
	}
	_, _, code = call(t, "checkpointer", "--once")
	checkpointerPrompt = nil
	if _, err := os.Stat(lock); code != 129 || !held || freed || err == nil {
		t.Errorf("signal during send: code %d, held %v, freed during send %v, lock after %v", code, held, freed, err)
	}
	t.Setenv("HERDSMAN_ORCHESTRATOR", "")

	t.Setenv("INTERVAL", "1")
	sent := func() int {
		n := 0
		for _, q := range srv.Requests() {
			if q.Method == "agent.prompt" {
				n++
			}
		}
		return n
	}
	before := sent()
	signalAfter(func() bool { return sent() >= before+2 })
	out, _, code = call(t, "checkpointer")
	if code != 129 || !strings.HasPrefix(out[9:], "checkpointer on receiver-pane: every 1s, compact at >= 1 tokens\n") ||
		!strings.Contains(out, "sending memory checkpoint") {
		t.Errorf("loop: %d %q", code, out)
	}
}

// blockedOutput holds the first "sending memory checkpoint" line until
// released, which pauses the input inside its locked critical section.
type blockedOutput struct {
	ready, unblock chan struct{}
	once           sync.Once
}

func (w *blockedOutput) Write(b []byte) (int, error) {
	if strings.Contains(string(b), "sending memory checkpoint") {
		w.once.Do(func() { close(w.ready) })
		<-w.unblock
	}
	return len(b), nil
}

// adjutantRun sets up an adjutant-mode checkpointer environment on a fake
// herdr and returns the inbox's input lock path.
func adjutantRun(t *testing.T, lockWait string) string {
	t.Helper()
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	box := inboxtest.New(t)
	box.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	fx.SetAgent("receiver-pane", herdrtest.Agent("receiver-pane", "receiver-session"))
	for _, kv := range box.Env() {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	tr := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"assistant","message":{"usage":{"input_tokens":7}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"HERDR_SOCKET_PATH": srv.Socket, "TARGET": "receiver-pane", "HERDSMAN_ORCHESTRATOR": inboxtest.Orchestrator,
		"TRANSCRIPT": tr, "LOG": filepath.Join(dir, "log"), "INTERVAL": "900", "LOCK_WAIT": lockWait} {
		t.Setenv(k, v)
	}
	return filepath.Join(box.Dir, inbox.MutationLock)
}

// A signal while an input is inside its locked critical section only
// cancels the run: no second writer can take the input lock until that
// input has unwound, and the run then frees it and exits 128+n.
func TestCheckpointerSignalKeepsInputLock(t *testing.T) {
	lock := adjutantRun(t, "1")
	out := &blockedOutput{ready: make(chan struct{}), unblock: make(chan struct{})}
	done := make(chan int, 1)
	go func() { done <- runCheckpointer([]string{"--once"}, nil, out, io.Discard) }()
	select {
	case <-out.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("input did not reach the blocked output")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("no input lock before the signal")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	second := inbox.TryDir(lock, 1, func(time.Duration) {})
	if second != nil {
		_ = second.Release()
		t.Error("a second writer took the input lock while the first input was still active")
	}
	close(out.unblock)
	select {
	case code := <-done:
		if code != 143 {
			t.Errorf("signal exit %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not exit")
	}
	if _, err := os.Stat(lock); err == nil {
		t.Error("the input lock was not freed after the run")
	}
}

// A signal while the input lock stays busy ends the lock wait at once
// (LOCK_WAIT=60 would otherwise hold the exit for about a minute); the
// other writer's lock stays in place and nothing is sent.
func TestCheckpointerSignalDuringBusyLock(t *testing.T) {
	lock := adjutantRun(t, "60")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	done := make(chan int, 1)
	go func() { done <- runCheckpointer([]string{"--once"}, nil, &stdout, io.Discard) }()
	time.Sleep(1500 * time.Millisecond) // inside the lock wait
	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 143 || time.Since(start) > 2*time.Second {
			t.Errorf("busy lock: exit %d after %v", code, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run kept waiting for the busy lock after the signal")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Error("the other writer's lock was removed")
	}
	if strings.Contains(stdout.String(), "sending") {
		t.Errorf("sent input without the lock: %q", stdout.String())
	}
}
