package route_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/route/routechan"
	"github.com/arafathusayn/herdsman/internal/sys"
)

type crash struct{}

// rig is one fake machine: a repository, a private home, a fake herdr and a
// journal that outlives the engines a test makes to stand for restarts.
type rig struct {
	t       *testing.T
	repo    string
	private string
	db      string
	srv     *herdrtest.Server
	fix     *herdrtest.Fixture
	world   *herdrtest.World
	callers *route.Callers
	crashOn string
	poll    func()
	exe     string
	policy  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, repo: filepath.Join(root, "proj"), private: filepath.Join(root, "private"), db: filepath.Join(root, "private", "ops.db"), callers: route.NewCallers()}
	for _, dir := range []string{filepath.Join(r.repo, ".git", "info")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(r.private, 0o700); err != nil {
		t.Fatal(err)
	}
	r.policy = filepath.Join(root, "policy.toml")
	if err := os.WriteFile(r.policy, []byte("sandbox = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r.exe, err = os.Executable(); err != nil {
		t.Fatal(err)
	}
	if r.exe, err = filepath.EvalSymlinks(r.exe); err != nil {
		t.Fatal(err)
	}
	r.srv = herdrtest.Start(t)
	r.fix = herdrtest.Serve(r.srv)
	r.world = herdrtest.ServeWorld(r.fix)
	return r
}

func (r *rig) request(adjutant, orchestrator string) route.UpRequest {
	return route.UpRequest{
		Route: "harbor", Repo: r.repo, Prompt: "fix the login redirect", AdjutantCommand: adjutant, OrchestratorCommand: orchestrator,
		HerdrSocket: r.srv.Socket, PrivateHome: r.private, Executable: r.exe, Date: "2026-11-09", PolicyFiles: []string{r.policy},
		Kinds: []string{"claude", "codex"},
	}
}

func (r *rig) engine() (*ops.Engine, func()) {
	r.t.Helper()
	db, err := store.Open(r.db, store.Writer, store.KindAccount)
	if err != nil {
		r.t.Fatal(err)
	}
	deps := route.Deps{Channel: routechan.Run, Callers: r.callers, PollEvery: time.Millisecond, Poll: func() {
		if r.poll != nil {
			r.poll()
		}
	}, AgentTimeout: 5 * time.Second}
	e := ops.NewEngine(db, ops.Options{Effects: route.Effects(deps), Fault: func(point string) {
		if point == r.crashOn {
			r.crashOn = ""
			panic(crash{})
		}
	}})
	return e, func() { db.Close() }
}

// attest makes the caller a person's shell in an attested pane.
func (r *rig) attest(key string) route.Caller {
	pane := "human-pane"
	r.world.SetProcess(pane, 100, os.Getpid())
	c := route.Caller{Pane: pane, PID: os.Getpid(), Started: ownStart(r.t), TTY: "/dev/ttys-fake"}
	r.callers.Set(key, c)
	return c
}

func ownStart(t *testing.T) time.Time {
	t.Helper()
	p, err := sys.Identify(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return p.Start
}

func (r *rig) plan(req route.UpRequest) *route.UpPlan {
	r.t.Helper()
	p, err := route.PlanUp(req)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

func drive(t *testing.T, e *ops.Engine, s ops.Spec) *ops.Operation {
	t.Helper()
	op, err := e.Submit(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	op, err = e.Drive(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	return op
}

// crashDrive runs the operation until the armed fault, as a killed daemon.
func crashDrive(t *testing.T, e *ops.Engine, s ops.Spec) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(crash); !ok {
				panic(v)
			}
		}
	}()
	drive(t, e, s)
	t.Fatal("the fault never fired")
}

func stepByKey(t *testing.T, op *ops.Operation, key string) ops.Step {
	t.Helper()
	for _, s := range op.Steps {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no step %q in %+v", key, op.Steps)
	return ops.Step{}
}

func TestUpBringsTheWholeRouteUp(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude --model opus", "codex"))
	r.attest(p.Key)
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if !strings.HasPrefix(filepath.Base(p.Folder), "harbor-2026-11-09") {
		t.Fatalf("folder %s", p.Folder)
	}
	for _, name := range []string{"00-route.md", "run-check.sh", "ref", "probes"} {
		if _, err := os.Stat(filepath.Join(p.Folder, name)); err != nil {
			t.Fatal(err)
		}
	}
	exclude, _ := os.ReadFile(filepath.Join(r.repo, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), ".herdsman") {
		t.Fatalf("exclude: %q", exclude)
	}
	st, err := os.Stat(p.Inbox)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("inbox %v %v", st, err)
	}
	ch, err := inbox.OpenChannel(inbox.Options{Dir: p.Inbox, Preflight: filepath.Join(p.Inbox, "setup.json"), ReadOnly: true}, r.exe)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.BinaryProofValid() {
		t.Fatal("the setup proof does not hold")
	}
	pending, _ := os.ReadFile(filepath.Join(p.Inbox, "peers"))
	approved, _ := os.ReadFile(filepath.Join(p.Inbox, "peers.approved"))
	if len(pending) < 10 || string(pending) != string(approved) {
		t.Fatalf("pair not approved: %q / %q", pending, approved)
	}
	record, err := os.ReadFile(filepath.Join(p.Inbox, "pair-approval.json"))
	if err != nil || !strings.Contains(string(record), op.ID) || !strings.Contains(string(record), "herdsman up") {
		t.Fatalf("approval record %q %v", record, err)
	}
	for _, name := range []string{p.AdjutantName, p.OrchestratorName} {
		if n := r.world.Starts(name); n != 1 {
			t.Fatalf("%s started %d times", name, n)
		}
	}
	for _, label := range []string{"courier", "card-watch", "checkpointer"} {
		pane := r.world.PaneByLabel(label)
		if pane == "" || len(r.world.Inputs(pane)) == 0 {
			t.Fatalf("service pane %s: %q", label, pane)
		}
	}
	var sawBrief bool
	for _, q := range r.srv.Requests() {
		if q.Method == "agent.prompt" && q.Params["text"] == "fix the login redirect" {
			sawBrief = true
		}
	}
	if !sawBrief {
		t.Fatal("the prompt never reached the adjutant")
	}
}

func TestPlanHasNoSideEffect(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude doctor && claude", "codex"))
	if len(r.srv.Requests()) != 0 {
		t.Fatalf("a plan called herdr: %v", r.srv.Requests())
	}
	if _, err := os.Stat(p.Folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a plan made the folder: %v", err)
	}
	for _, want := range []string{"claude doctor", p.Folder, p.Inbox, "orchestrator"} {
		if !strings.Contains(p.Text, want) {
			t.Fatalf("plan text misses %q:\n%s", want, p.Text)
		}
	}
}

func TestPlanRefusesAmbiguousKinds(t *testing.T) {
	r := newRig(t)
	if _, err := route.PlanUp(r.request("unknown-harness", "codex")); err == nil || !strings.Contains(err.Error(), "--adjutant-kind") {
		t.Fatalf("no kind: %v", err)
	}
	q := r.request("wrapper-tool", "codex")
	q.AdjutantKind = "claude"
	if _, err := route.PlanUp(q); err != nil {
		t.Fatal(err)
	}
	if _, err := route.PlanUp(r.request("foo; rm -rf x", "codex")); err == nil {
		t.Fatal("a refused line was planned")
	}
}

func TestRetryAfterACrashNeverStartsASecondAgent(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude", "codex"))
	r.attest(p.Key)
	ordinal := -1
	for i, s := range p.Spec.Steps {
		if s.Key == "agent:"+p.OrchestratorName {
			ordinal = i
		}
	}
	r.crashOn = fmt.Sprintf("step:%d:applied", ordinal)
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	if n := r.world.Starts(p.OrchestratorName); n != 1 {
		t.Fatalf("before the restart: %d", n)
	}
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, err := e.Submit(context.Background(), p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if n := r.world.Starts(p.OrchestratorName); n != 1 {
		t.Fatalf("agent started %d times", n)
	}
}

// preludeRig runs up until its first prelude step has had its effect, then
// kills the engine with the pane gone and no status written.
func preludeRig(t *testing.T, line string, withProbe bool) (*rig, *route.UpPlan, string) {
	r := newRig(t)
	req := r.request(line, "codex")
	req.NoAdjutant = false
	p := r.plan(req)
	if withProbe {
		p = r.withProbe(req, "test -e "+filepath.Join(r.repo, "effect"))
	}
	r.attest(p.Key)
	effect := filepath.Join(r.repo, "effect")
	r.world.OnInput(func(pane, text string) bool {
		if !strings.Contains(text, "herdsman-step") || strings.Contains(text, ".probe") {
			return true
		}
		if err := os.WriteFile(effect, []byte("x"), 0o600); err != nil {
			t.Error(err)
		}
		stepsDir := filepath.Join(p.Folder, ".steps", route.StepsOperation)
		if err := os.WriteFile(filepath.Join(stepsDir, "1.start"), nil, 0o600); err != nil {
			t.Error(err)
		}
		r.world.Destroy(pane)
		r.poll = func() { r.poll = nil; panic(crash{}) }
		return false
	})
	return r, p, effect
}

func (r *rig) withProbe(req route.UpRequest, probe string) *route.UpPlan {
	r.t.Helper()
	req.AdjutantProbe = probe
	return r.plan(req)
}

func TestUncertainPreludeWithPaneGoneIsNeitherRerunNorMarkedDone(t *testing.T) {
	r, p, _ := preludeRig(t, "claude doctor && claude", false)
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	r.world.OnInput(func(_, text string) bool {
		if strings.Contains(text, "herdsman-step") {
			t.Errorf("a step ran on resume: %s", text)
		}
		return true
	})
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, err := e.Submit(context.Background(), p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpStopped {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	st := stepByKey(t, op, "prelude:1")
	if st.State != ops.StepIntended {
		t.Fatalf("step is %s", st.State)
	}
	if r.world.Starts(p.AdjutantName) != 0 {
		t.Fatal("the adjutant started past an unsettled step")
	}
	if !strings.Contains(op.Reason, "herdsman up") {
		t.Fatalf("no resume line: %s", op.Reason)
	}
	// A person says the effect happened: the run goes on without repeating it.
	r.world.OnInput(nil)
	if err := route.Continue(e, r.callers, p, op, map[int]string{1: "done"}); err != nil {
		t.Fatal(err)
	}
	op, err = e.Drive(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("after the verdict: %s %s", op.State, op.Reason)
	}
	if n := r.world.Starts(p.AdjutantName); n != 1 {
		t.Fatalf("adjutant started %d times", n)
	}
}

func TestRerunVerdictRunsTheStepOnceMore(t *testing.T) {
	r, p, effect := preludeRig(t, "claude doctor && claude", false)
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Submit(context.Background(), p.Spec)
	if err := os.Remove(effect); err != nil {
		t.Fatal(err)
	}
	var runs int
	r.world.OnInput(func(_, text string) bool {
		if strings.Contains(text, "herdsman-step") {
			runs++
			stepsDir := filepath.Join(p.Folder, ".steps", route.StepsOperation)
			_ = os.WriteFile(filepath.Join(stepsDir, "1.start"), nil, 0o600)
			_ = os.WriteFile(filepath.Join(stepsDir, "1.rc"), []byte("0"), 0o600)
			return false
		}
		return true
	})
	if err := route.Continue(e, r.callers, p, op, map[int]string{1: "rerun"}); err != nil {
		t.Fatal(err)
	}
	op, err := e.Drive(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone || runs != 1 {
		t.Fatalf("state %s reason %s runs %d", op.State, op.Reason, runs)
	}
}

func TestProbeSettlesAnUncertainStep(t *testing.T) {
	r, p, _ := preludeRig(t, "claude doctor && claude", true)
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	r.world.OnInput(func(_, text string) bool {
		if strings.Contains(text, "herdsman-step") && !strings.Contains(text, ".probe") {
			t.Errorf("the step itself ran: %s", text)
		}
		return true
	})
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Submit(context.Background(), p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
}

func TestFailedPreludeStopsWithItsStatus(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("false && claude", "codex"))
	r.attest(p.Key)
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpConflict {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if !strings.Contains(stepByKey(t, op, "prelude:1").Detail, "exited 1") {
		t.Fatalf("detail %+v", stepByKey(t, op, "prelude:1"))
	}
	if r.world.Starts(p.AdjutantName) != 0 {
		t.Fatal("the agent started after a failed step")
	}
}

func TestOnceStepRefusesARerun(t *testing.T) {
	r := newRig(t)
	req := r.request("claude doctor && claude", "codex")
	req.AdjutantOnce = true
	p := r.plan(req)
	e, done := r.engine()
	defer done()
	op, err := e.Submit(context.Background(), p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	err = route.Continue(e, r.callers, p, op, map[int]string{1: "rerun"})
	if err == nil || !strings.Contains(err.Error(), "once") {
		t.Fatalf("got %v", err)
	}
}

func TestPairIsLeftPendingWithoutAttestation(t *testing.T) {
	cases := []struct {
		name  string
		setup func(r *rig, key string)
		want  string
	}{
		{"agent attached", func(r *rig, key string) {
			c := r.attest(key)
			r.fix.SetAgent(c.Pane, herdrtest.Agent(c.Pane, "s"))
		}, "agent"},
		{"pid not in pane", func(r *rig, key string) {
			c := r.attest(key)
			r.world.SetProcess(c.Pane, 100, os.Getpid()+1)
		}, "process"},
		{"harness ancestor", func(r *rig, key string) {
			c := r.attest(key)
			c.HarnessAncestor = "claude"
			r.callers.Set(key, c)
		}, "harness"},
		{"no terminal and no pane", func(r *rig, key string) {
			r.callers.Set(key, route.Caller{PID: os.Getpid(), Started: ownStart(r.t)})
		}, "terminal"},
		{"unknown pane", func(r *rig, key string) {
			r.callers.Set(key, route.Caller{Pane: "gone", PID: os.Getpid(), Started: ownStart(r.t), TTY: "/dev/ttys1"})
		}, "attest"},
		{"forged process", func(r *rig, key string) {
			r.callers.Set(key, route.Caller{PID: -1, Started: time.Unix(1, 0), TTY: "forged-terminal"})
		}, "process"},
		{"no verified start", func(r *rig, key string) {
			r.callers.Set(key, route.Caller{PID: os.Getpid(), TTY: "/dev/ttys1"})
		}, "process"},
		{"a shell in an agent's pane without saying so", func(r *rig, key string) {
			pane := r.world.AddPane("elsewhere", "/dev/ttys-agent")
			r.fix.SetAgent(pane, herdrtest.Agent(pane, "s"))
			r.callers.Set(key, route.Caller{PID: os.Getpid(), Started: ownStart(r.t), TTY: "/dev/ttys-agent"})
		}, "agent"},
		{"nobody", func(*rig, string) {}, "caller"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			p := r.plan(r.request("claude", "codex"))
			tc.setup(r, p.Key)
			e, done := r.engine()
			defer done()
			op := drive(t, e, p.Spec)
			if op.State != ops.OpStopped {
				t.Fatalf("state %s: %s", op.State, op.Reason)
			}
			if !strings.Contains(op.Reason, tc.want) || !strings.Contains(op.Reason, "register approve") {
				t.Fatalf("reason %q wants %q and the approve line", op.Reason, tc.want)
			}
			approved, _ := os.ReadFile(filepath.Join(p.Inbox, "peers.approved"))
			if strings.Contains(string(approved), p.AdjutantName) {
				t.Fatalf("approved anyway: %s", approved)
			}
		})
	}
}

func TestContinueAfterAFreshAttestationApproves(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude", "codex"))
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpStopped {
		t.Fatalf("state %s", op.State)
	}
	r.attest(p.Key)
	if err := route.Continue(e, r.callers, p, op, nil); err != nil {
		t.Fatal(err)
	}
	op, err := e.Drive(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if n := r.world.Starts(p.AdjutantName); n != 1 {
		t.Fatalf("started %d", n)
	}
}

func TestManualPairNeedsTheConfirmedChecksum(t *testing.T) {
	r := newRig(t)
	req := r.request("claude", "codex")
	req.PairManual = true
	p := r.plan(req)
	r.attest(p.Key)
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpStopped || !strings.Contains(op.Reason, "confirm") {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	sum, _, err := route.PairState(p.Inbox)
	if err != nil || sum == "" {
		t.Fatalf("%q %v", sum, err)
	}
	c, _ := r.callers.Get(p.Key)
	c.Confirmed = "cksum-of-something-else"
	r.callers.Set(p.Key, c)
	if err := route.Continue(e, r.callers, p, op, nil); err != nil {
		t.Fatal(err)
	}
	if op, _ = e.Drive(context.Background(), op.ID); op.State != ops.OpStopped {
		t.Fatalf("wrong checksum: %s", op.State)
	}
	c.Confirmed = sum
	r.callers.Set(p.Key, c)
	if err := route.Continue(e, r.callers, p, op, nil); err != nil {
		t.Fatal(err)
	}
	if op, _ = e.Drive(context.Background(), op.ID); op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
}

func TestNoAdjutantDropsTheChannel(t *testing.T) {
	r := newRig(t)
	req := r.request("", "codex")
	req.NoAdjutant = true
	p := r.plan(req)
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if _, err := os.Stat(p.Inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an inbox was made: %v", err)
	}
	for _, s := range op.Steps {
		for _, bad := range []string{"propose", "approve", "courier", "handshake"} {
			if strings.Contains(s.Key, bad) {
				t.Fatalf("channel step %s", s.Key)
			}
		}
	}
	if n := r.world.Starts(p.OrchestratorName); n != 1 {
		t.Fatal(strconv.Itoa(n))
	}
}

func TestSecondUpOnTheSameRouteStartsNothing(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude", "codex"))
	r.attest(p.Key)
	e, done := r.engine()
	defer done()
	first := drive(t, e, p.Spec)
	again := drive(t, e, p.Spec)
	if first.ID != again.ID {
		t.Fatalf("two operations: %s %s", first.ID, again.ID)
	}
	if n := r.world.Starts(p.AdjutantName); n != 1 {
		t.Fatalf("started %d", n)
	}
}

// TestACrashAfterAnyStepsEffectNeverRepeatsIt kills the run right after each
// step's effect, before the journal records it, and finishes it on restart.
func TestACrashAfterAnyStepsEffectNeverRepeatsIt(t *testing.T) {
	steps := len(newRig(t).plan(newRig(t).request("claude doctor && claude", "codex")).Spec.Steps)
	for ordinal := range steps {
		t.Run(strconv.Itoa(ordinal), func(t *testing.T) {
			r := newRig(t)
			p := r.plan(r.request("claude doctor && claude", "codex"))
			r.attest(p.Key)
			var updates int
			r.world.OnInput(func(_, text string) bool {
				if strings.Contains(text, "'doctor'") {
					updates++
				}
				return true
			})
			r.crashOn = fmt.Sprintf("step:%d:applied", ordinal)
			e, done := r.engine()
			crashDrive(t, e, p.Spec)
			done()
			e, done = r.engine()
			defer done()
			if err := e.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			op, err := e.Submit(context.Background(), p.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if op.State != ops.OpDone {
				t.Fatalf("state %s: %s", op.State, op.Reason)
			}
			if updates != 1 {
				t.Fatalf("the prelude step ran %d times", updates)
			}
			for _, name := range []string{p.AdjutantName, p.OrchestratorName} {
				if n := r.world.Starts(name); n != 1 {
					t.Fatalf("%s started %d times", name, n)
				}
			}
			facts := route.Describe(op)
			if facts.Folder != p.Folder || facts.Inbox != p.Inbox || facts.Workspace != p.Workspace || route.DateOf(op) != "2026-11-09" {
				t.Fatalf("facts %+v, date %s", facts, route.DateOf(op))
			}
		})
	}
}

func TestManualConfirmationStillNeedsAVerifiedCaller(t *testing.T) {
	r := newRig(t)
	req := r.request("claude", "codex")
	req.PairManual = true
	p := r.plan(req)
	e, done := r.engine()
	defer done()
	r.callers.Set(p.Key, route.Caller{PID: -1, TTY: "forged"})
	op := drive(t, e, p.Spec)
	sum, _, err := route.PairState(p.Inbox)
	if err != nil || sum == "" {
		t.Fatalf("%q %v", sum, err)
	}
	r.callers.Set(p.Key, route.Caller{PID: -1, TTY: "forged", Confirmed: sum})
	if err := route.Continue(e, r.callers, p, op, nil); err != nil {
		t.Fatal(err)
	}
	op, _ = e.Drive(context.Background(), op.ID)
	if op.State != ops.OpStopped {
		t.Fatalf("a forged caller confirmed the pair: %s", op.State)
	}
}

func TestABriefThatLooksLikeASkillCommandIsStillSent(t *testing.T) {
	for _, prompt := range []string{"/audit this repository", "$100 budget"} {
		t.Run(prompt, func(t *testing.T) {
			r := newRig(t)
			req := r.request("claude", "codex")
			req.Prompt = prompt
			p := r.plan(req)
			r.attest(p.Key)
			e, done := r.engine()
			defer done()
			if op := drive(t, e, p.Spec); op.State != ops.OpDone {
				t.Fatalf("state %s: %s", op.State, op.Reason)
			}
			var sent bool
			for _, q := range r.srv.Requests() {
				sent = sent || (q.Method == "agent.prompt" && q.Params["text"] == prompt)
			}
			if !sent {
				t.Fatal("the brief was never sent")
			}
		})
	}
}

func TestPlanOfAJournaledStartResumesWithoutTheOriginalFlags(t *testing.T) {
	r, p, _ := preludeRig(t, "claude doctor && claude", false)
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	r.world.OnInput(nil)
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Submit(context.Background(), p.Spec)
	restored, err := route.PlanOf(op)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Key != p.Key || restored.Folder != p.Folder || len(restored.Prelude) != 1 || restored.Spec.Plan != p.Spec.Plan {
		t.Fatalf("restored %+v", restored)
	}
	if err := route.Continue(e, r.callers, restored, op, map[int]string{1: "done"}); err != nil {
		t.Fatal(err)
	}
	if op, _ = e.Drive(context.Background(), op.ID); op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
}
