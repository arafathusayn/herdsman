package daemon

import (
	"context"
	"slices"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// The choices a person has for an unresolved typed submit (spec section 10,
// "A send cannot be recalled").
var unresolvedChoices = []string{"keep_waiting", "stop_execution", "send_again"}

// describe fills what a person's surface shows of an execution beside its
// observation: why it waits on a person, what is unresolved, its notices by
// state, and the controls its backend allows. Each is its own field, read from
// its own evidence; none is derived from the program status.
func (s *runtimeService) describe(out *daemonv1.Execution) {
	backend := out.GetRef().GetBackend()
	out.Actions = runtime.Actions(backend)
	id := out.GetRef().GetExecutionId()
	if backend != runtime.BackendCore || id == "" {
		return
	}
	if svc := s.d.noticeSvc(); svc != nil {
		if list, err := svc.List(id); err == nil {
			out.Notices, out.Attention = noticeView(list)
		}
		if svc.NotWaiting(id) {
			out.Attention = append(out.Attention, &daemonv1.AttentionReason{Reason: notice.EventNotWaiting,
				Detail: "the agent holds no `herdsman notice wait`; its notices stay issued until a person submits its start line again"})
		}
	}
	if out.GetObservation().GetReadiness() == string(runtime.ReadinessNeedsAttention) && len(out.Attention) == 0 {
		out.Attention = append(out.Attention, &daemonv1.AttentionReason{Reason: string(runtime.ReadinessNeedsAttention),
			Detail: "the execution waits on a person in its pane in the app"})
	}
	if subs, err := (submitStore{s.d}).Submits(id); err == nil {
		for _, r := range subs {
			if r.State != core.SubmitUnresolved && r.State != core.SubmitPendingInCore {
				continue
			}
			item := &daemonv1.UnresolvedItem{Kind: "typed_submit", Id: r.ID, State: r.State, Detail: r.Detail, Updated: millis(r.Updated)}
			if r.State == core.SubmitUnresolved {
				item.Choices = slices.Clone(unresolvedChoices)
			}
			out.Unresolved = append(out.Unresolved, item)
		}
	}
}

// noticeView counts an execution's notices by state and names the reasons
// they wait on a person.
func noticeView(list []notice.Notice) (*daemonv1.NoticeSummary, []*daemonv1.AttentionReason) {
	sum := &daemonv1.NoticeSummary{}
	var reasons []*daemonv1.AttentionReason
	for _, n := range list {
		switch n.State {
		case notice.StateIssued:
			sum.Issued++
			if sum.HeldGate == "" && n.HeldGate != "" {
				sum.HeldGate = n.HeldGate
			}
			if n.Attention {
				reasons = append(reasons, &daemonv1.AttentionReason{Reason: notice.AttentionNoticeWaiting, NoticeId: n.ID, Line: notice.AttendedLine,
					Detail: "a notice waits for this attended execution; a person types the line in its pane"})
			}
			if n.HeldReported {
				reasons = append(reasons, &daemonv1.AttentionReason{Reason: notice.EventHeld, NoticeId: n.ID, Gate: n.HeldGate,
					Detail: "the notice cannot be claimed: " + n.HeldGate})
			}
		case notice.StateClaimed:
			sum.Claimed++
			if n.Ordinary() {
				sum.OpenClaim = n.ID
			}
			if n.Interrupted {
				reasons = append(reasons, &daemonv1.AttentionReason{Reason: notice.AttentionInterrupted, NoticeId: n.ID, Line: notice.AttendedLine,
					Detail: "the harness was interrupted with a claim open; a person types the line in its pane"})
			}
			if n.StuckReported {
				reasons = append(reasons, &daemonv1.AttentionReason{Reason: notice.EventClaimStuck, NoticeId: n.ID,
					Detail: "the claim stays open after recovery was offered"})
			}
		case notice.StateHandled:
			sum.Handled++
		case notice.StateReturned:
			sum.Returned++
		}
	}
	return sum, reasons
}

// Show implements RuntimeService.Show: the provider focuses the execution's
// existing view. It creates nothing and types nothing.
func (s *runtimeService) Show(ctx context.Context, req *daemonv1.RuntimeServiceShowRequest) (*daemonv1.RuntimeServiceShowResponse, error) {
	if req.GetExecutionId() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no execution")
	}
	var row store.Execution
	found := false
	if err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		row, found, err = store.ExecutionByID(c, req.GetExecutionId())
		return err
	}); err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "executions: %v", err))
	}
	if !found {
		return nil, rpc.Errorf(rpc.NotFound, "no execution %s", req.GetExecutionId())
	}
	endpoint := ""
	if row.Backend == runtime.BackendTUI {
		endpoint = row.EndpointID
	}
	p, err := s.d.runtimeRegistry().Open(row.Backend, endpoint)
	if err != nil {
		return nil, runtimeFailure(err)
	}
	target := runtime.Target{Name: row.Agent, Pane: row.Pane, ExecutionID: row.ID, Generation: row.Generation, TerminalAddress: row.TerminalAddress}
	if err := p.Show(ctx, target); err != nil {
		if code := core.CodeOf(err); code != "" {
			return nil, rpc.Errorf(rpc.FailedPrecondition, "%v", err)
		}
		return nil, rpc.Errorf(rpc.Unavailable, "%v", err)
	}
	return &daemonv1.RuntimeServiceShowResponse{Backend: row.Backend}, nil
}
