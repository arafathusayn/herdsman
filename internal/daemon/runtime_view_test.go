package daemon

import (
	"slices"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// Gate "Public surfaces": RuntimeService reports, for each core execution,
// its observation, why it waits on a person, its unresolved items, its notices
// by state and its backend's explicit controls as separate fields; the CLI,
// the web and the SDKs all read these. Show focuses through the provider and
// nothing is typed.
func TestRuntimeServiceDescribesEachExecutionSeparately(t *testing.T) {
	r := newWorkerRig(t, NoticeConfig{})
	ctx := testCtxD(t)
	svc := r.h.d.noticeSvc()
	var attended runtime.Execution
	for _, e := range r.execs {
		if err := svc.Approve(e, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if e.HarnessKind == "codex" {
			attended = e
		}
	}
	if attended.ID == "" {
		t.Fatal("no Codex execution in the rig")
	}
	n, err := svc.Issue(ctx, notice.IssueRequest{Key: "t:view", Kind: notice.KindDispatch, Execution: attended.ID, Content: "work", Issuer: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := (submitStore{r.h.d}).PutSubmit(core.SubmitRecord{ID: "s-lost", Execution: attended.ID, State: core.SubmitUnresolved,
		Detail: "the reply was lost after typing", TextDigest: "d", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}

	c := routeClient(t, r.h)
	list, err := c.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: attended.Repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetExecutions()) != 2 {
		t.Fatalf("%v", list)
	}
	for _, e := range list.GetExecutions() {
		if !slices.Equal(e.GetActions(), []string{runtime.ActionShow, runtime.ActionSubmit}) {
			t.Errorf("%s actions %v", e.GetAgent(), e.GetActions())
		}
		if e.GetObservation().GetProcess() == "" || e.GetObservation().GetVisibility() == "" {
			t.Errorf("%s observation %+v", e.GetAgent(), e.GetObservation())
		}
		if e.GetRef().GetExecutionId() != attended.ID {
			if e.GetNotices().GetIssued() != 0 || len(e.GetUnresolved()) != 0 {
				t.Errorf("the other execution shows %s's state: %+v", attended.Agent, e)
			}
			continue
		}
		if got := e.GetNotices(); got.GetIssued() != 1 || got.GetClaimed() != 0 || got.GetOpenClaim() != "" {
			t.Errorf("notices %+v", got)
		}
		reasons := map[string]*daemonv1.AttentionReason{}
		for _, a := range e.GetAttention() {
			reasons[a.GetReason()] = a
		}
		if a := reasons[notice.AttentionNoticeWaiting]; a == nil || a.GetNoticeId() != n.ID || a.GetLine() != notice.AttendedLine {
			t.Errorf("attention %v", e.GetAttention())
		}
		if len(e.GetUnresolved()) != 1 || e.GetUnresolved()[0].GetId() != "s-lost" || !slices.Contains(e.GetUnresolved()[0].GetChoices(), "send_again") {
			t.Errorf("unresolved %v", e.GetUnresolved())
		}
	}
	one, err := c.Runtime.GetExecution(ctx, &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: attended.ID})
	if err != nil || one.GetExecution().GetNotices().GetIssued() != 1 || len(one.GetExecution().GetActions()) != 2 {
		t.Fatalf("get: %+v %v", one, err)
	}

	shown, err := c.Runtime.Show(ctx, &daemonv1.RuntimeServiceShowRequest{ExecutionId: attended.ID})
	if err != nil || shown.GetBackend() != runtime.BackendCore {
		t.Fatalf("show: %+v %v", shown, err)
	}
	if _, err := c.Runtime.Show(ctx, &daemonv1.RuntimeServiceShowRequest{ExecutionId: runtime.NewExecutionID()}); codeOf(err) != rpc.NotFound {
		t.Fatalf("show of nothing: %v", err)
	}
	if n := r.w.Core.Calls("terminal.send"); n != 0 {
		t.Errorf("typed into core %d times", n)
	}
	if n := r.w.Core.Calls("terminal.run"); n != 2 {
		t.Errorf("show started a terminal: %d runs", n)
	}
}

// A TUI execution lists the pane controls the web forwards, and none of
// core's.
func TestTUIExecutionsListThePaneControls(t *testing.T) {
	if got := runtime.Actions(runtime.BackendTUI); slices.Contains(got, runtime.ActionSubmit) || !slices.Contains(got, runtime.ActionPrompt) {
		t.Fatalf("%v", got)
	}
	e := &daemonv1.Execution{Ref: &corev1.RuntimeRef{Backend: runtime.BackendTUI, Pane: "w1:p2"}}
	(&runtimeService{}).describe(e)
	if !slices.Equal(e.GetActions(), runtime.Actions(runtime.BackendTUI)) || e.GetNotices() != nil {
		t.Fatalf("%+v", e)
	}
}
