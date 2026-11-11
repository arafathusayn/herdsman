package route_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	w        *coretest.World
	services *memServices
	channel  *memChannel
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
	if r.services == nil {
		r.services = &memServices{running: map[string]route.ServiceSpec{}}
	}
	if r.channel == nil {
		r.channel = &memChannel{parties: map[string]string{}, held: map[string]bool{}}
	}
	return r.rig.engineDeps(route.Deps{Callers: r.callers, PollEvery: time.Millisecond, AgentTimeout: 5 * time.Second, Runtime: reg, Services: r.services, CoreChannel: r.channel, Recovery: r.recovery, Down: r.down, Channels: r.channels})
}

// memChannel is the daemon's channel of a core route as a map: who is
// enrolled under which name, and whether the pair is approved.
type memChannel struct {
	mu        sync.Mutex
	opened    bool
	parties   map[string]string // party name -> execution id
	approved  map[string]string
	held      map[string]bool
	handshake int
}

func (m *memChannel) Open(_ context.Context, q route.ChannelOpen) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.opened = true
	// The daemon's channel makes the route's inbox, as Open does there.
	return os.MkdirAll(q.Inbox, 0o700)
}

func (m *memChannel) Enroll(_ context.Context, _, party, _ string, e runtime.Execution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parties[party] = e.ID
	return nil
}

func (m *memChannel) Approve(_ context.Context, _ string, pair []runtime.Execution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(pair) != len(m.parties) {
		return errors.New("the pending pair is not the one up attested")
	}
	for _, e := range pair {
		if m.parties[e.Agent] != e.ID {
			return errors.New("the pending pair is not the one up attested")
		}
	}
	m.approved = maps.Clone(m.parties)
	return nil
}

func (m *memChannel) Approved(string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.approved != nil && maps.Equal(m.approved, m.parties)
}

func (m *memChannel) Handshake(context.Context, string, string, string, string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handshake++
	return nil
}

func (m *memChannel) Hold(_ context.Context, _, party string, held bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held[party] = held
	return nil
}

func (m *memChannel) Held(_, party string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held[party]
}

// memServices is the daemon's service owner as a map: one entry per service.
type memServices struct {
	mu      sync.Mutex
	running map[string]route.ServiceSpec
	starts  []string
	stops   []string
}

func (m *memServices) Start(_ context.Context, s route.ServiceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.running[s.Key()]; !ok {
		m.starts = append(m.starts, s.Name)
	}
	m.running[s.Key()] = s
	return nil
}

func (m *memServices) Stop(_ context.Context, repo, name, service string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := route.ServiceSpec{Repo: repo, Route: name, Name: service}.Key()
	if _, ok := m.running[key]; ok {
		m.stops = append(m.stops, service)
	}
	delete(m.running, key)
	return nil
}

func (m *memServices) Running(repo, name, service string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[route.ServiceSpec{Repo: repo, Route: name, Name: service}.Key()]
	return ok
}

func (m *memServices) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.running {
		out = append(out, s.Name)
	}
	slices.Sort(out)
	return out
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
	if strings.Contains(p.Text, "pane") && !strings.Contains(p.Text, "needs a person") {
		t.Errorf("a core plan names a pane:\n%s", p.Text)
	}
	// up names, before the route starts, each step that will need a person.
	if !strings.Contains(p.Text, "needs a person (claude, unattended):") || !strings.Contains(p.Text, "start_line: once, before the first brief") ||
		!strings.Contains(p.Text, "compaction_due: only if the harness's automatic compaction is turned off") {
		t.Errorf("the plan does not name the steps that need a person:\n%s", p.Text)
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
	// The start line is due now, and the stream says so with the line.
	if evs := r.w.EventsOf(notice.EventNeedsAttention); len(evs) == 0 || evs[len(evs)-1].Reason != core.AttentionStartLine || !strings.Contains(evs[len(evs)-1].Line, "herdsman notice wait") {
		t.Errorf("no start_line attention on the stream: %+v", evs)
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
	// Pair channel: the route's channel is the daemon's, each coordinator
	// enrolled by its execution, the attested pair approved, and the
	// handshake seeded once.
	if !r.channel.opened || len(r.channel.parties) != 2 || !r.channel.Approved("harbor") || r.channel.handshake != 1 {
		t.Errorf("channel: %+v", r.channel)
	}
	// The coordinators find their channel by route and party name.
	all, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
	for _, x := range all {
		env := strings.Join(r.w.ExecEnv(x.LaunchIntent), "\n")
		if !strings.Contains(env, "HERDSMAN_INBOX=") || !strings.Contains(env, "HERDSMAN_NAME="+x.Agent) || strings.Contains(env, "HERDSMAN_ROUTE=") {
			t.Errorf("%s's environment names no party:\n%s", x.Agent, env)
		}
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
	// Services: each registered once with the daemon, no pane copy; no
	// courier without a channel.
	if got := r.services.names(); !slices.Equal(got, []string{route.ServiceCardWatch, route.ServiceCheckpoint}) {
		t.Fatalf("services after the start: %v", got)
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
	// The pause quiets the checkpointer; the card watch stays.
	if got := r.services.names(); !slices.Equal(got, []string{route.ServiceCardWatch}) {
		t.Fatalf("services after the pause: %v", got)
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
	// The stop ends every owned service of the route by its record.
	if got := r.services.names(); len(got) != 0 {
		t.Fatalf("services after the stop: %v", got)
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

// Teardown, delete on core: a delete stops through the provider, then
// archives; it never closes core's spaces or tabs and never types.
func TestCoreDeleteStopsThroughTheProviderThenArchives(t *testing.T) {
	r := newCoreRig(t)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", r.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	r.recovery = &pipeline{repo: r.repo}
	r.down = func(context.Context, string, string) error { r.downs++; return nil }
	reg := &memRegistry{routes: map[string]string{}}
	r.channels = reg
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	p := r.plan(q)
	if p.Inbox != "" {
		reg.routes["harbor"] = p.Inbox
	}
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
	del := func(gen int, force bool) (*route.ControlPlan, *ops.Operation) {
		t.Helper()
		plan, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbDelete, Start: start, Generation: gen, Force: force, Date: "2026-11-11"})
		if err != nil {
			t.Fatal(err)
		}
		return plan, drive(t, e, plan.Spec)
	}
	// A running agent is never deleted without --force.
	if _, got := del(1, false); got.State == ops.OpDone {
		t.Fatal("a running route was deleted without --force")
	}
	if _, err := os.Stat(p.Folder); err != nil {
		t.Fatalf("a refused delete removed the folder: %v", err)
	}
	plan, got := del(2, true)
	if got.State != ops.OpDone {
		t.Fatalf("delete --force: %s %q", got.State, lastError(got))
	}
	if strings.Contains(plan.Text, "close the workspace") {
		t.Errorf("a core delete closes a workspace:\n%s", plan.Text)
	}
	execs, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
	for _, x := range execs {
		if x.Desired != runtime.DesiredStopped {
			t.Errorf("an execution survived the delete: %+v", x)
		}
	}
	if _, err := os.Stat(p.Folder); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the route folder survived: %v", err)
	}
	if _, err := os.Stat(plan.Snapshot); err != nil {
		t.Errorf("no snapshot: %v", err)
	}
	if got := r.services.names(); len(got) != 0 {
		t.Errorf("services after the delete: %v", got)
	}
	// The route's daemon channel goes with its inbox.
	if p.Inbox != "" && (reg.Registered("harbor", p.Inbox) || !strings.Contains(plan.Text, "forget the route's daemon channel")) {
		t.Errorf("the route's channel outlived the delete: %s", plan.Text)
	}
	for _, m := range []string{"terminal.send", "terminal.keys", "space.close", "tab.close", "workspace.close"} {
		if n := r.w.Core.Calls(m); n != 0 {
			t.Errorf("the delete called %s %d times", m, n)
		}
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
