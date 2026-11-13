package route_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// startCore drives a core start to done with an attested shell.
func startCore(t *testing.T, r *coreRig, e *ops.Engine, q route.UpRequest) (*route.UpPlan, *ops.Operation) {
	t.Helper()
	p := r.plan(q)
	op := drive(t, e, p.Spec)
	r.attestShell(p.Key)
	for op.State != ops.OpDone {
		if err := route.Continue(e, r.callers, p, op, nil); err != nil {
			t.Fatal(err)
		}
		var err error
		if op, err = e.Drive(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
		if op.State == ops.OpFailed {
			t.Fatalf("start failed: %q", lastError(op))
		}
	}
	start, err := e.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p, start
}

func (r *coreRig) execByRole(t *testing.T, role string) runtime.Execution {
	t.Helper()
	all, err := r.w.Records.List(context.Background(), r.repo, "harbor")
	if err != nil {
		t.Fatal(err)
	}
	var out runtime.Execution
	for _, x := range all {
		if x.Role == role && x.Desired == runtime.DesiredRunning {
			out = x
		}
	}
	if out.ID == "" {
		t.Fatalf("no running %s in %+v", role, all)
	}
	return out
}

func controlPlan(t *testing.T, start *ops.Operation, v route.Verb, gen int, edit func(*route.ControlRequest)) *route.ControlPlan {
	t.Helper()
	q := route.ControlRequest{Verb: v, Start: start, Generation: gen, Date: "2026-11-11"}
	if edit != nil {
		edit(&q)
	}
	plan, err := route.PlanControl(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// holder is an agent's background `herdsman notice wait`, polled until ctx
// ends; report says whether it claims and acknowledges a final report.
type holder struct {
	mu      sync.Mutex
	answers []notice.WaitAnswer
}

func (h *holder) hold(ctx context.Context, svc *notice.Service, e runtime.Execution, id string, report bool) {
	var gen int64
	after := ""
	for ctx.Err() == nil {
		a, err := svc.Wait(ctx, e, notice.WaitRequest{WaitID: id, Generation: gen, After: after})
		if err != nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if a.Answer == notice.AnswerReplaced || a.Answer == notice.AnswerStopped {
			return
		}
		gen = a.Generation
		if a.Answer != notice.AnswerNotice && a.Answer != notice.AnswerRecovery {
			continue
		}
		h.mu.Lock()
		h.answers = append(h.answers, a)
		h.mu.Unlock()
		after = a.Notice.ID
		if report && a.Notice.Kind == notice.KindFinalReport {
			got, err := svc.Read(ctx, e, a.Notice.ID)
			if err == nil && got.Token != "" {
				_, _ = svc.Ack(ctx, e, a.Notice.ID, got.Token, notice.OutcomeCompleted, "wrote the report")
			}
		}
	}
}

func (h *holder) answer(i int) notice.WaitAnswer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.answers[i]
}

func (h *holder) kinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, a := range h.answers {
		out = append(out, a.Answer+":"+a.Notice.Kind)
	}
	return out
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Wake without input: Claude's start line has it hold a background wait, and
// its first brief reaches it through that wait with nothing typed.
// Stop reports and interrupt discipline: a stop asks every execution that
// holds a wait for its final report, waits for the reports or their
// deadline with nothing typed, interrupts only what is still in a turn,
// once, and records each execution's report outcome.
func TestCoreStopReportsAndInterruptDiscipline(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	p, start := startCore(t, r, e, r.request())
	svc := r.w.Notices
	adjutant, orchestrator := r.execByRole(t, "adjutant"), r.execByRole(t, "orchestrator")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ha, ho holder
	go ha.hold(ctx, svc, adjutant, "wait-a", true)
	go ho.hold(ctx, svc, orchestrator, "wait-o", false)
	waitUntil(t, "both briefs to wake their agents", func() bool {
		return len(ha.kinds()) > 0 && len(ho.kinds()) > 0
	})
	if got := ha.kinds()[0]; got != "notice:brief" {
		t.Fatalf("adjutant's first wake: %s", got)
	}
	// The adjutant handles its brief and rests; the orchestrator claims its
	// brief and is still in that turn when the stop comes.
	brief := ha.answer(0).Notice
	got, err := svc.Read(ctx, adjutant, brief.ID)
	if err != nil || !strings.Contains(got.Content, "herdsman adjutant --name") {
		t.Fatalf("adjutant's brief: %+v %v", got, err)
	}
	if _, err := svc.Ack(ctx, adjutant, brief.ID, got.Token, notice.OutcomeCompleted, "briefed"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(ctx, orchestrator, ho.answer(0).Notice.ID); err != nil {
		t.Fatal(err)
	}

	op := drive(t, e, controlPlan(t, start, route.VerbStop, 1, func(q *route.ControlRequest) { q.Grace = 700 * time.Millisecond }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("stop: %s %q", op.State, lastError(op))
	}
	rec, _ := route.ReadControlRecord(p.Folder)
	statuses := map[string]string{}
	for _, row := range rec.Agents {
		statuses[row.Name] = row.Status
	}
	if s := statuses[adjutant.Agent]; !strings.HasPrefix(s, notice.ReportReported) {
		t.Errorf("adjutant: %q", s)
	}
	if s := statuses[orchestrator.Agent]; !strings.HasPrefix(s, notice.ReportTimeout+" (unclaimed)") {
		t.Errorf("orchestrator: %q", s)
	}
	if n := r.w.Core.Calls("terminal.interrupt"); n != 1 {
		t.Errorf("interrupts = %d, want 1 (the orchestrator in its turn)", n)
	}
	if n := len(r.w.EventsOf("report_outcome")); n != 2 {
		t.Errorf("report_outcome events = %d", n)
	}
	// No automated submit: nothing on the core path typed or keyed.
	for _, m := range []string{"terminal.send", "terminal.keys"} {
		if n := r.w.Core.Calls(m); n != 0 {
			t.Errorf("%s called %d times", m, n)
		}
	}
	// After the stop, nothing is served to the stopped executions.
	for _, x := range []runtime.Execution{adjutant, orchestrator} {
		list, _ := svc.List(x.ID)
		for _, n := range list {
			if n.State == notice.StateIssued || n.State == notice.StateClaimed {
				t.Errorf("%s left %s %s", x.Agent, n.Kind, n.State)
			}
		}
	}
}

// An execution that holds no wait cannot be asked: its report is
// unavailable at once, and the stop does not wait for it.
func TestCoreStopWithoutAWaitIsUnavailable(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	p, start := startCore(t, r, e, q)
	orchestrator := r.execByRole(t, "orchestrator")
	began := time.Now()
	op := drive(t, e, controlPlan(t, start, route.VerbStop, 1, func(q *route.ControlRequest) { q.Grace = time.Minute }).Spec)
	if op.State != ops.OpDone || time.Since(began) > 30*time.Second {
		t.Fatalf("stop: %s %q after %s", op.State, lastError(op), time.Since(began))
	}
	rec, _ := route.ReadControlRecord(p.Folder)
	if len(rec.Agents) != 1 || !strings.HasPrefix(rec.Agents[0].Status, notice.ReportUnavailable+" ("+"not_waiting") {
		t.Fatalf("row: %+v (%s)", rec.Agents, orchestrator.Agent)
	}
	if n := r.w.Core.Calls("terminal.interrupt"); n != 0 {
		t.Errorf("an agent at rest was interrupted %d times", n)
	}
}

// Stop reports across a daemon restart: a daemon that stops while the stop
// waits for reports comes back, moves each deadline once (no agent could
// reach it while it was down), and finishes the stop with the reports.
func TestCoreStopReportsSurviveADaemonRestart(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	first, closeFirst := r.engine()
	q := r.request()
	q.NoAdjutant = true
	p, start := startCore(t, r, first, q)
	orchestrator := r.execByRole(t, "orchestrator")
	ctx, cancel := context.WithCancel(context.Background())
	var h holder
	go h.hold(ctx, r.w.Notices, orchestrator, "wait-1", false)
	waitUntil(t, "the first wait", func() bool { return r.w.Notices.Holding(orchestrator.ID) })

	plan := controlPlan(t, start, route.VerbStop, 1, func(q *route.ControlRequest) { q.Grace = 2 * time.Second })
	reports := -1
	for i, s := range plan.Spec.Steps {
		if s.Key == "core-reports" {
			reports = i
		}
	}
	r.crashOn = "step:" + itoa(reports) + ":intended"
	crashDriveIfReached(t, first, plan.Spec)
	closeFirst()
	r.crashOn = ""
	cancel()

	// The daemon comes back: a new notice service on the same store, with
	// its own boot. The agent's wait connects again and reports.
	r.w.Notices = r.w.NewNotices(notice.Config{Poll: 200 * time.Millisecond, Boot: "boot-2"})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var h2 holder
	go h2.hold(ctx2, r.w.Notices, orchestrator, "wait-2", true)
	second, closeSecond := r.engine()
	defer closeSecond()
	if err := second.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, err := second.List()
	if err != nil {
		t.Fatal(err)
	}
	op, err := second.Drive(context.Background(), all[len(all)-1].ID)
	if err != nil || op.State != ops.OpDone {
		t.Fatalf("stop after the restart: %v %s %q", err, op.State, lastError(op))
	}
	list, err := r.w.Notices.List(orchestrator.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved := 0
	for _, n := range list {
		if n.Kind == notice.KindFinalReport && n.DeadlineMoved && n.State == notice.StateHandled {
			moved++
		}
	}
	if moved != 1 {
		t.Fatalf("final reports moved once and handled: %d of %+v", moved, list)
	}
	rec, _ := route.ReadControlRecord(p.Folder)
	if len(rec.Agents) != 1 || !strings.HasPrefix(rec.Agents[0].Status, notice.ReportReported) {
		t.Fatalf("row: %+v", rec.Agents)
	}
}

// Pause and resume by adapter: a pause holds the route, interrupts a Claude
// turn once and an agent at rest never; the resume lifts the hold and the
// interrupted claim comes back to its wait as a recovery answer.
func TestCorePauseInATurnAndResume(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	_, start := startCore(t, r, e, q)
	svc := r.w.Notices
	orchestrator := r.execByRole(t, "orchestrator")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var h holder
	go h.hold(ctx, svc, orchestrator, "wait-1", false)
	waitUntil(t, "the brief", func() bool { return len(h.kinds()) > 0 })
	brief := h.answer(0).Notice
	if _, err := svc.Read(ctx, orchestrator, brief.ID); err != nil {
		t.Fatal(err)
	}
	r.w.Core.SetStatus(orchestrator.TerminalAddress, "working")

	if op := drive(t, e, controlPlan(t, start, route.VerbPause, 1, nil).Spec); op.State != ops.OpDone {
		t.Fatalf("pause: %s %q", op.State, lastError(op))
	}
	if n := r.w.Core.Calls("terminal.interrupt"); n != 1 {
		t.Fatalf("interrupts = %d, want 1", n)
	}
	if rs, _ := svc.RouteState(r.repo, "harbor"); !rs.Held {
		t.Fatal("the pause did not hold the route")
	}
	// Under the hold nothing new is served.
	if _, err := svc.Issue(ctx, notice.IssueRequest{Key: "t:held", Kind: notice.KindDispatch, Execution: orchestrator.ID, Content: "x", Issuer: "op-t"}); notice.GateOf(err) != notice.GateHeld {
		t.Fatalf("issue under a hold: %v", err)
	}

	r.w.Core.SetStatus(orchestrator.TerminalAddress, "idle")
	if op := drive(t, e, controlPlan(t, start, route.VerbResume, 2, func(q *route.ControlRequest) { q.From = route.ControlPaused }).Spec); op.State != ops.OpDone {
		t.Fatalf("resume: %s %q", op.State, lastError(op))
	}
	if rs, _ := svc.RouteState(r.repo, "harbor"); rs.Held {
		t.Fatal("the resume left the hold")
	}
	waitUntil(t, "the recovery answer", func() bool {
		for _, k := range h.kinds() {
			if k == "recovery:brief" {
				return true
			}
		}
		return false
	})
	if n := r.w.Core.Calls("terminal.interrupt"); n != 1 {
		t.Errorf("interrupts after resume = %d", n)
	}
	for _, m := range []string{"terminal.send", "terminal.keys"} {
		if n := r.w.Core.Calls(m); n != 0 {
			t.Errorf("%s called %d times", m, n)
		}
	}
}

// Wait capacity: a launch that could not hold its wait is refused in
// preflight, before any terminal starts.
func TestCoreLaunchBeyondTheWaitBudgetIsRefused(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	r.w.Notices = r.w.NewNotices(notice.Config{MaxWaits: 1})
	e, closeDB := r.engine()
	defer closeDB()
	p := r.plan(r.request())
	r.attestShell(p.Key)
	op := drive(t, e, p.Spec)
	for range 4 {
		if op.State != ops.OpStopped || strings.Contains(lastError(op), "wait_budget") {
			break
		}
		if err := route.Continue(e, r.callers, p, op, nil); err != nil {
			t.Fatal(err)
		}
		var err error
		if op, err = e.Drive(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(lastError(op), "wait_budget") {
		t.Fatalf("second launch: %s %q", op.State, lastError(op))
	}
	if live := len(r.w.Core.Live()); live != 1 {
		t.Fatalf("terminals = %d, want 1", live)
	}
}
