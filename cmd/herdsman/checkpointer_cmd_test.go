package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
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
	if _, e, code := call(t, "checkpointer", "--probe"); code != 2 || e != "herdsman checkpointer: TARGET: required, the orchestrator pane id (got \"\")\n" {
		t.Errorf("no TARGET: %d %q", code, e)
	}
	t.Setenv("TARGET", "w9:p9")
	t.Setenv("HERDSMAN_ORCHESTRATOR", "o")
	t.Setenv("HERDSMAN_INBOX", "")
	if _, e, code := call(t, "checkpointer", "--once"); code != 2 || e != "herdsman checkpointer: HERDSMAN_INBOX: required in adjutant mode (HERDSMAN_ORCHESTRATOR is set) (got \"\")\n" {
		t.Errorf("no inbox: %d %q", code, e)
	}
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
		if _, e, code := call(t, args...); code != 2 || e != "herdsman checkpointer: HEALTH_AFTER: a whole number from 1 to 86400 (got \"bad\\nline\")\n" {
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
