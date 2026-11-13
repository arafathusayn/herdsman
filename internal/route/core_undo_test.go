package route_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// startedCoreRoute runs a core route without an adjutant to done.
func startedCoreRoute(t *testing.T, r *coreRig, e *ops.Engine, q route.UpRequest) (*route.UpPlan, *ops.Operation) {
	t.Helper()
	p := r.plan(q)
	op := drive(t, e, p.Spec)
	r.attestShell(p.Key)
	for i := 0; op.State != ops.OpDone; i++ {
		if i > 8 || op.State == ops.OpFailed {
			t.Fatalf("start: %s %q", op.State, lastError(op))
		}
		if err := route.Continue(e, r.callers, p, op, nil); err != nil {
			t.Fatal(err)
		}
		var err error
		if op, err = e.Drive(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	start, err := e.Get(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p, start
}

// Undo of a core route's delete cannot bring its executions back: they ended
// with the delete. The restored route is a stopped route, and its resume
// starts every agent again under fresh launches.
func TestUndoneCoreDeleteResumesUnderFreshLaunches(t *testing.T) {
	r := newCoreRig(t)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", r.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	r.recovery = &pipeline{repo: r.repo}
	r.down = func(context.Context, string, string) error { r.downs++; return nil }
	r.w.OpenApp()
	e, closeDB := r.engine()
	defer closeDB()
	q := r.request()
	q.NoAdjutant = true
	p, start := startedCoreRoute(t, r, e, q)
	before, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
	// What undo restores: the route folder, as it was before the delete.
	saved := filepath.Join(t.TempDir(), "folder")
	if err := os.CopyFS(saved, os.DirFS(p.Folder)); err != nil {
		t.Fatal(err)
	}
	plan, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbDelete, Start: start, Generation: 1, Force: true, Date: "2026-11-11"})
	if err != nil {
		t.Fatal(err)
	}
	del := drive(t, e, plan.Spec)
	if del.State != ops.OpDone {
		t.Fatalf("delete: %s %q", del.State, lastError(del))
	}
	verbs := []*ops.Operation{del}
	if got := route.ControlState(verbs); got != route.ControlDeleted {
		t.Fatalf("after the delete: %s", got)
	}
	if err := os.CopyFS(p.Folder, os.DirFS(saved)); err != nil {
		t.Fatal(err)
	}
	state := route.ControlStateUndone(verbs, map[string]bool{del.ID: true})
	if state != route.ControlStopped {
		t.Fatalf("after the undo: %s", state)
	}
	resume, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbResume, Start: start, Generation: 2, From: state, Date: "2026-11-11"})
	if err != nil {
		t.Fatal(err)
	}
	got := drive(t, e, resume.Spec)
	if got.State != ops.OpStopped || !strings.Contains(lastError(got), core.CodeNeedsAttention) {
		t.Fatalf("resume: %s %q\n%s", got.State, lastError(got), resume.Text)
	}
	after, _ := r.w.Records.List(context.Background(), r.repo, "harbor")
	if len(after) != 2*len(before) {
		t.Fatalf("the resume launched %d executions after %d", len(after)-len(before), len(before))
	}
	old := map[string]bool{}
	for _, x := range before {
		old[x.LaunchIntent], old[x.ID] = true, true
	}
	fresh := 0
	for _, x := range after {
		if !old[x.ID] {
			fresh++
			if old[x.LaunchIntent] {
				t.Errorf("the resume reused launch %s", x.LaunchIntent)
			}
			continue
		}
		if x.Desired != runtime.DesiredStopped {
			t.Errorf("an execution the delete ended runs again: %+v", x)
		}
	}
	if fresh != len(before) {
		t.Fatalf("fresh executions: %d", fresh)
	}
}

// A finished delete names the channel it forgot; a delete of a route without
// an inbox names none, and nor does another operation.
func TestForgottenChannelOfADelete(t *testing.T) {
	r, _ := recoverable(t)
	_, start, _, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	op := &ops.Operation{ID: "d", Kind: route.KindDelete}
	for _, sp := range c.Spec.Steps {
		op.Steps = append(op.Steps, ops.Step{StepSpec: sp})
	}
	ch, ok := route.ForgottenChannelOf(op)
	if !ok || ch.Route != "harbor" || ch.Inbox == "" || ch.Backend != runtime.BackendTUI || ch.HerdrSocket == "" {
		t.Fatalf("%+v %v", ch, ok)
	}
	op.Kind = route.KindStop
	if _, ok := route.ForgottenChannelOf(op); ok {
		t.Error("a stop forgot a channel")
	}
	if _, err := os.Stat(ch.Inbox); errors.Is(err, os.ErrNotExist) {
		t.Fatal("the plan alone removed the inbox")
	}
}
