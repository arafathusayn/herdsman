package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// call runs one command line as main does and returns its output and exit code.
func call(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(args, &out, &errOut)
	code := 0
	if e, ok := err.(*exitError); ok {
		code = e.code
	} else if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String(), errOut.String(), code
}

func TestHelpListsEveryCommand(t *testing.T) {
	out, _, code := call(t, "help")
	for _, name := range []string{"status", "route", "run-check", "wait", "watch", "approval-gate", "transcript-audit", "web"} {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + ` `).MatchString(out) {
			t.Errorf("help does not list %s", name)
		}
	}
	if code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	out, errOut, code := call(t, "fly")
	if code != 2 || out != "" || !strings.Contains(errOut, "Usage:") {
		t.Errorf("unknown command: %d %q %q", code, out, errOut)
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	register(command{name: "route"})
}

// The usage errors of every command: usage on stderr, exit 2.
func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"route"}, "usage: herdsman route check"},                        // no subcommand
		{[]string{"route", "check"}, "usage: herdsman route check"},               // K8 usage without a folder
		{[]string{"route", "check", dir, "later"}, "usage: herdsman route check"}, // K8b an unknown stage
		{[]string{"route", "log", filepath.Join(dir, "00-route.md")}, "usage: herdsman route log"},
		{[]string{"status", "x"}, "usage: herdsman status"},
		{[]string{"wait", "x"}, "usage: herdsman wait"},
		{[]string{"watch"}, "usage: herdsman watch"},
		{[]string{"watch", "nobody"}, "usage: herdsman watch"},
	} {
		_, errOut, code := call(t, tc.args...)
		if code != 2 || !strings.HasPrefix(errOut, tc.want) {
			t.Errorf("%v: %d %q", tc.args, code, errOut)
		}
	}
}

// run-tests.sh L1, L1b, L2, L2b and Z2 (the text kept as given).
func TestRouteLog(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 25, 7, 5, 0, 0, time.Local) }
	t.Cleanup(func() { now = time.Now })
	p := filepath.Join(t.TempDir(), "00-route.md")
	_ = os.WriteFile(p, []byte("# route\n"), 0o644)
	if _, _, code := call(t, "route", "log", p, "im-a started task a"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, _, code := call(t, "route", "log", p, "im-a: round 2 (due 14:05) * done"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "# route\n- 07:05 im-a started task a\n- 07:05 im-a: round 2 (due 14:05) * done\n" {
		t.Errorf("L1, L1b, Z2: %q", b)
	}
	_, errOut, code := call(t, "route", "log", p)
	if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman route log") {
		t.Errorf("L2: %d %q", code, errOut)
	}
	_, errOut, code = call(t, "route", "log", p+".none", "x")
	if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman route log") {
		t.Errorf("L2b: %d %q", code, errOut)
	}
	_ = os.Chmod(p, 0o444)
	if os.Geteuid() != 0 {
		_, errOut, code = call(t, "route", "log", p, "x")
		if code != 1 || !strings.HasPrefix(errOut, "herdsman route log:") {
			t.Errorf("a read-only file: %d %q", code, errOut)
		}
	}
}

func TestRouteCheckThroughTheCommand(t *testing.T) {
	_, _, code := call(t, "route", "check", t.TempDir(), "close")
	if code == 0 || code == 2 {
		t.Errorf("an empty folder passes the close check: %d", code)
	}
}

func TestStatusNeedsSpecs(t *testing.T) {
	t.Setenv("HERDSMAN_STATUS_SPECS", "")
	_, errOut, code := call(t, "status")
	if code != 1 || errOut != "herdsman status: HERDSMAN_STATUS_SPECS: set HERDSMAN_STATUS_SPECS\n" {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestStatusThroughTheCommand(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("HERDSMAN_STATUS_SPECS", "im-a:w1:p1")
	t.Setenv("LIMIT_MIN", "x")
	// No socket path, no config folder and no home: the client has no socket.
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	out, _, code := call(t, "status")
	if code != 0 || !strings.Contains(out, "im-a") {
		t.Errorf("%d %q", code, out)
	}
}

func TestRunCheckThroughTheCommand(t *testing.T) {
	t.Setenv("HERDSMAN_LOCK", filepath.Join(t.TempDir(), "lock"))
	t.Setenv("HERDSMAN_PROBES", "off")
	if _, _, code := call(t, "run-check"); code != 64 {
		t.Errorf("no command: %d", code)
	}
	if _, _, code := call(t, "run-check", "--publish"); code == 0 {
		t.Error("--publish without hours passes")
	}
	if _, _, code := call(t, "run-check", "--publish", "x"); code != 64 {
		t.Errorf("--publish x: %d", code)
	}
}

func TestTranscriptAuditThroughTheCommand(t *testing.T) {
	out, _, code := call(t, "transcript-audit", "claude")
	if code != 2 || out != "GAP usage: herdsman transcript-audit claude|codex <transcript> <session-id> [allowed-write-root/ ...]\n" {
		t.Errorf("usage: %d %q", code, out)
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"assistant","sessionId":"s1","message":{"content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"git push"}}]}}`+"\n"), 0o644)
	t.Setenv("AUDIT_CHANNEL_DIR", "/skill/scripts")
	out, _, code = call(t, "transcript-audit", "claude", p, "s1")
	if code != 1 || !strings.HasPrefix(out, "FLAG tool=Bash call=b1 class=git-write") {
		t.Errorf("flag: %d %q", code, out)
	}
}

func TestApprovalGateThroughTheCommand(t *testing.T) {
	t.Setenv("HERDSMAN_INBOX", "")
	out, errOut, code := call(t, "approval-gate")
	if code == 0 || out+errOut == "" {
		t.Errorf("no arguments: %d %q %q", code, out, errOut)
	}
}

// The waiters end on an interrupt; each run gets one SIGINT once its
// signal context is set.
func TestWatchersEndOnInterrupt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "none.sock"))
	t.Setenv("HERDSMAN_STATE", filepath.Join(dir, "state"))
	t.Setenv("HERDSMAN_REPORTS", filepath.Join(dir, "reports"))
	t.Setenv("HERDSMAN_IMPLEMENTERS", "")
	t.Setenv("HERDSMAN_PRS", "")
	t.Setenv("POLL", "1")
	t.Setenv("PATH", dir)
	for _, args := range [][]string{{"watch", "implementers"}, {"watch", "reviewer"}, {"watch", "prs"}, {"wait"}} {
		done := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
			}
		}()
		call(t, args...)
		close(done)
	}
}

// R2 finding 2: LIMIT_MIN defaults to 30 only when unset or empty; an
// explicit 0 flags a two-minute job (and so gives wait its HEALTH event);
// a value that is no number flags nothing, as the script's test fails.
func TestStatusLimitFromTheEnvironment(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", "working"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "└ ◆ short  running  2m 1s\n└ ◆ long  running  45m 0s\n")
	t.Setenv("HERDR_SOCKET_PATH", srv.Socket)
	for _, tc := range []struct{ limit, want, not string }{
		{"0", `short .*<-- OVER 0m`, `^$`},
		{"", `long .*<-- OVER 30m`, `short .*OVER`},
		{"x", `long  running  45m 0s \[45m\]$`, `OVER`},
		// R3 finding 2: the shell's integer syntax, and the label as written.
		{" 30", `long .*<-- OVER  30m$`, `short .*OVER`},
		{"30 ", `long .*<-- OVER 30 m$`, `short .*OVER`},
		{"00", `short .*<-- OVER 00m$`, `^$`},
		{"030", `long .*<-- OVER 030m$`, `short .*OVER`},
		{"+30", `long .*<-- OVER \+30m$`, `short .*OVER`},
		{" ", `long  running  45m 0s \[45m\]$`, `OVER`},
	} {
		t.Setenv("LIMIT_MIN", tc.limit)
		var b bytes.Buffer
		statusSnapshot(context.Background(), &b, "im-a:w9:p1:")
		if !regexp.MustCompile("(?m)"+tc.want).MatchString(b.String()) || (tc.not != `^$` && regexp.MustCompile("(?m)"+tc.not).MatchString(b.String())) {
			t.Errorf("LIMIT_MIN=%q:\n%s", tc.limit, b.String())
		}
	}
}

// R2 finding 2, the health side: with LIMIT_MIN=0 the waiter's in-process
// status check flags the two-minute job as a HEALTH event.
func TestWaitHealthWithAZeroLimit(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", "working"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "└ ◆ short  running  2m 1s\n")
	dir := t.TempDir()
	for k, v := range map[string]string{"HERDR_SOCKET_PATH": srv.Socket, "HERDSMAN_STATE": filepath.Join(dir, "state"),
		"HERDSMAN_REPORTS": filepath.Join(dir, "reports"), "HERDSMAN_IMPLEMENTERS": "", "HERDSMAN_REVIEWERS": "",
		"HERDSMAN_STATUS_SPECS": "im-a:w9:p1:", "HERDSMAN_HEALTH_EVERY": "0", "LIMIT_MIN": "0", "DEADLINE": "0", "POLL": "1"} {
		t.Setenv(k, v)
	}
	t.Setenv("HERDSMAN_STATUS_SCRIPT", "") // empty: the in-process check (A12)
	out := ""
	for range 2 {
		o, _, _ := call(t, "wait")
		out += o
	}
	if !regexp.MustCompile(`(?m)^HEALTH .*OVER 0m`).MatchString(out) {
		t.Errorf("no HEALTH event for a zero limit:\n%s", out)
	}
}

// R3 finding 2, the health side: LIMIT_MIN=" 30" is 30 to the shell, so a
// 45-minute job is a HEALTH event, its flag printed as written.
func TestWaitHealthWithAPaddedLimit(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"] = "im-a", "working"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "└ ◆ long  running  45m 0s\n")
	dir := t.TempDir()
	for k, v := range map[string]string{"HERDR_SOCKET_PATH": srv.Socket, "HERDSMAN_STATE": filepath.Join(dir, "state"),
		"HERDSMAN_REPORTS": filepath.Join(dir, "reports"), "HERDSMAN_IMPLEMENTERS": "", "HERDSMAN_REVIEWERS": "",
		"HERDSMAN_STATUS_SPECS": "im-a:w9:p1:", "HERDSMAN_HEALTH_EVERY": "0", "LIMIT_MIN": " 30", "DEADLINE": "0", "POLL": "1"} {
		t.Setenv(k, v)
	}
	t.Setenv("HERDSMAN_STATUS_SCRIPT", "") // empty: the in-process check (A12)
	out := ""
	for range 2 {
		o, _, _ := call(t, "wait")
		out += o
	}
	if !regexp.MustCompile(`(?m)^HEALTH .*OVER  30m`).MatchString(out) {
		t.Errorf("no HEALTH event for a padded limit:\n%s", out)
	}
}
