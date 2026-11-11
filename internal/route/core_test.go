package route_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// coreRig is a route rig whose agents launch on a fake herdr-core.
type coreRig struct {
	*rig
	w *coretest.World
}

func newCoreRig(t *testing.T) *coreRig {
	t.Helper()
	r := &coreRig{rig: newRig(t), w: coretest.New(t)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.w.AutoWrap(ctx)
	return r
}

func (r *coreRig) request() route.UpRequest {
	q := r.rig.request("claude", "claude")
	q.HerdrSocket = ""
	q.Backend = runtime.BackendCore
	q.Core = &route.CoreLaunch{
		Socket: r.w.Core.Socket, Owner: "install-1", Wrapper: "/usr/local/bin/herdsman", LaunchSocket: r.w.Registrar.Path,
		Login: map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/home/u"}, LoginFrom: "daemon",
		Timeouts: core.Timeouts{Register: 5 * time.Second, Ready: 5 * time.Second, Visibility: 300 * time.Millisecond, Poll: 10 * time.Millisecond},
	}
	return q
}

func (r *coreRig) engine() (*ops.Engine, func()) {
	r.t.Helper()
	reg := runtime.Registry{runtime.BackendCore: func(endpoint string) (runtime.Provider, error) { return r.w.NewProvider(endpoint), nil }}
	return r.rig.engineDeps(route.Deps{Callers: r.callers, PollEvery: time.Millisecond, AgentTimeout: 5 * time.Second, Runtime: reg})
}

// attestShell makes the caller a person's shell outside every agent.
func (r *coreRig) attestShell(key string) {
	r.callers.Set(key, route.Caller{PID: os.Getpid(), Started: ownStart(r.t), TTY: "/dev/ttys-fake"})
}

func lastError(op *ops.Operation) string {
	out := op.Reason
	for _, st := range op.Steps {
		if st.Detail != "" {
			out += "\n" + st.Key + ": " + st.Detail
		}
	}
	return out
}

// Core path: up on herdr-core spawns each agent through the wrapper, never
// types into a terminal, and stops at its two waits for a person: an app that
// never showed the core, and each harness's start line. Continue settles them
// and the pair is enrolled for an attested shell.
func TestCoreUpWaitsForAPersonTwiceThenEnrolls(t *testing.T) {
	r := newCoreRig(t)
	e, closeDB := r.engine()
	defer closeDB()
	p := r.plan(r.request())
	if strings.Contains(p.Text, "pane") {
		t.Errorf("a core plan names a pane:\n%s", p.Text)
	}
	op := drive(t, e, p.Spec)
	if op.State != ops.OpStopped || !strings.Contains(lastError(op), core.CodeWaitingVisibility) {
		t.Fatalf("first wait: %s %q", op.State, lastError(op))
	}

	r.w.OpenApp()
	cont := func() *ops.Operation {
		t.Helper()
		if err := route.Continue(e, r.callers, p, op, nil); err != nil {
			t.Fatal(err)
		}
		got, err := e.Drive(context.Background(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	op = cont()
	if op.State != ops.OpStopped || !strings.Contains(lastError(op), core.CodeNeedsAttention) || !strings.Contains(lastError(op), "proj-harbor-adjutant") || !strings.Contains(lastError(op), "herdsman notice wait") {
		t.Fatalf("second wait: %s %q", op.State, lastError(op))
	}
	op = cont()
	if op.State != ops.OpStopped || !strings.Contains(lastError(op), "orchestrator") || !strings.Contains(lastError(op), "herdsman notice wait") {
		t.Fatalf("orchestrator's start line: %s %q", op.State, lastError(op))
	}
	// No caller: the pair is left pending, the start is not done.
	op = cont()
	if op.State != ops.OpStopped || !strings.Contains(lastError(op), "pair is left pending") {
		t.Fatalf("pair without a caller: %s %q", op.State, lastError(op))
	}
	r.attestShell(p.Key)
	op = cont()
	if op.State != ops.OpDone {
		t.Fatalf("after attestation: %s %q", op.State, lastError(op))
	}
	if n := r.w.Core.Calls("terminal.send"); n != 0 {
		t.Errorf("up typed into core %d times", n)
	}
	// The role commands that the start line used to carry are the first
	// brief of each agent, issued as a notice once the pair is enrolled.
	briefs, err := r.w.Notices.RouteList(r.repo, "harbor")
	if err != nil {
		t.Fatal(err)
	}
	var adjutant, orchestrator bool
	for _, n := range briefs {
		if n.Kind != notice.KindBrief || n.State != notice.StateIssued {
			t.Errorf("first brief %s: %s %s", n.ID, n.Kind, n.State)
		}
		adjutant = adjutant || strings.Contains(n.Content, "herdsman adjutant --name")
		orchestrator = orchestrator || strings.Contains(n.Content, "herdsman orchestrator --adjutant")
	}
	if len(briefs) != 2 || !adjutant || !orchestrator {
		t.Errorf("briefs: %d adjutant=%v orchestrator=%v", len(briefs), adjutant, orchestrator)
	}
	if _, err := os.Stat(filepath.Join(p.Folder, ".steps", "core-pair.json")); err != nil {
		t.Errorf("no pair record: %v", err)
	}
	execs, err := r.w.Records.List(context.Background(), r.repo, "harbor")
	if err != nil || len(execs) != 2 {
		t.Fatalf("records: %v %d", err, len(execs))
	}
	for _, x := range execs {
		if x.Evidence.PID == 0 || x.Backend != runtime.BackendCore || x.Desired != runtime.DesiredRunning {
			t.Errorf("record %+v", x)
		}
		if got := r.w.Execed(x.LaunchIntent); len(got) == 0 || got[0] != "/usr/local/bin/claude" {
			t.Errorf("%s ran %v", x.Agent, got)
		}
	}
}

// Caller identity: a start whose caller is an agent execution is never
// enrolled, even from a terminal.
func TestCoreEnrollRefusesAnAgentCaller(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	p := r.plan(q)
	op := drive(t, e, p.Spec)
	if !strings.Contains(lastError(op), core.CodeNeedsAttention) {
		t.Fatalf("start line: %s %q", op.State, lastError(op))
	}
	// The prompt is in the start line: it goes to a private file, never into
	// the reason.
	if strings.Contains(lastError(op), "fix the login redirect") {
		t.Errorf("the prompt is in the reason: %q", lastError(op))
	}
	r.callers.Set(p.Key, route.Caller{PID: os.Getpid(), Started: ownStart(t), TTY: "/dev/ttys-fake", Execution: "e-agent"})
	if err := route.Continue(e, r.callers, p, op, nil); err != nil {
		t.Fatal(err)
	}
	op, _ = e.Drive(context.Background(), op.ID)
	if op.State == ops.OpDone || !strings.Contains(lastError(op), "e-agent, an agent") {
		t.Fatalf("an agent enrolled the pair: %s %q", op.State, lastError(op))
	}
}

// The fallbacks of the open owner decisions: no restricted worker on core,
// no manual pair, no restricted Codex, and Codex only with --no-daemon.
func TestCorePlanRefusals(t *testing.T) {
	r := newCoreRig(t)
	restricted := r.request()
	restricted.Policy = policy.Policy{Profile: policy.ProfileRestricted}
	manual := r.request()
	manual.PairManual = true
	codex := r.request()
	codex.OrchestratorCommand = "codex"
	noCore := r.request()
	noCore.Core = nil
	for name, tc := range map[string]struct {
		q    route.UpRequest
		want string
	}{
		"restricted": {restricted, "restricted"},
		"manual":     {manual, "--pair manual"},
		"codex":      {codex, "--no-daemon"},
		"no core":    {noCore, "launch socket"},
	} {
		if _, err := route.PlanUp(tc.q); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Pause and stop act through the provider; a stopped route resumes under new
// reservations.
func TestCoreControlPausesStopsAndResumes(t *testing.T) {
	r := newCoreRig(t)
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
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
	verb := func(v route.Verb, gen int, from string) *ops.Operation {
		t.Helper()
		plan, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: v, Start: start, Generation: gen, From: from, Date: "2026-11-11"})
		if err != nil {
			t.Fatal(err)
		}
		return drive(t, e, plan.Spec)
	}
	if got := verb(route.VerbPause, 1, ""); got.State != ops.OpDone {
		t.Fatalf("pause: %s %q", got.State, lastError(got))
	}
	rec, _ := route.ReadControlRecord(p.Folder)
	if rec == nil || len(rec.Agents) != 1 || rec.Agents[0].Status != core.PauseLeftWaiting {
		t.Fatalf("pause record %+v", rec)
	}
	if n := r.w.Core.Calls("terminal.interrupt"); n != 0 {
		t.Errorf("a pause interrupted an agent at rest %d times", n)
	}
	if got := verb(route.VerbStop, 2, ""); got.State != ops.OpDone {
		t.Fatalf("stop: %s %q", got.State, lastError(got))
	}
	execs, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
	if len(execs) != 1 || execs[0].Desired != runtime.DesiredStopped {
		t.Fatalf("after stop: %+v", execs)
	}
	if _, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbDelete, Start: start, Generation: 3, Date: "2026-11-11"}); err == nil {
		t.Error("delete planned on core")
	}
	resumed := verb(route.VerbResume, 3, route.ControlStopped)
	if resumed.State != ops.OpStopped || !strings.Contains(lastError(resumed), core.CodeNeedsAttention) {
		t.Fatalf("resume: %s %q", resumed.State, lastError(resumed))
	}
	execs, _ = r.w.Records.List(context.Background(), r.repo, "harbor")
	if len(execs) != 2 || execs[0].LaunchIntent == execs[1].LaunchIntent {
		t.Fatalf("a resume reused its reservation: %+v", execs)
	}
}

// Launch crash points: a daemon killed at any step boundary of a core start
// and restarted never leaves two terminals or two records for one agent; the
// restarted start either finishes or stops for a person.
func TestCoreStartSurvivesACrashAtEveryBoundary(t *testing.T) {
	probe := newCoreRig(t)
	pq := probe.request()
	pq.NoAdjutant = true
	steps := len(probe.plan(pq).Spec.Steps)
	for ordinal := range steps {
		for _, point := range []string{"intended", "applied"} {
			r := newCoreRig(t)
			r.w.OpenApp()
			q := r.request()
			q.NoAdjutant = true
			p := r.plan(q)
			r.attestShell(p.Key)
			r.crashOn = "step:" + itoa(ordinal) + ":" + point
			first, closeFirst := r.engine()
			crashDriveIfReached(t, first, p.Spec)
			closeFirst()
			r.crashOn = ""
			second, closeSecond := r.engine()
			if err := second.Recover(context.Background()); err != nil {
				t.Fatalf("step %d %s: %v", ordinal, point, err)
			}
			all, err := second.List()
			if err != nil {
				t.Fatal(err)
			}
			op := all[len(all)-1]
			for range 4 {
				if op.State != ops.OpStopped {
					break
				}
				if err := route.Continue(second, r.callers, p, op, nil); err != nil {
					t.Fatal(err)
				}
				if op, err = second.Drive(context.Background(), op.ID); err != nil {
					t.Fatal(err)
				}
			}
			closeSecond()
			execs, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
			if live := len(r.w.Core.Live()); live > 1 || len(execs) > 1 {
				t.Fatalf("step %d %s: %d terminals, %d records", ordinal, point, live, len(execs))
			}
			if op.State != ops.OpDone && op.State != ops.OpStopped {
				t.Errorf("step %d %s: %s %q", ordinal, point, op.State, lastError(op))
			}
		}
	}
}
