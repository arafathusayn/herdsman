package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// A25 item 2: --json prints one JSON object per event line, and one
// object for a status or a check result.
func TestEventJSON(t *testing.T) {
	var b bytes.Buffer
	w := newEventJSON(&b)
	_, _ = w.Write([]byte("REPORT core /r/core.md 12:00\nTICK no event in 300s 12:05\nPR-WATCH-ERROR PR 7 could not be read\n"))
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", b.String())
	}
	var e Event
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e.Event != "report" || e.Detail != "core /r/core.md" || e.Time != "12:00" {
		t.Errorf("%+v %v", e, err)
	}
	e = Event{}
	if err := json.Unmarshal([]byte(lines[2]), &e); err != nil || e.Event != "pr_watch_error" || e.Detail != "PR 7 could not be read" || e.Time != "" {
		t.Errorf("%+v %v", e, err)
	}
}

func TestStatusJSON(t *testing.T) {
	snap := strings.Join([]string{"12:00:01", "machine: load 9.50 (5 min) on 8 cores, memory free 5%", "   <-- LOAD HIGH",
		"== im-a (w9:p1) state=working model=model-b", "   bg: job  running  1h 02m [62m]  <-- OVER 30m", "== rv-1 (w9:p9) state= model="}, "\n") + "\n"
	var s StatusJSON
	if err := json.Unmarshal(statusJSON(snap), &s); err != nil {
		t.Fatal(err)
	}
	if s.Time != "12:00:01" || s.Machine.Load5 != "9.50" || s.Machine.CPUs != "8" || s.Machine.FreePercent != "5" ||
		len(s.Machine.Flags) != 1 || s.Machine.Flags[0] != "LOAD HIGH" || len(s.Agents) != 2 || s.Agents[0].Name != "im-a" ||
		s.Agents[0].Pane != "w9:p1" || s.Agents[0].State != "working" || s.Agents[0].Model != "model-b" || len(s.Agents[0].Flags) != 1 ||
		s.Agents[0].Flags[0] != "OVER 30m" || len(s.Agents[0].Lines) != 1 || s.Agents[1].Model != "" {
		t.Errorf("%+v", s)
	}
}

func TestRouteCheckJSON(t *testing.T) {
	dir := t.TempDir()
	out, _, code := call(t, "route", "check", "--json", dir)
	var r CheckJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 1 || r.OK || len(r.Items) == 0 || r.Items[0].Status != "missing" {
		t.Errorf("%d %q %+v %v", code, out, r, err)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("one object: %q", out)
	}
	_ = os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
}

func TestWaitJSONThroughTheCommand(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("HERDSMAN_STATE", t.TempDir())
	t.Setenv("HERDSMAN_REPORTS", t.TempDir())
	t.Setenv("HERDSMAN_IMPLEMENTERS", "im-a")
	t.Setenv("DEADLINE", "0")
	t.Setenv("POLL", "1")
	out, _, code := call(t, "wait", "--json")
	var e Event
	if err := json.Unmarshal([]byte(out), &e); err != nil || code != 0 || e.Event != "tick" {
		t.Errorf("%d %q %v", code, out, err)
	}
	if _, errs, code := call(t, "watch", "--json"); code != 2 || !strings.Contains(errs, "usage") {
		t.Errorf("watch --json without a kind: %d %q", code, errs)
	}
}

// R5 finding 10: approval-gate and transcript-audit have --help tables and
// --json lines.
func TestGateAndAuditHelpAndJSON(t *testing.T) {
	for name, codes := range map[string][]string{"approval-gate": {"0", "1", "2", "3", "4", "5"}, "transcript-audit": {"0", "1", "2"}} {
		out, _, code := call(t, name, "--help")
		if code != 0 || !strings.Contains(out, "usage: herdsman "+name) || !strings.Contains(out, "\nexit codes:\n") {
			t.Errorf("%s --help: %d %q", name, code, out)
			continue
		}
		for _, c := range codes {
			if !strings.Contains(out[strings.Index(out, "\nexit codes:\n"):], "\n  "+c+" ") {
				t.Errorf("%s --help: no row for %s", name, c)
			}
		}
	}
	out, _, code := call(t, "transcript-audit", "--json", "claude")
	var e Event
	if err := json.Unmarshal([]byte(out), &e); err != nil || code != 2 || e.Event != "gap" || !strings.HasPrefix(e.Detail, "usage:") {
		t.Errorf("transcript-audit --json: %d %q %v", code, out, err)
	}
	t.Setenv("HERDSMAN_INBOX", "")
	out, _, code = call(t, "approval-gate", "--json", "complete")
	if code == 0 || (out != "" && !json.Valid([]byte(strings.SplitN(out, "\n", 2)[0]))) {
		t.Errorf("approval-gate --json: %d %q", code, out)
	}
}

// R5 finding 9: the command wiring of status --json, and the branches of
// the JSON writers.
func TestStatusJSONThroughTheCommand(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	a := herdrtest.Agent("w9:p1", "s")
	a["name"], a["agent_status"], a["model"] = "im-a", "working", "model-a"
	fx.SetAgent("w9:p1", a)
	fx.SetScreen("w9:p1", "└ ◆ job running 1h02m\n")
	t.Setenv("HERDR_SOCKET_PATH", srv.Socket)
	t.Setenv("HERDSMAN_STATUS_SPECS", "im-a:w9:p1:")
	t.Setenv("LIMIT_MIN", "")
	out, errs, code := call(t, "status", "--json")
	var s StatusJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil || code != 0 || errs != "" || len(s.Agents) != 1 || s.Agents[0].Model != "model-a" ||
		s.Agents[0].State != "working" || len(s.Agents[0].Flags) != 1 || s.Agents[0].Flags[0] != "OVER 30m" || strings.Count(out, "\n") != 1 {
		t.Errorf("%d %q %q %v", code, out, errs, err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestJSONWriterBranches(t *testing.T) {
	var b bytes.Buffer
	w := newEventJSON(&b)
	_, _ = w.Write([]byte("\n   \nREPORT a"))
	if b.Len() != 0 {
		t.Errorf("blank lines and a partial line print nothing: %q", b.String())
	}
	_, _ = w.Write([]byte(" 12:00:01\n"))
	if !strings.Contains(b.String(), `"time":"12:00:01"`) {
		t.Errorf("%q", b.String())
	}
	if _, err := newEventJSON(failWriter{}).Write([]byte("TICK x\n")); err == nil {
		t.Error("a failed write is an error")
	}
	var r CheckJSON
	if err := json.Unmarshal(checkJSON("ok a\n\nMISSING b\n", 1), &r); err != nil || r.OK || len(r.Items) != 2 || r.Items[1] != (CheckItem{"missing", "b"}) {
		t.Errorf("%+v %v", r, err)
	}
}
