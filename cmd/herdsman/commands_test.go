package main

import (
	"bytes"
	"context"
	"fmt"
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

// Every command the skill and the README show answers --help with its own
// usage and exit 0 (A25); a message action prints only its own line.
func TestEveryDocumentedCommandHelp(t *testing.T) {
	cases := []struct{ args, want string }{
		{"route", "usage: herdsman route check"},
		{"route check", "usage: herdsman route check"},
		{"route log", "herdsman route log"},
		{"status", "usage: herdsman status"},
		{"wait", "usage: herdsman wait"},
		{"watch", "usage: herdsman watch"},
		{"watch implementers", "usage: herdsman watch"},
		{"watch reviewer", "usage: herdsman watch"},
		{"watch prs", "usage: herdsman watch"},
		{"run-check", "usage: herdsman run-check"},
		{"checkpointer", "herdsman checkpointer [--once"},
		{"checkpointer guard", "usage: herdsman checkpointer guard <orchestrator-name> <pane>"},
		{"transcript-audit", "usage: herdsman transcript-audit"},
		{"approval-gate", "usage: herdsman approval-gate"},
		{"message", "usage: herdsman message"},
		{"models", "usage: herdsman models"},
		{"models last", "usage: herdsman models"},
		{"models save", "usage: herdsman models"},
		{"afk", "usage: herdsman afk"},
		{"web", "-port"},
		{"doctor", "usage: herdsman doctor"},
		{"review start", "usage: herdsman review start"},
	}
	actions := []string{"status", "wait", "read", "release", "ack", "send", "hold", "unhold", "register", "courier", "card-watch", "resume-goal", "authority", "leak-gate"}
	for _, a := range actions {
		want := "usage: herdsman message [--json] " + a
		if a == "unhold" {
			want = "| unhold <name>" // one line with hold
		}
		cases = append(cases, struct{ args, want string }{"message " + a, want})
	}
	for _, c := range cases {
		args := append(strings.Fields(c.args), "--help")
		out, errOut, code := call(t, args...)
		if code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("%s --help: exit %d, want %q in stdout %q (stderr %q)", c.args, code, c.want, out, errOut)
		}
	}
	for _, a := range actions {
		out, _, _ := call(t, "message", a, "--help")
		if n := strings.Count(out, "usage: "); n != 1 || (a != "leak-gate" && strings.Contains(out, "leak-gate <file>")) {
			t.Errorf("message %s --help prints more than its own action: %q", a, out)
		}
	}
	// A trailing --help after an argument that is no subcommand goes to the
	// command (run-check passes it on to the command it runs).
	if out, _, code := call(t, "transcript-audit", "claude", "--help"); code != 2 || !strings.HasPrefix(out, "GAP usage") {
		t.Errorf("transcript-audit claude --help: %d %q", code, out)
	}
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

// The old shell suite's L1, L1b, L2, L2b and Z2 (the text kept as given).
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
	if code != 2 || errOut != "herdsman status: HERDSMAN_STATUS_SPECS: required, not set (got \"\")\n" {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestStatusThroughTheCommand(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("HERDSMAN_STATUS_SPECS", "im-a:w1:p1")
	t.Setenv("LIMIT_MIN", "")
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
	// A26: hours bash could not read are refused, not run as a check.
	if _, errs, code := call(t, "run-check", "--publish", "x"); code != 2 || errs != "herdsman run-check: --publish: not a whole number of hours (got \"x\")\n" {
		t.Errorf("--publish x: %d %q", code, errs)
	}
	// A25: an invalid setting is one line naming it, exit 2, and nothing runs.
	t.Setenv("HERDSMAN_GATE_POLL", "soon")
	if out, errs, code := call(t, "run-check", "echo", "ran"); code != 2 || out != "" ||
		errs != "herdsman run-check: HERDSMAN_GATE_POLL: not a duration (seconds, or a number with s, m, h or d) (got \"soon\")\n" {
		t.Errorf("GATE_POLL=soon: %d %q %q", code, out, errs)
	}
}

func TestTranscriptAuditThroughTheCommand(t *testing.T) {
	out, _, code := call(t, "transcript-audit", "claude")
	if code != 2 || out != "GAP usage: herdsman transcript-audit claude|codex <transcript> <session-id> [allowed-write-root/ ...]\n" {
		t.Errorf("usage: %d %q", code, out)
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(p, []byte(`{"type":"assistant","sessionId":"s1","message":{"content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"git push"}}]}}`+"\n"), 0o644)
	out, _, code = call(t, "transcript-audit", "claude", p, "s1")
	if code != 1 || !strings.HasPrefix(out, "FLAG tool=Bash call=b1 class=git-write") {
		t.Errorf("flag: %d %q", code, out)
	}
	// The channel scripts are gone: a run of one by its old path is a script
	// run like any other, and AUDIT_CHANNEL_DIR no longer exempts it.
	_ = os.WriteFile(p, []byte(`{"type":"assistant","sessionId":"s1","message":{"content":[{"type":"tool_use","id":"s1","name":"Bash","input":{"command":"/bin/bash /skill/scripts/message.sh status"}}]}}`+"\n"), 0o644)
	t.Setenv("AUDIT_CHANNEL_DIR", "/skill/scripts")
	out, _, code = call(t, "transcript-audit", "claude", p, "s1")
	if code != 1 || !strings.HasPrefix(out, "FLAG tool=Bash call=s1 class=script-run target=bash") {
		t.Errorf("old channel script: %d %q", code, out)
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
		// R3 finding 2: the shell's integer syntax, and the label as written.
		{" 30", `long .*<-- OVER  30m$`, `short .*OVER`},
		{"30 ", `long .*<-- OVER 30 m$`, `short .*OVER`},
		{"00", `short .*<-- OVER 00m$`, `^$`},
		{"030", `long .*<-- OVER 030m$`, `short .*OVER`},
		{"+30", `long .*<-- OVER \+30m$`, `short .*OVER`},
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

func TestMessageCommand(t *testing.T) {
	out, _, code := call(t, "help")
	if code != 0 || !strings.Contains(out, "  message ") {
		t.Errorf("help does not list message: %q", out)
	}
	out, _, code = call(t, "message", "--help")
	if code != 0 || !strings.Contains(out, "exit codes:") {
		t.Errorf("message --help: %d %q", code, out)
	}
	t.Setenv("HERDSMAN_NAME", "")
	_, errOut, code := call(t, "message", "status")
	if code != 2 || !strings.Contains(errOut, `herdsman message: HERDSMAN_NAME: required, not set (got "")`) {
		t.Errorf("no name: %d %q", code, errOut)
	}
	t.Setenv("HERDSMAN_NAME", "route-orchestrator")
	t.Setenv("HERDSMAN_INBOX", filepath.Join(t.TempDir(), "missing"))
	_, errOut, code = call(t, "message", "status")
	if code != 2 || !strings.Contains(errOut, "pre-create a private inbox directory") {
		t.Errorf("missing inbox: %d %q", code, errOut)
	}
	_, errOut, code = call(t, "message", "bogus")
	if code != 2 || !strings.HasPrefix(errOut, "message: status|wait|read") {
		t.Errorf("unknown action: %d %q", code, errOut)
	}
	if executable() == "" {
		t.Error("no executable path")
	}
}

// A25: LIMIT_MIN is a strict setting for status and for wait's health
// check: one line naming it, exit 2, nothing printed on stdout.
func TestStatusLimitIsStrict(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("HERDSMAN_STATUS_SPECS", "im-a:w1:p1")
	for _, v := range []string{"x", " ", "1/0", "-1"} {
		t.Setenv("LIMIT_MIN", v)
		out, errs, code := call(t, "status")
		if code != 2 || out != "" || !strings.HasPrefix(errs, "herdsman status: LIMIT_MIN: ") || strings.Count(errs, "\n") != 1 {
			t.Errorf("status LIMIT_MIN=%q: %d %q %q", v, code, out, errs)
		}
		t.Setenv("DEADLINE", "0")
		t.Setenv("POLL", "1")
		t.Setenv("HERDSMAN_STATE", t.TempDir())
		out, errs, code = call(t, "wait")
		if code != 2 || out != "" || !strings.HasPrefix(errs, "herdsman wait: LIMIT_MIN: ") {
			t.Errorf("wait LIMIT_MIN=%q: %d %q %q", v, code, out, errs)
		}
	}
}

// A25 item 2: each slice-1 command's --help ends in one table of its exit
// codes.
func TestHelpListsExitCodes(t *testing.T) {
	for name, codes := range map[string][]string{
		"run-check": {"64", "69", "70", "75", "2"},
		"status":    {"0", "2"}, // a missing HERDSMAN_STATUS_SPECS exits 2 (A25)
		"route":     {"0", "1", "2"},
		"wait":      {"0", "2"},
		"watch":     {"0", "1", "2"},
	} {
		out, _, code := call(t, name, "--help")
		if code != 0 || !strings.Contains(out, "usage: herdsman "+name) || !strings.Contains(out, "\nexit codes:\n") {
			t.Errorf("%s --help: %d %q", name, code, out)
			continue
		}
		table := out[strings.Index(out, "\nexit codes:\n"):]
		for _, c := range codes {
			if !regexp.MustCompile(`(?m)^  ` + c + ` +\S`).MatchString(table) {
				t.Errorf("%s --help: no row for exit %s in %q", name, c, table)
			}
		}
	}
}

// R5 finding 6: approval-gate reads HEALTH_AFTER and LOCK_WAIT through the
// strict parser: one line naming the variable, exit 2, before the inbox.
func TestGateSettingsAreStrict(t *testing.T) {
	t.Setenv("HERDSMAN_INBOX", filepath.Join(t.TempDir(), "none"))
	for _, tc := range []struct{ k, v, reason string }{
		{"HEALTH_AFTER", "bad", "not a whole number"}, {"HEALTH_AFTER", "0", "must be from 1 to 86400"},
		{"HEALTH_AFTER", "1\n2", "not a whole number"}, {"LOCK_WAIT", "61", "must be from 1 to 60"}, {"LOCK_WAIT", "x", "not a whole number"},
	} {
		t.Setenv("HEALTH_AFTER", "")
		t.Setenv("LOCK_WAIT", "")
		t.Setenv(tc.k, tc.v)
		out, errs, code := call(t, "approval-gate", "complete", "r1", "done")
		want := fmt.Sprintf("herdsman approval-gate: %s: %s (got %q)\n", tc.k, tc.reason, tc.v)
		if code != 2 || out != "" || errs != want {
			t.Errorf("%s=%q: %d %q, want %q", tc.k, tc.v, code, errs, want)
		}
	}
	// Valid values pass on to the inbox, which then reports its own error.
	t.Setenv("HEALTH_AFTER", " 0180 ")
	t.Setenv("LOCK_WAIT", "6")
	if _, errs, _ := call(t, "approval-gate", "complete", "r1", "done"); strings.Contains(errs, "HEALTH_AFTER") || strings.Contains(errs, "LOCK_WAIT") {
		t.Errorf("valid values: %q", errs)
	}
}

// Route ruling A25: a missing required setting is a settings error. Each
// slice 1 command with a required setting prints the one-line settings
// error and exits 2 before it runs anything. (wait, watch implementers and
// reviewer, run-check and route have no required setting; transcript-audit
// keeps main's GAP lines, which already exit 2.)
func TestMissingRequiredSettingsExit2(t *testing.T) {
	line := func(cmd, v string) string {
		return "herdsman " + cmd + ": " + v + `: required, not set (got "")` + "\n"
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"status", map[string]string{"HERDSMAN_STATUS_SPECS": ""}, []string{"status"}, line("status", "HERDSMAN_STATUS_SPECS")},
		{"watch prs PRS", map[string]string{"HERDSMAN_PRS": "", "HERDSMAN_REPO": "o/r", "HERDSMAN_PR_STATE": t.TempDir()}, []string{"watch", "prs"}, line("watch prs", "HERDSMAN_PRS")},
		{"watch prs REPO", map[string]string{"HERDSMAN_PRS": "7", "HERDSMAN_REPO": "", "HERDSMAN_PR_STATE": t.TempDir()}, []string{"watch", "prs"}, line("watch prs", "HERDSMAN_REPO")},
		{"watch prs STATE", map[string]string{"HERDSMAN_PRS": "7", "HERDSMAN_REPO": "o/r", "HERDSMAN_PR_STATE": ""}, []string{"watch", "prs"}, line("watch prs", "HERDSMAN_PR_STATE")},
		{"approval-gate consume", map[string]string{"HERDSMAN_INBOX": ""}, []string{"approval-gate", "consume", "/x/go", "push", "t", "r", "-"}, line("approval-gate", "HERDSMAN_INBOX")},
		{"approval-gate complete", map[string]string{"HERDSMAN_INBOX": ""}, []string{"approval-gate", "complete", "id", "done"}, line("approval-gate", "HERDSMAN_INBOX")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			out, errOut, code := call(t, tc.args...)
			if code != 2 || errOut != tc.want || out != "" {
				t.Errorf("%d %q %q, want 2 %q", code, out, errOut, tc.want)
			}
		})
	}
}

// Review head-2 P2: approval-gate --help names the settings it needs.
func TestGateHelpNamesItsSettings(t *testing.T) {
	out, _, code := call(t, "approval-gate", "--help")
	for _, s := range []string{"HERDSMAN_INBOX", "HERDSMAN_PREFLIGHT", "HEALTH_AFTER", "LOCK_WAIT"} {
		if code != 0 || !strings.Contains(out, s) {
			t.Errorf("approval-gate --help does not name %s: %d %q", s, code, out)
		}
	}
}
