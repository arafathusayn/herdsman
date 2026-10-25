package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/status"
)

// The W, R, E and Z1 cases of the skill's run-tests.sh. Each run is one
// pass: the sleep stub ends the loop, as the shell suite's `sleep` stub did.

type harness struct {
	t      *testing.T
	dir    string
	fx     *herdrtest.Fixture
	client *herdr.Client
	env    map[string]string
	now    time.Time
	sleeps []time.Duration
	status string // the in-process status snapshot
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := herdrtest.Start(t)
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, fx: herdrtest.Serve(srv), client: herdr.New(srv.Socket), now: time.Now()}
	h.env = map[string]string{
		"HERDSMAN_REPORTS": filepath.Join(dir, "reports"), "HERDSMAN_STATE": filepath.Join(dir, "state"),
		"HERDSMAN_IMPLEMENTERS": "im-a im-b", "HERDSMAN_PANE_im_a": "w9:p1", "HERDSMAN_PANE_im_b": "w9:p2",
		"HERDSMAN_REVIEWER_PANE": "w9:p3", "HERDSMAN_REVIEWER_NAME": "rv-1",
	}
	_ = os.MkdirAll(h.env["HERDSMAN_REPORTS"], 0o755)
	return h
}

func (h *harness) screen(pane string, lines ...string) {
	h.fx.SetScreen(pane, strings.Join(lines, "\n")+"\n")
}

func (h *harness) file(p string, text string) {
	h.t.Helper()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// run runs fn once with the harness's environment plus extra settings; a
// key set to "\x00" is unset.
func (h *harness) run(fn func(context.Context, Env, Deps) int, extra ...string) (string, int) {
	h.t.Helper()
	env := maps.Clone(h.env)
	for i := 0; i+1 < len(extra); i += 2 {
		env[extra[i]] = extra[i+1]
	}
	lookup := func(k string) (string, bool) {
		v, ok := env[k]
		if v == "\x00" {
			return "", false
		}
		return v, ok
	}
	var out bytes.Buffer
	d := Deps{Client: h.client, Now: func() time.Time { return h.now }, Stdout: &out, Stderr: &out,
		Sleep:  func(d time.Duration) bool { h.sleeps = append(h.sleeps, d); return false },
		Status: func(_ context.Context, w io.Writer, _ string) { io.WriteString(w, h.status) },
	}
	code := fn(context.Background(), lookup, d)
	return out.String(), code
}

func (h *harness) wait(extra ...string) string {
	out, code := h.run(Wait, append([]string{"DEADLINE", "0", "POLL", "1"}, extra...)...)
	if code != 0 {
		h.t.Errorf("wait exit %d", code)
	}
	return out
}

func match(t *testing.T, name, re, s string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: want /%s/ in %q", name, re, s)
	}
}

func noMatch(t *testing.T, name, re, s string) {
	t.Helper()
	if regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: unexpected /%s/ in %q", name, re, s)
	}
}

func TestImplementersWatcher(t *testing.T) {
	h := newHarness(t)
	reports := h.env["HERDSMAN_REPORTS"]
	runW := func() string { out, _ := h.run(Implementers); return out }
	h.screen("w9:p1", "• Working (3m 10s • esc to interrupt)", "› Ask Codex to do anything")
	h.screen("w9:p2", "Pursuing goal (2m)")
	out := runW()
	noMatch(t, "W1 first working pass is silent", `STALL|IDLE|BLOCKED|REPORT`, out)
	out = runW()
	match(t, "W2 stall im-a", `STALL im-a`, out)
	match(t, "W2 stall im-b", `STALL im-b`, out)
	h.screen("w9:p1", "Password for 'https://x@github.com': Device not configured")
	out = runW()
	match(t, "W3 blocked im-a", `BLOCKED im-a Password for`, out)
	h.screen("w9:p2", "› Ask Codex to do anything", "/path/to/report.md")
	out = runW()
	match(t, "W4 idle im-b", `IDLE im-b`, out)
	h.file(filepath.Join(reports, "a.md"), "STATUS: complete\n")
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	_ = os.Chtimes(filepath.Join(reports, "a.md"), old, old)
	out = runW()
	match(t, "W5 report a", "REPORT a "+regexp.QuoteMeta(reports)+"/a.md", out)
	out = runW()
	noMatch(t, "W5b report a not repeated", `REPORT a`, out)
	h.file(filepath.Join(reports, "a.md"), "STATUS: complete\nhead: new\n")
	out = runW()
	match(t, "W6 report a updated", "REPORT-UPDATED a "+regexp.QuoteMeta(reports)+"/a.md", out)
	out = runW()
	noMatch(t, "W6b update not repeated", `REPORT-UPDATED`, out)
	h.screen("w9:p1", "• Queued follow-up inputs", "  ? 1 question")
	out = runW()
	match(t, "W7 queued question blocked", `BLOCKED im-a Queued follow-up inputs`, out)
	out = runW()
	noMatch(t, "W7b unchanged blocked screen is silent", `BLOCKED`, out)
	h.file(filepath.Join(reports, "b.md"), "STATUS: complete\n")
	out, code := h.run(Implementers)
	match(t, "W8 report b", `REPORT b`, out)
	noMatch(t, "W8b no ALL-REPORTS exit", `ALL-REPORTS`, out)
	if code != 0 || h.sleeps[len(h.sleeps)-1] != 120*time.Second {
		t.Errorf("W8c the loop only ends at the sleep, which is 120 s: %d %v", code, h.sleeps)
	}

	// W9: an unset list keeps the default list; an empty one means none.
	h2 := newHarness(t)
	h2.screen("w9:p1", "Password for 'https://x@github.com': Device not configured")
	h2.screen("w9:p2", "Password for 'https://x@github.com': Device not configured")
	h2.screen("w2:pA", "Password for 'https://x@github.com': Device not configured")
	h2.file(filepath.Join(h2.env["HERDSMAN_REPORTS"], "a.md"), "STATUS: complete\n")
	out, _ = h2.run(Implementers, "HERDSMAN_IMPLEMENTERS", "", "HERDSMAN_STATE", filepath.Join(h2.dir, "w9a"))
	noMatch(t, "W9a an empty implementer list watches no implementer", `BLOCKED|REPORT|STALL|IDLE|rror`, out)
	out, _ = h2.run(Implementers, "HERDSMAN_IMPLEMENTERS", "", "HERDSMAN_INTEGRATOR", "im-b", "HERDSMAN_STATE", filepath.Join(h2.dir, "w9b"))
	match(t, "W9b an empty list still watches the integrator", `BLOCKED im-b Password for`, out)
	noMatch(t, "W9c and only the integrator", `im-a`, out)
	out, _ = h2.run(Implementers, "HERDSMAN_IMPLEMENTERS", "\x00", "HERDSMAN_PANE_im_a", "\x00", "HERDSMAN_STATE", filepath.Join(h2.dir, "w9c"))
	match(t, "W9d an unset implementer list keeps the default list (and its default pane)", `BLOCKED im-a Password for`, out)
}

func TestReviewerWatcher(t *testing.T) {
	h := newHarness(t)
	runR := func() string { out, _ := h.run(Reviewer); return out }
	h.screen("w9:p3", "· Incubating… (10s · thinking with xhigh effort)")
	out := runR()
	noMatch(t, "R1 working is silent", `IDLE|BLOCKED|REVIEW`, out)
	h.screen("w9:p3", "Allow reads outside the working directories?")
	out = runR()
	match(t, "R2 blocked", `BLOCKED rv-1 Allow reads outside`, out)
	out = runR()
	noMatch(t, "R2b unchanged blocked screen is silent", `BLOCKED`, out)
	h.screen("w9:p3", "❯", "/path/review-31-1.md")
	out = runR()
	match(t, "R3 idle", `IDLE rv-1`, out)
	out = runR()
	noMatch(t, "R4 unchanged idle is silent", `IDLE`, out)
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "review-31-1.md"), "VERDICT: pass\n")
	out = runR()
	match(t, "R5 review", "REVIEW "+regexp.QuoteMeta(h.env["HERDSMAN_REPORTS"])+"/review-31-1.md", out)
	out = runR()
	noMatch(t, "R5b review not repeated", `REVIEW`, out)
	out, _ = h.run(Reviewer, "HERDSMAN_REPORTS", filepath.Join(h.dir, "no-such-dir"))
	noMatch(t, "R6 no reports dir: no error text", `No such file|error`, out)
	h.sleeps = nil
	runR()
	if len(h.sleeps) != 1 || h.sleeps[0] > 60*time.Second || h.sleeps[0] <= 0 {
		t.Errorf("R7 the reviewer watcher sleeps 60 s or less between polls: %v", h.sleeps)
	}
}

func TestWaiter(t *testing.T) {
	h := newHarness(t)
	reports := h.env["HERDSMAN_REPORTS"]
	h.screen("w9:p1", "• Working (1s • esc to interrupt)")
	h.screen("w9:p2", "Pursuing goal (1m)")
	h.screen("w9:p3", "· Incubating…")
	out, code := h.run(Wait, "DEADLINE", "0", "POLL", "1")
	match(t, "E1 tick", `^TICK no event`, out)
	if code != 0 {
		t.Errorf("E1 exit 0: %d", code)
	}
	h.file(filepath.Join(reports, "a.md"), "STATUS: complete\n")
	out = h.wait()
	match(t, "E2 report", `REPORT a `, out)
	noMatch(t, "E2b no tick with an event", `TICK`, out)
	h.screen("w9:p2", "Goal achieved (20m)")
	out = h.wait()
	match(t, "E3 goal done no report", `GOAL-DONE-NO-REPORT im-b`, out)
	out = h.wait()
	noMatch(t, "E3b not repeated on the same screen", `GOAL-DONE`, out)
	h.screen("w9:p3", "Allow reads outside the working directories?")
	out = h.wait()
	match(t, "E4 reviewer blocked", `BLOCKED rv-1 Allow reads outside`, out)
	h.file(filepath.Join(reports, "review-7-1.md"), "VERDICT: fix\n")
	out = h.wait()
	match(t, "E5 review", `REVIEW .*review-7-1.md`, out)
	h.screen("w9:p3", "· Incubating…")
	h.screen("w9:p1", "• Working (9m • esc to interrupt)")
	h.wait()
	h.wait()
	out = h.wait()
	noMatch(t, "E6a no stall after the change plus two unchanged polls", `STALL`, out)
	out = h.wait()
	match(t, "E6b stall on the third unchanged poll", `STALL im-a`, out)

	unix := func(d time.Duration) string { return strconv.FormatInt(h.now.Add(d).Unix(), 10) }
	h.env["HERDSMAN_DUE_im_b"] = unix(time.Hour)
	out = h.wait()
	noMatch(t, "E7 not overdue before the due time", `OVERDUE`, out)
	h.env["HERDSMAN_DUE_im_b"] = unix(-time.Minute)
	out = h.wait()
	match(t, "E8 overdue without a report", `OVERDUE im-b due [0-9][0-9]:[0-9][0-9]`, out)
	out = h.wait()
	noMatch(t, "E8b overdue not repeated", `OVERDUE`, out)
	h.env["HERDSMAN_DUE_im_a"] = unix(-time.Minute)
	out = h.wait()
	match(t, "E9 overdue with a stale existing report", `OVERDUE im-a`, out)
	delete(h.env, "HERDSMAN_DUE_im_a")
	h.env["HERDSMAN_DUE_im_b"] = unix(2 * time.Second)
	out = h.wait()
	noMatch(t, "E10a armed, not yet due", `OVERDUE`, out)
	h.file(filepath.Join(reports, "b.md"), "STATUS: done\n")
	h.now = h.now.Add(3 * time.Second)
	out = h.wait()
	match(t, "E10b report event", `REPORT b `, out)
	noMatch(t, "E10c no overdue after the report", `OVERDUE`, out)
	delete(h.env, "HERDSMAN_DUE_im_b")
	if out, code := h.run(Wait, "DEADLINE", "0", "POLL", "1", "HERDSMAN_DUE_im_b", "soon"); code != 2 || !strings.HasPrefix(out, "herdsman wait: HERDSMAN_DUE_im_b: ") {
		t.Errorf("E10d (A25) a due time that is no number is an invalid setting: %d %q", code, out)
	}

	h.env["HERDSMAN_INTEGRATOR"], h.env["HERDSMAN_PANE_int_1"], h.env["HERDSMAN_TASK_int_1"] = "int-1", "w9:p4", "i"
	out = h.wait()
	noMatch(t, "E11a integrator watched, no report yet", `REPORT`, out)
	h.file(filepath.Join(reports, "i.md"), "STATUS: complete\n")
	out = h.wait()
	match(t, "E11b integrator report", "REPORT i "+regexp.QuoteMeta(reports)+"/i.md", out)
	delete(h.env, "HERDSMAN_INTEGRATOR")

	// E13: the health check is silent on the first arm, then each flag once, until it clears.
	h.env["HERDSMAN_STATUS_SPECS"], h.env["HERDSMAN_HEALTH_EVERY"] = "im-a:w9:p1:"+h.dir, "0"
	h.status = strings.Join([]string{"12:00:00", "machine: load 9.00 (5 min) on 8 cores, memory free 50%", "   <-- LOAD HIGH",
		"== im-a (w9:p1) state= model=", "   files changed in last 10m under t: 0  <-- NO WRITES in 10m"}, "\n") + "\n"
	out = h.wait()
	noMatch(t, "E13a first arm only starts the clock", `HEALTH`, out)
	out = h.wait()
	match(t, "E13b machine flag", `HEALTH machine LOAD HIGH `, out)
	match(t, "E13c agent flag", `HEALTH im-a NO WRITES in 10m `, out)
	out = h.wait()
	noMatch(t, "E13d same flags are not repeated", `HEALTH`, out)
	h.status = strings.Join([]string{"12:10:00", "machine: load 2.00 (5 min) on 8 cores, memory free 50%", "== im-a (w9:p1) state= model=",
		"   files changed in last 10m under t: 0  <-- NO WRITES in 10m"}, "\n") + "\n"
	out = h.wait()
	noMatch(t, "E13e a cleared flag is silent", `HEALTH`, out)
	h.status = strings.Join([]string{"12:20:00", "machine: load 9.00 (5 min) on 8 cores, memory free 50%", "   <-- LOAD HIGH", "== im-a (w9:p1) state= model=",
		"   files changed in last 10m under t: 0  <-- NO WRITES in 10m"}, "\n") + "\n"
	out = h.wait()
	match(t, "E13f a flag that returns is reported again", `HEALTH machine LOAD HIGH`, out)
	noMatch(t, "E13g the unchanged flag stays silent", `HEALTH im-a`, out)

	// E17: the interval is HERDSMAN_HEALTH_EVERY, default 300; the old name HEALTH_EVERY is still read.
	healthRun := func(ago int, extra ...string) string {
		s := filepath.Join(h.dir, "state-h")
		_ = os.RemoveAll(s)
		h.file(filepath.Join(s, "health-at"), strconv.FormatInt(h.now.Unix()-int64(ago), 10))
		return h.wait(append([]string{"HERDSMAN_HEALTH_EVERY", "\x00", "HEALTH_EVERY", "\x00", "HERDSMAN_STATE", s}, extra...)...)
	}
	noMatch(t, "E17a default interval: a check 200 s ago is not due", `HEALTH`, healthRun(200))
	match(t, "E17b default interval is 300 s: a check 400 s ago is due", `HEALTH machine LOAD HIGH`, healthRun(400))
	match(t, "E17c HERDSMAN_HEALTH_EVERY is read: a shorter interval is due", `HEALTH machine LOAD HIGH`, healthRun(200, "HERDSMAN_HEALTH_EVERY", "100"))
	noMatch(t, "E17d HERDSMAN_HEALTH_EVERY is read: a longer interval is not due", `HEALTH`, healthRun(700, "HERDSMAN_HEALTH_EVERY", "1000"))
	match(t, "E17e the old name HEALTH_EVERY is still read: a shorter interval is due", `HEALTH machine LOAD HIGH`, healthRun(200, "HEALTH_EVERY", "100"))
	noMatch(t, "E17f the old name HEALTH_EVERY is still read: a longer interval is not due", `HEALTH`, healthRun(700, "HEALTH_EVERY", "1000"))
	noMatch(t, "E17g HERDSMAN_HEALTH_EVERY wins over the old name", `HEALTH`, healthRun(200, "HERDSMAN_HEALTH_EVERY", "1000", "HEALTH_EVERY", "100"))

	// A12: HERDSMAN_STATUS_SCRIPT names a program the waiter runs directly.
	prog := filepath.Join(h.dir, "status-prog")
	h.file(prog, "#!/bin/sh\nprintf '%s\\n' '== im-b (w9:p2) state= model=' '   tree not found: x  <-- TREE MISSING'\n")
	_ = os.Chmod(prog, 0o755)
	match(t, "A12a an executable status program is run and its flags read", `HEALTH im-b TREE MISSING`, healthRun(400, "HERDSMAN_STATUS_SCRIPT", prog))
	_ = os.Chmod(prog, 0o644)
	out = healthRun(400, "HERDSMAN_STATUS_SCRIPT", prog)
	noMatch(t, "A12b a status file that is not executable gives no HEALTH event", `HEALTH`, out)
	match(t, "A12c and an error line", `HERDSMAN_STATUS_SCRIPT .* is not executable`, out)
	noMatch(t, "A12d a status program that does not exist is skipped silently", `HEALTH|executable`, healthRun(400, "HERDSMAN_STATUS_SCRIPT", filepath.Join(h.dir, "nope")))
	match(t, "A12e an empty HERDSMAN_STATUS_SCRIPT takes the snapshot in process", `HEALTH machine LOAD HIGH`, healthRun(400, "HERDSMAN_STATUS_SCRIPT", ""))
	delete(h.env, "HERDSMAN_STATUS_SPECS")
	delete(h.env, "HERDSMAN_HEALTH_EVERY")

	// E18: an unset list keeps the default list; an empty one means none (a review-only route).
	r3 := filepath.Join(h.dir, "reports3")
	h.file(filepath.Join(r3, "a.md"), "STATUS: complete\n")
	out = h.wait("HERDSMAN_REPORTS", r3, "HERDSMAN_STATE", filepath.Join(h.dir, "state-i1"), "HERDSMAN_IMPLEMENTERS", "")
	noMatch(t, "E18a an empty implementer list watches no implementer", `REPORT`, out)
	match(t, "E18b and the waiter still ticks", `^TICK no event`, out)
	h.file(filepath.Join(r3, "review-3-1.md"), "VERDICT: pass\n")
	out = h.wait("HERDSMAN_REPORTS", r3, "HERDSMAN_STATE", filepath.Join(h.dir, "state-i1"), "HERDSMAN_IMPLEMENTERS", "")
	match(t, "E18c an empty implementer list still reports a review", `REVIEW .*review-3-1.md`, out)
	out = h.wait("HERDSMAN_REPORTS", r3, "HERDSMAN_STATE", filepath.Join(h.dir, "state-i2"), "HERDSMAN_IMPLEMENTERS", "\x00")
	match(t, "E18d an unset implementer list keeps the default list", `REPORT a `, out)
}

// E16: a status tree that does not exist reaches the orchestrator as its own
// HEALTH flag, through the in-process snapshot.
func TestWaiterHealthWithTheRealSnapshot(t *testing.T) {
	h := newHarness(t)
	s := filepath.Join(h.dir, "state-t")
	h.file(filepath.Join(s, "health-at"), strconv.FormatInt(h.now.Unix()-400, 10))
	var b bytes.Buffer
	d := Deps{Client: h.client, Now: func() time.Time { return h.now }, Stdout: &b, Stderr: &b, Sleep: func(time.Duration) bool { return false },
		Status: func(ctx context.Context, w io.Writer, specs string) {
			status.Write(ctx, w, status.Options{Specs: status.ParseSpecs(specs), LimitMin: 30, Client: h.client, Now: h.now})
		}}
	env := func(k string) (string, bool) {
		m := map[string]string{"DEADLINE": "0", "POLL": "1", "HERDSMAN_STATE": s, "HERDSMAN_IMPLEMENTERS": "",
			"HERDSMAN_REPORTS": h.env["HERDSMAN_REPORTS"], "HERDSMAN_STATUS_SPECS": "im-a:w9:p1:" + h.dir + "/no-such-tree"}
		v, ok := m[k]
		return v, ok
	}
	Wait(context.Background(), env, d)
	match(t, "E16 the waiter reports a missing tree as a HEALTH flag", `HEALTH im-a TREE MISSING`, b.String())
}

func TestWaiterStalledFooter(t *testing.T) {
	h := newHarness(t)
	h.screen("w9:p1", "• Working (12s • esc to interrupt)", "  Goal stalled (/goal resume)")
	h.screen("w9:p2", "Pursuing goal (1m)")
	h.screen("w9:p3", "• Working (5s • esc to interrupt)", "  Goal stalled (/goal resume)")
	out := h.wait()
	noMatch(t, "E14a working pane with a stale stalled footer is not BLOCKED", `BLOCKED im-a`, out)
	noMatch(t, "E14b working reviewer with a stale stalled footer is not BLOCKED", `BLOCKED rv-1`, out)
	h.wait()
	out = h.wait()
	noMatch(t, "E14c no stall after the change plus two unchanged polls", `STALL im-a`, out)
	out = h.wait()
	match(t, "E14d stall on the third unchanged poll", `STALL im-a`, out)
	h.screen("w9:p1", "› Ask Codex to do anything", "  Goal stalled (/goal resume)")
	h.screen("w9:p2", "Apply for Daybreak")
	h.screen("w9:p3", "❯", "  Goal stalled (/goal resume)")
	out = h.wait()
	match(t, "E15a quiet pane with a stalled footer is BLOCKED", `BLOCKED im-a Goal stalled`, out)
	match(t, "E15b quiet pane with the Daybreak text is BLOCKED", `BLOCKED im-b Apply for Daybreak`, out)
	match(t, "E15c quiet reviewer with a stalled footer is BLOCKED", `BLOCKED rv-1 Goal stalled`, out)
	out = h.wait()
	noMatch(t, "E15d unchanged blocked screens are silent", `BLOCKED`, out)
}

// Z1: the documented command form (reviewers as name:pane pairs, a due time).
func TestWaiterReviewersAndDeadline(t *testing.T) {
	h := newHarness(t)
	h.file(filepath.Join(h.env["HERDSMAN_REPORTS"], "a.md"), "STATUS: complete\n")
	out := h.wait("HERDSMAN_DUE_im_b", strconv.FormatInt(h.now.Unix()-60, 10), "HERDSMAN_REVIEWERS", "rv-1:w9:p3 solo")
	match(t, "Z1 waiter: report", `REPORT a `, out)
	match(t, "Z1b waiter: due time", `OVERDUE im-b due [0-9][0-9]:[0-9][0-9]`, out)
	// A pass with no event polls again, and the deadline ends the wait with TICK.
	h2 := newHarness(t)
	polls := 0
	var b bytes.Buffer
	d := Deps{Client: h2.client, Stdout: &b, Now: func() time.Time { return h2.now },
		Sleep: func(dd time.Duration) bool { polls++; h2.now = h2.now.Add(dd); return true }}
	env := func(k string) (string, bool) {
		v, ok := map[string]string{"DEADLINE": "120", "POLL": "60", "HERDSMAN_IMPLEMENTERS": "", "HERDSMAN_STATE": filepath.Join(h2.dir, "s"),
			"HERDSMAN_REPORTS": h2.env["HERDSMAN_REPORTS"]}[k]
		return v, ok
	}
	Wait(context.Background(), env, d)
	if polls != 2 || !strings.HasPrefix(b.String(), "TICK no event in 120s ") {
		t.Errorf("deadline: %d polls, %q", polls, b.String())
	}
	// The default settings (the reports folder of the template does not
	// exist) and a stopped sleep.
	b.Reset()
	d.Sleep = func(time.Duration) bool { return false }
	d.Client = nil
	st := filepath.Join(h2.dir, "defaults")
	only := func(k string) (string, bool) {
		if k == "HERDSMAN_STATE" {
			return st, true
		}
		return "", false
	}
	if Wait(context.Background(), only, d) != 0 || b.String() != "" {
		t.Errorf("default settings: %q", b.String())
	}
}

// The default sleep waits its time, and ends at once, false, when the
// context ends (an interrupt of the command).
func TestDefaultSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var d Deps
	d.defaults(ctx)
	if !d.Sleep(time.Millisecond) {
		t.Error("a short sleep ends the loop")
	}
	cancel()
	start := time.Now()
	if d.Sleep(time.Hour) || time.Since(start) > 10*time.Second {
		t.Error("an ended context does not stop the sleep")
	}
}

func TestSmallHelpers(t *testing.T) {
	if paneOf(func(string) (string, bool) { return "", false }, "rv-x") != "" {
		t.Error("an unknown agent has a pane")
	}
	if first(regexp.MustCompile("a\nb"), "a\nb") != "" {
		t.Error("a match across lines is no line")
	}
	if jqCompare(json.Number("2"), json.Number("2.0")) != 0 {
		t.Error("equal numbers")
	}
	if jqCompare(json.Number("3"), json.Number("2")) != 1 {
		t.Error("a greater number")
	}
}

// (Go) The waiter's implementer cases the shell suite leaves out: a blocked
// implementer (once per screen), an updated report, and a pane it cannot read.
func TestWaiterImplementerEvents(t *testing.T) {
	h := newHarness(t)
	reports := h.env["HERDSMAN_REPORTS"]
	h.screen("w9:p1", "Password for 'https://github.com':")
	h.screen("w9:p2", "Pursuing goal (1m)")
	h.screen("w9:p3", "· Incubating…")
	match(t, "a blocked implementer", `BLOCKED im-a Password for`, h.wait())
	noMatch(t, "an unchanged blocked screen is silent", `BLOCKED`, h.wait())
	report := filepath.Join(reports, "a.md")
	h.file(report, "STATUS: complete\n")
	match(t, "a report", `REPORT a `, h.wait())
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(report, later, later)
	match(t, "an updated report", `REPORT-UPDATED a `, h.wait())
	h.fx.Fail("pane.read", "temporary_failure")
	h.wait()
}

// R3 sweep (a): a reports folder whose name holds [ lists its own reviews.
func TestReviewsInABracketedFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rep[x]")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "review-7-1.md"), []byte("VERDICT: fix\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "review-x"), nil, 0o644)
	if got := reviews(dir); len(got) != 1 || got[0] != filepath.Join(dir, "review-7-1.md") {
		t.Errorf("reviews: %v", got)
	}
	if reviews(filepath.Join(dir, "none")) != nil {
		t.Error("a missing folder has reviews")
	}
}

// A25: the waiter's settings are strict; padded and leading-zero values
// read as test reads them, anything else is one line and exit 2.
func TestWaiterStrictSettings(t *testing.T) {
	h := newHarness(t)
	h.screen("w9:p1", "• Working (1s • esc to interrupt)")
	h.screen("w9:p2", "Pursuing goal (1m)")
	h.screen("w9:p3", "· Incubating…")
	match(t, "a padded DEADLINE", `^TICK no event`, h.wait("DEADLINE", " 0 "))
	match(t, "a DEADLINE of 00", `^TICK no event`, h.wait("DEADLINE", "00"))
	for _, tc := range [][3]string{
		{"DEADLINE", "0x0", "not a duration"}, {"DEADLINE", "-1", "not a duration"}, {"POLL", "0", "must be at least 1 second"},
		{"POLL", "x", "not a duration"}, {"HERDSMAN_HEALTH_EVERY", "x", "not a duration"}, {"HEALTH_EVERY", "1/0", "not a duration"},
		{"HERDSMAN_DUE_im_a", "soon", "not a whole number"},
	} {
		extra := []string{"DEADLINE", "0", "POLL", "1", tc[0], tc[1]}
		if tc[0] == "HEALTH_EVERY" {
			extra = append(extra, "HERDSMAN_HEALTH_EVERY", "\x00")
		}
		out, code := h.run(Wait, extra...)
		want := fmt.Sprintf("herdsman wait: %s: %s", tc[0], tc[2])
		if code != 2 || !strings.HasPrefix(out, want) || !strings.HasSuffix(out, fmt.Sprintf("(got %q)\n", tc[1])) || strings.Count(out, "\n") != 1 {
			t.Errorf("%s=%q: %d %q", tc[0], tc[1], code, out)
		}
	}
	// A huge DEADLINE clamps to a day instead of overflowing.
	noMatch(t, "a huge DEADLINE", `TICK|herdsman wait`, h.wait("DEADLINE", "99999999999999999999"))
	h.env["HERDSMAN_STATUS_SPECS"] = "im-a:w9:p1:"
	h.status = "12:00:00\nmachine: load 9.00 (5 min) on 8 cores, memory free 50%\n   <-- LOAD HIGH\n"
	h.wait("HERDSMAN_HEALTH_EVERY", " 0")
	match(t, "a padded HERDSMAN_HEALTH_EVERY", `HEALTH machine LOAD HIGH`, h.wait("HERDSMAN_HEALTH_EVERY", " 0"))
	delete(h.env, "HERDSMAN_STATUS_SPECS")
	past := strconv.FormatInt(h.now.Add(-time.Hour).Unix(), 10)
	match(t, "a padded due time is overdue", `OVERDUE im-a due `, h.wait("HERDSMAN_DUE_im_a", " "+past))
}

// A26: the script's $(( )) on its own state files ended the whole pass on
// a value bash could not read (08, or junk); the waiter reads 08 as 8 and
// says what it does with junk.
func TestWaiterStateNumbers(t *testing.T) {
	h := newHarness(t)
	h.screen("w9:p1", "• Working (1s • esc to interrupt)")
	h.screen("w9:p2", "Pursuing goal (1m)")
	h.screen("w9:p3", "· Incubating…")
	h.wait()
	st := h.env["HERDSMAN_STATE"]
	h.file(filepath.Join(st, "stall-im-a"), "08\n")
	match(t, "a stall count of 08 is 8, so the screen has stalled", `STALL im-a screen unchanged`, h.wait())
	h.file(filepath.Join(st, "stall-im-a"), "x\n")
	out := h.wait()
	match(t, "a junk stall count is reported", `herdsman wait: state file stall-im-a is not a number \(got "x"\); counting from 0`, out)
	if b, _ := os.ReadFile(filepath.Join(st, "stall-im-a")); strings.TrimSpace(string(b)) != "1" {
		t.Errorf("stall count after junk: %q", b)
	}
	// health-at: junk restarts the clock, with a line, and no check runs.
	h.env["HERDSMAN_STATUS_SPECS"] = "im-a:w9:p1:"
	h.status = "12:00:00\nmachine: load 9.00 (5 min) on 8 cores, memory free 50%\n   <-- LOAD HIGH\n"
	h.file(filepath.Join(st, "health-at"), "08x\n")
	out = h.wait("HERDSMAN_HEALTH_EVERY", "0")
	match(t, "a junk health time is reported", `herdsman wait: state file health-at is not a number \(got "08x"\); the health clock starts again`, out)
	noMatch(t, "and no check runs on it", `HEALTH`, out)
	match(t, "the next pass checks", `HEALTH machine LOAD HIGH`, h.wait("HERDSMAN_HEALTH_EVERY", "0"))
	h.file(filepath.Join(st, "health-at"), strconv.FormatInt(h.now.Unix(), 10)+"\n")
	h.status = "12:00:00\nmachine: load 9.00 (5 min) on 8 cores, memory free 5%\n   <-- MEMORY LOW\n"
	noMatch(t, "a fresh health time waits", `HEALTH`, h.wait("HERDSMAN_HEALTH_EVERY", "300"))
}

// A25 and A26 for the PR watcher.
func TestPRWatcherStrictSettings(t *testing.T) {
	r := newPRRig(t)
	r.fixture("7-view.json", view("OPEN", "abcd1234ffff"))
	out, _ := r.run("7", "s1", 0, "HERDSMAN_PR_DEADLINE", " 0")
	match(t, "a padded deadline", `^TICK no PR change in 0s `, out)
	for _, tc := range [][3]string{
		{"HERDSMAN_PR_DEADLINE", "x", "not a duration"}, {"HERDSMAN_PR_INTERVAL", "0", "must be at least 1 second"},
		{"HERDSMAN_PR_INTERVAL", "-3", "not a duration"}, {"HERDSMAN_PR_WAKE_CHECKS", "(", "not a pattern"},
	} {
		out, code := r.run("7", "s2", 0, tc[0], tc[1])
		if code != 2 || !strings.HasPrefix(out, "herdsman watch prs: "+tc[0]+": "+tc[2]) || strings.Count(out, "\n") != 1 {
			t.Errorf("%s=%q: %d %q", tc[0], tc[1], code, out)
		}
	}
	// A huge interval clamps to a day.
	var slept time.Duration
	env := map[string]string{"HERDSMAN_PRS": "7", "HERDSMAN_REPO": "org/repo", "HERDSMAN_PR_STATE": filepath.Join(r.dir, "s3"),
		"HERDSMAN_PR_INTERVAL": "99999999999999999999"}
	var b bytes.Buffer
	r.gh.err = &b
	PRs(context.Background(), func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Deps{Stdout: &b, Stderr: &b, Gh: r.gh.run, Sleep: func(d time.Duration) bool { slept = d; return false }})
	if slept != 24*time.Hour {
		t.Errorf("a huge interval: slept %v", slept)
	}
	// A junk fail count is reported and counted from 0.
	r.touch("8-fail")
	_ = os.MkdirAll(filepath.Join(r.dir, "s4"), 0o755)
	_ = os.WriteFile(filepath.Join(r.dir, "s4", "fail-8"), []byte("x\n"), 0o644)
	out, code := r.run("8", "s4", 0, "_", "")
	match(t, "a junk fail count", `herdsman watch prs: state file fail-8 is not a number \(got "x"\); counting from 0`, out)
	if b, _ := os.ReadFile(filepath.Join(r.dir, "s4", "fail-8")); strings.TrimSpace(string(b)) != "1" || code != 0 {
		t.Errorf("fail count after junk: %q exit %d", b, code)
	}
	_ = os.WriteFile(filepath.Join(r.dir, "s4", "fail-8"), []byte("02\n"), 0o644)
	out, code = r.run("8", "s4", 0, "_", "")
	match(t, "a fail count of 02 is 2: the third failure", `PR-WATCH-ERROR PR 8 could not be read`, out)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
}

// A25 item 3: Herdr's agent status comes first. A working pane whose
// printed output shows "Password for" above its last 5 lines raises no
// BLOCKED; the same text on the last lines does; Herdr's "blocked" does.
func TestBlockedFromHerdrStatusFirst(t *testing.T) {
	for _, run := range []struct {
		name string
		fn   func(context.Context, Env, Deps) int
	}{{"wait", Wait}, {"implementers", Implementers}} {
		h := newHarness(t)
		h.env["DEADLINE"], h.env["POLL"] = "0", "1"
		agent := func(pane, st string) {
			a := herdrtest.Agent(pane, "s")
			a["agent_status"] = st
			h.fx.SetAgent(pane, a)
		}
		agent("w9:p1", "working")
		agent("w9:p2", "working")
		h.screen("w9:p2", "Pursuing goal (1m)")
		h.screen("w9:p1", "$ git log", "commit abc", "    fix: Password for prompt in the docs", "line", "line", "line", "line",
			"• Working (3m 10s • esc to interrupt)")
		h.screen("w9:p3", "· Incubating…")
		out, _ := h.run(run.fn)
		noMatch(t, run.name+": printed output of a working pane", `BLOCKED`, out)
		h.screen("w9:p1", "line", "Password for 'https://x@github.com':")
		out, _ = h.run(run.fn)
		match(t, run.name+": the prompt on the last lines", `BLOCKED im-a Password for`, out)
		agent("w9:p2", "blocked")
		h.screen("w9:p2", "some question the patterns do not know")
		out, _ = h.run(run.fn)
		match(t, run.name+": Herdr's blocked status", `BLOCKED im-b `, out)
	}
}

// R5 finding 11: a stored health time outside 0..now is invalid state:
// reported, and the clock starts again, so HEALTH is not suppressed forever.
func TestWaiterHealthEpochBounds(t *testing.T) {
	h := newHarness(t)
	h.env["HERDSMAN_STATUS_SPECS"] = "im-a:w9:p1:"
	h.status = "12:00:00\nmachine: load 9.00 (5 min) on 8 cores, memory free 50%\n   <-- LOAD HIGH\n"
	st := h.env["HERDSMAN_STATE"]
	for _, v := range []string{"-9223372036854775808", "-1", strconv.FormatInt(h.now.Add(time.Hour).Unix(), 10)} {
		h.file(filepath.Join(st, "health-at"), v+"\n")
		out := h.wait("HERDSMAN_HEALTH_EVERY", "0")
		match(t, "an out-of-range health time "+v, `herdsman wait: state file health-at is out of range \(got "`+regexp.QuoteMeta(v)+`"\); the health clock starts again`, out)
		match(t, "the next pass checks after "+v, `HEALTH machine LOAD HIGH`, h.wait("HERDSMAN_HEALTH_EVERY", "0"))
		_ = os.Remove(filepath.Join(st, "health-flags"))
	}
}

// R5 finding 7 (A28): the model-switch prompt is matched by its shape, for
// any model name, and no model family is written in the detector.
func TestModelSwitchPromptByShape(t *testing.T) {
	for _, re := range []*regexp.Regexp{waitBlockRE, implBlockRE} {
		for _, s := range []string{"Switch to model-z for the rest of this task?", "› Switch to effort-x-model-9 now"} {
			if got := first(re, s); !strings.HasPrefix(got, "Switch to ") {
				t.Errorf("%q: %q", s, got)
			}
		}
		if strings.Contains(strings.ToLower(re.String()), "gpt") {
			t.Errorf("a model family in %s", re)
		}
	}
}

// R5 findings 2 and 3: a working -> blocked change from Herdr is reported
// even when the pane text stays the same, once; and a stalled-goal line in
// a working agent's printed output is no block, on implementer and
// reviewer panes alike.
func TestBlockedTransitionsAndStalledFilter(t *testing.T) {
	for _, tc := range []struct {
		name, pane, who string
		fn              func(context.Context, Env, Deps) int
	}{
		{"wait implementer", "w9:p1", "im-a", Wait}, {"wait reviewer", "w9:p3", "rv-1", Wait},
		{"watch implementers", "w9:p1", "im-a", Implementers}, {"watch reviewer", "w9:p3", "rv-1", Reviewer},
	} {
		h := newHarness(t)
		h.env["DEADLINE"], h.env["POLL"] = "0", "1"
		agent := func(st string) {
			a := herdrtest.Agent(tc.pane, "s")
			a["agent_status"] = st
			h.fx.SetAgent(tc.pane, a)
		}
		h.screen("w9:p1", "plain text")
		h.screen("w9:p2", "Pursuing goal (1m)")
		h.screen("w9:p3", "plain text")
		agent("working")
		h.run(tc.fn)
		agent("blocked")
		out, _ := h.run(tc.fn)
		match(t, tc.name+": blocked with the same text", `BLOCKED `+tc.who+` agent blocked`, out)
		out, _ = h.run(tc.fn)
		noMatch(t, tc.name+": reported once", `BLOCKED `+tc.who, out)
		agent("working")
		h.run(tc.fn)
		agent("blocked")
		out, _ = h.run(tc.fn)
		match(t, tc.name+": blocked again after working", `BLOCKED `+tc.who, out)
		if strings.HasPrefix(tc.name, "wait") {
			agent("working")
			h.screen(tc.pane, "$ cat notes", "Goal stalled in the old run", "a", "b", "c", "d", "e")
			out, _ = h.run(tc.fn)
			noMatch(t, tc.name+": a stalled line in printed output", `BLOCKED `+tc.who, out)
		}
	}
}
