package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// coreDaemon starts a real daemon on a fake herdr-core world, in scratch
// homes: never the user's own core, app, herdr server or daemon.
func coreDaemon(t *testing.T) (*coretest.World, string, string) {
	t.Helper()
	base, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	home, private, repo := filepath.Join(base, "h"), filepath.Join(base, "p"), filepath.Join(base, "repo")
	for _, dir := range []string{home, repo} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// The deprecated experiment key remains accepted for older config files.
	optIn := "[runtime]\nbackend = \"herdr-core\"\n\n[runtime.core]\nexperimental = true\n"
	if err := os.WriteFile(filepath.Join(base, "herdsman.config.toml"), []byte(optIn), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "herdsman-bin")
	if err := os.WriteFile(bin, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := coretest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.AutoWrap(ctx)
	d := daemon.New(daemon.Config{Home: home, PrivateHome: private, Version: daemonVersion(), Executable: bin,
		HerdrSocket: filepath.Join(base, "no-herdr.sock"), Procs: func() ([]daemon.Proc, error) { return nil, nil },
		Core: daemon.CoreConfig{Socket: w.Core.Socket, App: coretest.AppRunner{W: w}, AppHome: w.AppHome, Procs: w.Procs, Peer: w.Peer,
			Environ:  []string{"PATH=/usr/bin:/bin", "HOME=" + home},
			Timeouts: core.Timeouts{Register: 5 * time.Second, Ready: 5 * time.Second, Visibility: 300 * time.Millisecond, Poll: 10 * time.Millisecond}, NoReconcile: true}})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	t.Setenv("HOME", home)
	t.Setenv("HERDSMAN_PRIVATE_HOME", private)
	t.Setenv("HERDSMAN_STATE_DIR", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Chdir(repo)
	return w, private, repo
}

// Exercise the production CLI dispatch, not a direct call to coreTask: a
// fresh installation must launch task agents on core without an opt-in file.
func TestNewAgentUsesDefaultCoreThroughTheCLI(t *testing.T) {
	w, _, repo := coreDaemon(t)
	w.OpenApp()
	if err := os.Remove(filepath.Join(filepath.Dir(repo), "herdsman.config.toml")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"branch", "-M", "main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	var out, errOut bytes.Buffer
	if code := runNew([]string{"probe", "--base", "main", "--no-setup", "--agent", `CORE_FIXTURE=ok claude --append-system-prompt "two words"`, "--json", "--", "fix the fixture"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("new failed (%d): %s %s", code, &out, &errOut)
	}
	var result struct {
		AgentName string `json:"agent_name"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.AgentName != "probe" {
		t.Fatalf("new did not launch its agent: %s (%v)", &out, err)
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer closeService()
	rows, err := service.ListExecutions(context.Background(), &daemonv1.RuntimeServiceListExecutionsRequest{})
	if err != nil || len(rows.GetExecutions()) != 1 {
		t.Fatalf("task has no durable execution: %v %v", rows, err)
	}
	rec := rows.GetExecutions()[0]
	if rec.GetRef().GetBackend() != "herdr-core" || rec.GetAppBinding().GetVisibility() != "verified" || rec.GetNotices().GetIssued() != 1 {
		t.Fatalf("task lacks core, main-app visibility or its notice: %v", rec)
	}
	if w.Core.Calls("terminal.send")+w.Core.Calls("terminal.keys") != 0 {
		t.Fatal("task launch typed into the terminal")
	}
}

// runtimeEventsOf reads the daemon's runtime topic through the SDK.
func runtimeEventsOf(t *testing.T, private string) []*daemonv1.RuntimeEvent {
	t.Helper()
	socket := filepath.Join(private, "run", "daemon.sock")
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	svc := daemonv1.NewEventServiceClient(&http.Client{Transport: &rpc.Transport{Base: tr}}, "http://herdsman")
	res, err := svc.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "runtime"}}})
	if err != nil {
		t.Fatal(err)
	}
	var out []*daemonv1.RuntimeEvent
	for _, ev := range res.GetEvents() {
		var re struct {
			Kind      string `json:"kind"`
			Reason    string `json:"reason"`
			Line      string `json:"line"`
			Execution string `json:"execution_id"`
		}
		if json.Unmarshal(ev.GetPayload(), &re) == nil {
			out = append(out, &daemonv1.RuntimeEvent{Kind: re.Kind, Reason: re.Reason, Line: re.Line, ExecutionId: re.Execution})
		}
	}
	return out
}

// up --backend herdr-core, end to end through the CLI: up names, before the
// start, the steps of the run that need a person (Codex is attended), halts
// at each person's wait with the reason, raises needs_attention when a step
// is due, and types nothing into core.
func TestUpOnCoreThroughTheCLI(t *testing.T) {
	w, private, _ := coreDaemon(t)
	w.OpenApp()
	up := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runUp(append([]string{"harbor", "--no-tui", "--backend", "herdr-core"}, args...), nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	code, out, errOut := up("--no-adjutant", "-o", "codex --no-daemon", "-p", "ship it")
	if code == 0 {
		t.Fatalf("a core start with a person's wait reported success:\n%s", out)
	}
	for _, want := range []string{"needs a person (codex, attended):", core.AttentionStartLine, core.AttentionInterrupted, core.AttentionGoalPaused} {
		if !strings.Contains(out, want) {
			t.Errorf("up did not name %q before the start:\n%s", want, out)
		}
	}
	if strings.Contains(out, core.AttentionNoticeWaiting+":") {
		t.Fatal("native queue wakes were reported as manual-only")
	}
	if !strings.Contains(errOut, "waiting for a person") || !strings.Contains(errOut, core.CodeNeedsAttention) {
		t.Fatalf("the halt: %q", errOut)
	}
	seen := false
	for _, ev := range runtimeEventsOf(t, private) {
		if ev.GetKind() == "needs_attention" && ev.GetReason() == core.AttentionStartLine && ev.GetLine() == core.CodexStartLine {
			seen = true
		}
	}
	if !seen {
		t.Errorf("no needs_attention start_line event: %+v", runtimeEventsOf(t, private))
	}
	// --continue picks the start up and says nothing again of the plan.
	code, out, _ = up("--no-adjutant", "-o", "codex --no-daemon", "-p", "ship it", "--continue")
	if code == 0 || strings.Contains(out, "needs a person") {
		t.Errorf("continue: %d\n%s", code, out)
	}
	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("up typed into core %d times", n)
	}
}

// The gate keeps the TUI's agent list for a file inbox and for a caller in
// a Herdr pane; only a daemon-owned inbox of a pane-less caller reads the
// daemon's inventory.
func TestGateInventoryIsTheDaemonsOnlyOnCore(t *testing.T) {
	dir := t.TempDir()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if gateInventory(dir, env(nil)) != nil {
		t.Error("a file inbox read the daemon's inventory")
	}
	if gateInventory("", env(nil)) != nil {
		t.Error("no inbox read the daemon's inventory")
	}
	if gateInventory(dir, env(map[string]string{"HERDR_PANE_ID": "p1"})) != nil {
		t.Error("a TUI caller read the daemon's inventory")
	}
}
