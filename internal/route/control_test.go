package route_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// upped starts the whole route and returns its journaled start.
func upped(t *testing.T, r *rig) (*route.UpPlan, *ops.Operation, *ops.Engine, func()) {
	t.Helper()
	p := r.plan(r.request("claude --model opus", "codex"))
	r.attest(p.Key)
	e, done := r.engine()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("up: %s: %s", op.State, op.Reason)
	}
	return p, op, e, done
}

func control(t *testing.T, start *ops.Operation, verb route.Verb, gen int, edit ...func(*route.ControlRequest)) *route.ControlPlan {
	t.Helper()
	q := route.ControlRequest{Verb: verb, Start: start, Generation: gen, Date: "2026-11-09"}
	for _, f := range edit {
		f(&q)
	}
	c, err := route.PlanControl(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) keysSent(key string) []string {
	var panes []string
	for _, q := range r.srv.Requests() {
		if q.Method != "pane.send_input" {
			continue
		}
		keys, _ := q.Params["keys"].([]any)
		if slices.Contains(keys, any(key)) {
			panes = append(panes, q.Params["pane_id"].(string))
		}
	}
	return panes
}

func TestPauseInterruptsHoldsAndClosesNothing(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	defer done()
	r.srv.ForgetRequests()
	op := drive(t, e, control(t, start, route.VerbPause, 1).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	esc := r.keysSent("esc")
	for _, label := range []string{"adjutant", "orchestrator"} {
		if !slices.Contains(esc, r.world.PaneByLabel(label)) {
			t.Fatalf("%s was not interrupted: %v", label, esc)
		}
	}
	if got := r.keysSent("ctrl+c"); !slices.Equal(got, []string{r.world.PaneByLabel("checkpointer")}) {
		t.Fatalf("checkpointer stop: %v", got)
	}
	for _, name := range []string{p.AdjutantName, p.OrchestratorName} {
		if _, err := os.Stat(filepath.Join(p.Inbox, "state", "hold-"+name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range []string{"adjutant", "orchestrator", "courier", "card-watch", "checkpointer"} {
		if r.world.PaneByLabel(label) == "" {
			t.Fatalf("pause closed the %s pane", label)
		}
	}
	state, err := route.ReadControlRecord(p.Folder)
	if err != nil || state.Verb != route.VerbPause || len(state.Agents) != 2 || state.Agents[0].Pane == "" {
		t.Fatalf("%+v %v", state, err)
	}
	log, _ := os.ReadFile(filepath.Join(p.Folder, "00-route.md"))
	if !strings.Contains(string(log), "paused") {
		t.Fatalf("no run-state line:\n%s", log)
	}
}

func TestPauseIsBestEffortWhenAnAgentIsGone(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	defer done()
	r.fix.SetAgent(r.world.PaneByLabel("orchestrator"), nil)
	op := drive(t, e, control(t, start, route.VerbPause, 1).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	state, _ := route.ReadControlRecord(p.Folder)
	var missed bool
	for _, a := range state.Agents {
		if a.Name == p.OrchestratorName && a.Status == route.StatusNotReached {
			missed = true
		}
	}
	if !missed {
		t.Fatalf("the unreachable agent is not named: %+v", state)
	}
}

func TestACrashedPauseFinishesAndHoldsOnce(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	done()
	_ = e
	spec := control(t, start, route.VerbPause, 1).Spec
	for ordinal := range spec.Steps {
		r.crashOn = "step:" + itoa(ordinal) + ":applied"
		spec := control(t, start, route.VerbPause, ordinal+1).Spec
		first, closeFirst := r.engine()
		crashDrive(t, first, spec)
		closeFirst()
		second, closeSecond := r.engine()
		if err := second.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
		ops2, err := second.List()
		if err != nil {
			t.Fatal(err)
		}
		last := ops2[len(ops2)-1]
		closeSecond()
		if last.State != ops.OpDone {
			t.Fatalf("after a crash at step %d: %s: %s", ordinal, last.State, last.Reason)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Inbox, "state", "hold-"+p.OrchestratorName)); err != nil {
		t.Fatal(err)
	}
}

func TestResumeOfAPausedRouteLiftsTheHoldAndContinues(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	defer done()
	drive(t, e, control(t, start, route.VerbPause, 1).Spec)
	checkpointer := r.world.PaneByLabel("checkpointer")
	before := len(r.world.Inputs(checkpointer))
	r.srv.ForgetRequests()
	op := drive(t, e, control(t, start, route.VerbResume, 2, func(q *route.ControlRequest) { q.Note = "the disk is free again"; q.From = route.ControlPaused }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if _, err := os.Stat(filepath.Join(p.Inbox, "state", "hold-"+p.AdjutantName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hold still there: %v", err)
	}
	if len(r.world.Inputs(checkpointer)) != before+1 {
		t.Fatal("the checkpointer was not started again")
	}
	var prompted []string
	for _, q := range r.srv.Requests() {
		text, _ := q.Params["text"].(string)
		if q.Method == "agent.prompt" && strings.Contains(text, "Continue from where you stopped") && strings.Contains(text, "the disk is free again") {
			prompted = append(prompted, q.Params["target"].(string))
		}
	}
	if len(prompted) != 2 {
		t.Fatalf("continue prompts: %v", prompted)
	}
	if n := r.world.Starts(p.AdjutantName); n != 1 {
		t.Fatalf("a pause's resume restarted the agent: %d", n)
	}
}

func TestStopEndsSessionsKeepsWorkAndLeavesThePairPending(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	defer done()
	op := drive(t, e, control(t, start, route.VerbStop, 1).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	for _, label := range []string{"adjutant", "orchestrator", "courier", "card-watch", "checkpointer"} {
		if pane := r.world.PaneByLabel(label); pane != "" {
			t.Fatalf("the %s pane is still open: %s", label, pane)
		}
	}
	for _, kept := range []string{p.Folder, p.Inbox} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatal(err)
		}
	}
	sum, approved, err := route.PairState(p.Inbox)
	if err != nil || sum == "" || approved {
		t.Fatalf("the pair: %q %v %v", sum, approved, err)
	}
	if st, _ := route.ReadControlRecord(p.Folder); st.Verb != route.VerbStop {
		t.Fatalf("%+v", st)
	}
}

func TestResumeOfAStoppedRouteRestartsAgentsWithoutThePrelude(t *testing.T) {
	r := newRig(t)
	p := r.plan(r.request("claude --model opus", "codex"))
	r.attest(p.Key)
	e, done := r.engine()
	defer done()
	start := drive(t, e, p.Spec)
	drive(t, e, control(t, start, route.VerbStop, 1).Spec)
	r.attest(p.Key)
	op := drive(t, e, control(t, start, route.VerbResume, 2, func(q *route.ControlRequest) { q.From = route.ControlStopped }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	for _, name := range []string{p.AdjutantName, p.OrchestratorName} {
		if n := r.world.Starts(name); n != 2 {
			t.Fatalf("%s started %d times", name, n)
		}
	}
	if _, approved, _ := route.PairState(p.Inbox); !approved {
		t.Fatal("the pair was not approved again")
	}
	for _, label := range []string{"courier", "card-watch", "checkpointer"} {
		if r.world.PaneByLabel(label) == "" {
			t.Fatalf("%s did not come back", label)
		}
	}
}

// pipeline is the recovery backend the tests use: a real snapshot of the
// repository and the real verifier for what lies inside it. The private inbox
// must stay outside any repository, so a worktree snapshot cannot hold it; the
// pipeline stands in for the scratch archive that covers it. failVerify, when
// set, can refuse a call.
type pipeline struct {
	repo       string
	calls      int
	failVerify func(call int) error
	captured   []string
}

func (p *pipeline) Capture(ctx context.Context, q route.SnapshotRequest) error {
	p.captured = q.Targets
	_, err := snapshot.Take(ctx, snapshot.Options{Worktree: q.Repo, Dest: q.Dest, Task: "route-" + q.Route, Op: q.Operation})
	return err
}

func (p *pipeline) Verify(ctx context.Context, dest string, targets []route.SnapshotTarget) error {
	p.calls++
	if p.failVerify != nil {
		if err := p.failVerify(p.calls); err != nil {
			return err
		}
	}
	var inside []route.SnapshotTarget
	for _, target := range targets {
		if strings.HasPrefix(target.Path, p.repo+string(filepath.Separator)) {
			inside = append(inside, target)
		} else if !slices.Contains(p.captured, target.Path) {
			return errors.New("no scratch archive covers " + target.Path)
		}
	}
	return route.VerifySnapshot(ctx, dest, inside)
}

// recoverable is a rig whose repository is a real one, so that a real snapshot
// captures the route folder.
func recoverable(t *testing.T) (*rig, *pipeline) {
	t.Helper()
	r := newRig(t)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", r.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	p := &pipeline{repo: r.repo}
	r.recovery = p
	r.down = func(context.Context, string, string) error { r.downs++; return nil }
	return r, p
}

func deleted(t *testing.T, r *rig) (*route.UpPlan, *ops.Operation, *ops.Engine, func()) {
	t.Helper()
	p, start, e, done := upped(t, r)
	drive(t, e, control(t, start, route.VerbStop, 1).Spec)
	return p, start, e, done
}

func assertKept(t *testing.T, r *rig, p *route.UpPlan) {
	t.Helper()
	for _, kept := range []string{p.Folder, p.Inbox} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("removed without verification: %v", err)
		}
		if _, err := os.Stat(kept + ".deleting"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("left staged: %v", err)
		}
	}
}

func TestDeleteTakesAVerifiedSnapshotThenRemovesTheRoute(t *testing.T) {
	r, pipe := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	op := drive(t, e, c.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	for _, gone := range []string{p.Folder, p.Inbox, p.Folder + ".deleting", p.Inbox + ".deleting"} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: %v", gone, err)
		}
	}
	if _, err := snapshot.Verify(context.Background(), c.Snapshot); err != nil {
		t.Fatalf("no verified snapshot: %v", err)
	}
	if !slices.Contains(pipe.captured, p.Folder) || !slices.Contains(pipe.captured, p.Inbox) {
		t.Fatalf("captured %v", pipe.captured)
	}
	if r.downs != 1 {
		t.Fatalf("down ran %d times", r.downs)
	}
	if ws := r.world.PaneByLabel("adjutant"); ws != "" {
		t.Fatalf("pane %s", ws)
	}
}

func TestDeleteRemovesNothingWithoutARecoveryBackend(t *testing.T) {
	r := newRig(t)
	p, start, e, done := deleted(t, r)
	defer done()
	op, err := e.Submit(context.Background(), control(t, start, route.VerbDelete, 2).Spec)
	if err != nil {
		t.Fatal(err)
	}
	if op, _ = e.Drive(context.Background(), op.ID); op.State == ops.OpDone {
		t.Fatal("delete finished with no recovery backend")
	}
	assertKept(t, r, p)
}

func TestDeleteRemovesNothingWhenTheSnapshotIsDamaged(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	r.onFault = func(point string) {
		if point == "step:1:done" {
			if err := os.WriteFile(filepath.Join(c.Snapshot, snapshot.BundleName), []byte("damaged"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	op := drive(t, e, c.Spec)
	if op.State == ops.OpDone {
		t.Fatal("the delete went on with a damaged snapshot")
	}
	assertKept(t, r, p)
	if r.world.PaneByLabel("adjutant") != "" {
		t.Fatal("stop should have closed the pane already")
	}
}

func TestDeleteRefusesASnapshotThatOmitsATarget(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	r.onFault = func(point string) {
		if point != "step:1:done" {
			return
		}
		raw, err := os.ReadFile(filepath.Join(c.Snapshot, snapshot.ManifestName))
		if err != nil {
			t.Error(err)
			return
		}
		var m snapshot.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Error(err)
			return
		}
		rel, _ := filepath.Rel(m.Worktree, p.Folder)
		m.Files = slices.DeleteFunc(m.Files, func(f snapshot.FileRecord) bool { return strings.HasPrefix(string(f.Path), rel+"/") })
		raw, _ = json.Marshal(m)
		if err := os.WriteFile(filepath.Join(c.Snapshot, snapshot.ManifestName), raw, 0o600); err != nil {
			t.Error(err)
		}
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("a snapshot without the route folder allowed the delete")
	}
	assertKept(t, r, p)
}

func TestVerifySnapshotBindsEveryTarget(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2, func(q *route.ControlRequest) { q.KeepFolder = true })
	drive(t, e, c.Spec)
	ctx := context.Background()
	if err := route.VerifySnapshot(ctx, c.Snapshot, []route.SnapshotTarget{{Path: p.Folder, Live: p.Folder}}); err != nil {
		t.Fatal(err)
	}
	if err := route.VerifySnapshot(ctx, c.Snapshot, []route.SnapshotTarget{{Path: t.TempDir()}}); err == nil {
		t.Fatal("a target outside the snapshot was accepted")
	}
	if err := route.VerifySnapshot(ctx, c.Snapshot, []route.SnapshotTarget{{Path: filepath.Join(r.repo, "never-captured")}}); err == nil {
		t.Fatal("a target with no inventory was accepted")
	}
	if err := route.VerifySnapshot(ctx, c.Snapshot, nil); err == nil {
		t.Fatal("no targets was accepted")
	}
	changed := filepath.Join(p.Folder, "00-route.md")
	if err := os.WriteFile(changed, []byte("changed since the snapshot, and longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := route.VerifySnapshot(ctx, c.Snapshot, []route.SnapshotTarget{{Path: p.Folder, Live: p.Folder}}); err == nil {
		t.Fatal("a changed file was accepted")
	}
	added := filepath.Join(p.Folder, "written-later")
	if err := os.WriteFile(added, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := route.VerifySnapshot(ctx, c.Snapshot, []route.SnapshotTarget{{Path: p.Folder, Live: p.Folder}}); err == nil {
		t.Fatal("a file the snapshot lacks was accepted")
	}
}

func TestAFailedLastCheckPutsEveryTargetBack(t *testing.T) {
	r, pipe := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	base := pipe.calls
	_ = base
	// The snapshot and down steps verify once each, the removal's first check
	// is next, and its check after staging is the one that fails here.
	pipe.failVerify = func(call int) error {
		if call == base+4 {
			return errors.New("the snapshot went bad")
		}
		return nil
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("the delete ignored its last check")
	}
	assertKept(t, r, p)
}

func TestAnAbsentTargetDoesNotExcuseAFailedVerification(t *testing.T) {
	r, pipe := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	r.onFault = func(point string) {
		if point != "step:1:done" {
			return
		}
		if err := os.RemoveAll(p.Folder); err != nil {
			t.Error(err)
		}
		if err := os.RemoveAll(p.Inbox); err != nil {
			t.Error(err)
		}
		pipe.failVerify = func(int) error { return errors.New("the snapshot went bad") }
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("a failed verification became success because the targets were gone")
	}
}

func TestDeleteRefusesAFolderTheRouteDoesNotOwn(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range c.Spec.Steps {
		c.Spec.Steps[i].Spec = strings.ReplaceAll(c.Spec.Steps[i].Spec, `"targets":["`+p.Folder+`"`, `"targets":["`+other+`"`)
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("removed a folder outside the route")
	}
	if _, err := os.Stat(filepath.Join(other, "precious")); err != nil {
		t.Fatal(err)
	}
}

func TestForcedDeleteStopsAgentsFirst(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := upped(t, r)
	defer done()
	names, err := route.RunningAgents(context.Background(), start)
	if err != nil || len(names) != 2 {
		t.Fatalf("running agents: %v %v", names, err)
	}
	op := drive(t, e, control(t, start, route.VerbDelete, 1, func(q *route.ControlRequest) { q.Force = true }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if pane := r.world.PaneByLabel("orchestrator"); pane != "" {
		t.Fatalf("orchestrator pane %s", pane)
	}
	if _, err := os.Stat(p.Folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestKeepFolderAndKeepWorktrees(t *testing.T) {
	r, _ := recoverable(t)
	p, start, e, done := deleted(t, r)
	defer done()
	op := drive(t, e, control(t, start, route.VerbDelete, 2, func(q *route.ControlRequest) { q.KeepFolder, q.KeepWorktrees = true, true }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if _, err := os.Stat(p.Folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if r.downs != 0 {
		t.Fatalf("down ran %d times", r.downs)
	}
}

func TestDownMustSucceedBeforeAnythingIsRemoved(t *testing.T) {
	r, _ := recoverable(t)
	p, start, _, done := deleted(t, r)
	r.down = func(context.Context, string, string) error { return errors.New("a task still holds unpushed work") }
	done()
	e, done := r.engine()
	defer done()
	if op := drive(t, e, control(t, start, route.VerbDelete, 2).Spec); op.State == ops.OpDone {
		t.Fatal("delete ignored a failed down")
	}
	assertKept(t, r, p)
}

func TestTeardownRunsWhenAsked(t *testing.T) {
	r := newRig(t)
	p, start, _, done := upped(t, r)
	var folders []string
	r.teardown = func(_ context.Context, _, folder string) error { folders = append(folders, folder); return nil }
	done()
	e, done := r.engine()
	defer done()
	op := drive(t, e, control(t, start, route.VerbPause, 1, func(q *route.ControlRequest) { q.Teardown = true }).Spec)
	if op.State != ops.OpDone || !slices.Equal(folders, []string{p.Folder}) {
		t.Fatalf("%s %s %v", op.State, op.Reason, folders)
	}
	op = drive(t, e, control(t, start, route.VerbStop, 2, func(q *route.ControlRequest) { q.Teardown = true }).Spec)
	if op.State != ops.OpDone || len(folders) != 2 {
		t.Fatalf("%s %s %v", op.State, op.Reason, folders)
	}
}

func TestPlainPauseRunsNoTeardown(t *testing.T) {
	r := newRig(t)
	_, start, e, done := upped(t, r)
	defer done()
	c := control(t, start, route.VerbPause, 1)
	for _, st := range c.Spec.Steps {
		if strings.Contains(st.Name, "teardown") {
			t.Fatalf("step %s", st.Name)
		}
	}
	_ = e
}

func TestControlStateIsTheLastFinishedVerb(t *testing.T) {
	mk := func(id, kind string, state ops.OpState) *ops.Operation {
		return &ops.Operation{ID: id, Kind: kind, State: state}
	}
	cases := []struct {
		name string
		ops  []*ops.Operation
		want string
	}{
		{"none", nil, route.ControlRunning},
		{"paused", []*ops.Operation{mk("1", route.KindPause, ops.OpDone)}, route.ControlPaused},
		{"resumed", []*ops.Operation{mk("1", route.KindPause, ops.OpDone), mk("2", route.KindResume, ops.OpDone)}, route.ControlRunning},
		{"a failed stop does not count", []*ops.Operation{mk("1", route.KindPause, ops.OpDone), mk("2", route.KindStop, ops.OpFailed)}, route.ControlPaused},
		{"deleted", []*ops.Operation{mk("1", route.KindDelete, ops.OpDone)}, route.ControlDeleted},
	}
	for _, c := range cases {
		if got := route.ControlState(c.ops); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestACrashAtAnyStepOfAVerbFinishesOnRestart kills the engine after each step's
// effect and before its record, then restarts it.
func TestACrashAtAnyStepOfAVerbFinishesOnRestart(t *testing.T) {
	cases := []struct {
		name        string
		recoverable bool
		attest      bool
		setup       []route.Verb
		verb        route.Verb
		from        string
		check       func(*testing.T, *rig, *route.UpPlan)
	}{
		{name: "pause", verb: route.VerbPause, check: func(t *testing.T, r *rig, p *route.UpPlan) {
			if r.world.PaneByLabel("orchestrator") == "" {
				t.Fatal("pause closed a pane")
			}
		}},
		{name: "resume from stop", setup: []route.Verb{route.VerbStop}, verb: route.VerbResume, from: route.ControlStopped, attest: true, check: func(t *testing.T, r *rig, p *route.UpPlan) {
			if r.world.PaneByLabel("orchestrator") == "" {
				t.Fatal("the orchestrator did not come back")
			}
		}},
		{name: "resume", setup: []route.Verb{route.VerbPause}, verb: route.VerbResume, from: route.ControlPaused, check: func(t *testing.T, r *rig, p *route.UpPlan) {
			if n := r.world.Starts(p.OrchestratorName); n != 1 {
				t.Fatalf("started %d times", n)
			}
		}},
		{name: "stop", verb: route.VerbStop, check: func(t *testing.T, r *rig, p *route.UpPlan) {
			if pane := r.world.PaneByLabel("orchestrator"); pane != "" {
				t.Fatalf("pane %s is open", pane)
			}
		}},
		{name: "delete", recoverable: true, setup: []route.Verb{route.VerbStop}, verb: route.VerbDelete, check: func(t *testing.T, r *rig, p *route.UpPlan) {
			if _, err := os.Stat(p.Folder); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("folder: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mk := func() *rig {
				if c.recoverable {
					r, _ := recoverable(t)
					return r
				}
				return newRig(t)
			}
			probe := mk()
			pp, start, e, done := upped(t, probe)
			gen := 1
			for _, v := range c.setup {
				drive(t, e, control(t, start, v, gen).Spec)
				gen++
			}
			if c.attest {
				probe.attest(pp.Key)
			}
			steps := len(control(t, start, c.verb, gen, func(q *route.ControlRequest) { q.From = c.from }).Spec.Steps)
			done()
			for ordinal := range steps {
				for _, point := range []string{"intended", "applied"} {
					r := mk()
					p, start, e, done := upped(t, r)
					gen := 1
					for _, v := range c.setup {
						drive(t, e, control(t, start, v, gen).Spec)
						gen++
					}
					done()
					if c.attest {
						r.attest(p.Key)
					}
					spec := control(t, start, c.verb, gen, func(q *route.ControlRequest) { q.From = c.from }).Spec
					r.crashOn = "step:" + itoa(ordinal) + ":" + point
					first, closeFirst := r.engine()
					crashDriveIfReached(t, first, spec)
					closeFirst()
					if r.crashOn != "" {
						// The step's Apply failed and its probe found the effect,
						// so this boundary is never reached.
						r.crashOn = ""
						continue
					}
					second, closeSecond := r.engine()
					if err := second.Recover(context.Background()); err != nil {
						t.Fatalf("step %d %s: %v", ordinal, point, err)
					}
					all, err := second.List()
					if err != nil {
						t.Fatal(err)
					}
					last := all[len(all)-1]
					closeSecond()
					if last.State != ops.OpDone {
						// A step that may not be repeated stops for a person; that is the rule.
						if last.Steps[ordinal].Retry == ops.RetryNever && point == "intended" {
							continue
						}
						t.Fatalf("crash at step %d %s (%s): %s: %s", ordinal, point, last.Steps[ordinal].Key, last.State, last.Reason)
					}
					c.check(t, r, p)
				}
			}
		})
	}
}

// crashDriveIfReached is crashDrive for a boundary a run may legally skip.
func crashDriveIfReached(t *testing.T, e *ops.Engine, s ops.Spec) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(crash); !ok {
				panic(v)
			}
		}
	}()
	drive(t, e, s)
}

func TestResumePastAPendingPairFinishesOnAFreshAttestation(t *testing.T) {
	r := newRig(t)
	p, start, e, done := upped(t, r)
	defer done()
	drive(t, e, control(t, start, route.VerbStop, 1).Spec)
	r.callers.Set(p.Key, route.Caller{})
	spec := control(t, start, route.VerbResume, 2, func(q *route.ControlRequest) { q.From = route.ControlStopped }).Spec
	op := drive(t, e, spec)
	if op.State != ops.OpStopped {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if sum, approved, _ := route.PairState(p.Inbox); sum == "" || approved {
		t.Fatalf("the pair should wait: %q %v", sum, approved)
	}
	r.attest(p.Key)
	if err := route.ResolveHalted(e, op); err != nil {
		t.Fatal(err)
	}
	op, err := e.Drive(context.Background(), op.ID)
	if err != nil || op.State != ops.OpDone {
		t.Fatalf("%v %s: %s", err, op.State, op.Reason)
	}
	if _, approved, _ := route.PairState(p.Inbox); !approved {
		t.Fatal("the pair was not approved")
	}
	if err := route.ResolveHalted(e, op); err != nil {
		t.Fatalf("a finished operation: %v", err)
	}
}

func TestStopInterruptsAnAgentThatOutlastsTheGrace(t *testing.T) {
	r := newRig(t)
	_, start, e, done := upped(t, r)
	defer done()
	pane := r.world.PaneByLabel("orchestrator")
	r.fix.Edit(pane, "agent_status", "working")
	r.srv.ForgetRequests()
	op := drive(t, e, control(t, start, route.VerbStop, 1, func(q *route.ControlRequest) { q.Grace = 50 * time.Millisecond }).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	if !slices.Contains(r.keysSent("esc"), pane) {
		t.Fatal("the agent past its grace was not interrupted")
	}
}
