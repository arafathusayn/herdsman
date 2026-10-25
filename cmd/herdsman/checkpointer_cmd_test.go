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
	// The orchestrator is blocked, so the idle wait holds the run until
	// the test frees it. A SIGHUP during the run exits 129.
	fx.Edit("receiver-pane", "agent_status", "blocked")
	exited := make(chan int, 1)
	checkpointerExit = func(c int) { exited <- c }
	defer func() { checkpointerExit = os.Exit }()
	checkpointerWorkingTimeout = 20 * time.Millisecond
	defer func() { checkpointerWorkingTimeout = 0 }()
	go func() {
		for srv.Subscribers() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		c := <-exited
		if c != 129 {
			t.Errorf("signal exit %d", c)
		}
		fx.Edit("receiver-pane", "agent_status", "idle")
		srv.Emit("pane.agent_status_changed", map[string]any{"pane_id": "receiver-pane"})
	}()
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
}
