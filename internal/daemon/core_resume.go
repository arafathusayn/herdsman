package daemon

import (
	"context"
	"slices"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// ResumeGoalResult is what a resume-goal on herdr-core reports: always held,
// since only a person resumes a paused goal (spec section 10).
type ResumeGoalResult struct {
	Status    string
	Reason    string
	Line      string
	Execution runtime.Execution
}

// resumeGoal serves `herdsman message resume-goal <name>` on a herdr-core
// route. Nothing is typed into the harness: the target's paused goal is a
// state only a person ends, so the daemon raises needs_attention with reason
// goal_paused and the line to submit, once per call, and answers held. The
// caller is a party of the route, proven by its process; the target is an
// approved party other than the adjutant whose harness has a paused goal.
func (d *Daemon) resumeGoal(ctx context.Context, name, target string, res *ResumeGoalResult) *APIError {
	rt, e := d.route(name)
	if e != nil {
		return e
	}
	if !d.channelOnCore(name) {
		return Errf("bad_request", "resume-goal through the daemon serves a herdr-core route; route %s runs on the TUI", name)
	}
	if _, e := d.authParty(ctx, rt); e != nil {
		return e
	}
	c := d.coreRT()
	if c == nil {
		return Errf("identity_unavailable", "this daemon runs no herdr-core backend")
	}
	var peer *inbox.Peer
	if e := rt.do(ctx, func() *APIError {
		b, ok := rt.registryBytes("peers.approved")
		if !ok {
			return Errf("unknown_target", "the route has no approved pair")
		}
		peers, ok := inbox.ValidRegistry(b)
		if !ok {
			return Errf("internal", "invalid approved registry")
		}
		for i := range peers {
			if peers[i].Name == target {
				peer = &peers[i]
			}
		}
		return nil
	}); e != nil {
		return e
	}
	if peer == nil {
		return Errf("unknown_target", "%s is not an approved party of route %s", target, name)
	}
	if peer.Role == "adjutant" {
		return Errf("bad_request", "never target the adjutant")
	}
	id, ok := inbox.ExecutionOf(peer.Identity)
	if !ok {
		return Errf("unknown_target", "%s is bound to no execution", target)
	}
	entry, _ := d.routeEntry(name)
	all, err := c.records.List(ctx, entry.Repo, name)
	if err != nil {
		return Errf("internal", "read the execution records: %v", err)
	}
	i := slices.IndexFunc(all, func(x runtime.Execution) bool { return x.ID == id })
	if i < 0 || d.executionReplaced(id) {
		return Errf("unknown_target", "%s's execution %s no longer runs", target, id)
	}
	exec := all[i]
	a, err := core.AdapterFor(exec.HarnessKind)
	if err != nil {
		return Errf("bad_request", "%v", err)
	}
	if !slices.Contains(a.Facts.PersonOnly, core.PersonOnlyGoalPaused) {
		return Errf("bad_request", "%s runs %s, which has no paused goal to resume", target, exec.HarnessKind)
	}
	d.noticeSvc().Emit(notice.Event{Kind: notice.EventNeedsAttention, Repo: exec.Repo, Route: exec.Route, Execution: exec.ID,
		Reason: core.AttentionGoalPaused, Line: core.GoalResumeLine,
		Detail: "a paused goal is resumed only by a person: submit the line in " + target + "'s terminal"})
	*res = ResumeGoalResult{Status: "held", Reason: core.AttentionGoalPaused, Line: core.GoalResumeLine, Execution: exec}
	return nil
}

// ResumeGoal implements MessageService.ResumeGoal.
func (s *messageService) ResumeGoal(ctx context.Context, req *daemonv1.ResumeGoalRequest) (*daemonv1.ResumeGoalResponse, error) {
	var out ResumeGoalResult
	if e := s.d.resumeGoal(ctx, req.GetRoute().GetValue(), req.GetTarget(), &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	x := out.Execution
	return &daemonv1.ResumeGoalResponse{Version: s.contract(), Status: out.Status, Reason: out.Reason, Line: out.Line,
		Execution: &corev1.RuntimeRef{Backend: x.Backend, ExecutionId: x.ID, Generation: x.Generation, EndpointId: x.EndpointID, TerminalAddress: x.TerminalAddress}}, nil
}
