package daemon

import (
	"context"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// A preview runs the checks of its call and reports what the call would do. It
// writes nothing: no journal record, queue entry, credential or route state. A
// plan describes the daemon as it stands, so a later call can still be refused.

func planOf(action, target, description string, irreversible bool) *corev1.Plan {
	return &corev1.Plan{Steps: []*corev1.PlanStep{{Action: action, Target: target, Description: description, Irreversible: irreversible}}}
}

// previewParty proves the route exists and the caller is one of its parties.
func (d *Daemon) previewParty(ctx context.Context, name string) (*Route, string, *APIError) {
	rt, e := d.route(name)
	if e != nil {
		return nil, "", e
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return nil, "", e
	}
	return rt, me, nil
}

func (s *messageService) previewShutdown(ctx context.Context, req *daemonv1.ShutdownRequest) (*daemonv1.ShutdownResponse, error) {
	if req.GetReason() != "stop" && req.GetReason() != "restart" {
		return nil, rpcFailure(ctx, Errf("bad_request", "reason is stop or restart"))
	}
	return &daemonv1.ShutdownResponse{Version: s.contract(), Plan: planOf("stop", "daemon", "stop the daemon and every route it serves", false)}, nil
}

func (s *messageService) previewCreateRoute(ctx context.Context, q CreateRequest) (*daemonv1.CreateRouteResponse, error) {
	s.d.regMu.Lock()
	defer s.d.regMu.Unlock()
	if _, e := s.d.admitCreate(q); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.CreateRouteResponse{Version: s.contract(), Plan: planOf("create", q.Route, "register the route with a new inbox", false)}, nil
}

func (s *messageService) previewAdoptRoute(ctx context.Context, name string, q AdoptRequest) (*daemonv1.AdoptRouteResponse, error) {
	s.d.regMu.Lock()
	defer s.d.regMu.Unlock()
	if e := s.d.admitAdopt(name, q); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := s.d.admitAdoptInbox(name, q); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.AdoptRouteResponse{Version: s.contract(), Plan: planOf("adopt", name, "move the file inbox "+q.Inbox+" under the daemon", false)}, nil
}

func (s *messageService) previewRemoveRoute(ctx context.Context, name string) (*daemonv1.RemoveRouteResponse, error) {
	rf, err := ReadRoutes(s.d.cfg.PrivateHome)
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "%v", err))
	}
	if _, ok := rf.Routes[name]; !ok {
		return nil, rpcFailure(ctx, Errf("unknown_route", "route %s is not registered", name))
	}
	return &daemonv1.RemoveRouteResponse{Version: s.contract(), Plan: planOf("remove", name, "forget the route and keep its inbox", false)}, nil
}

func (s *messageService) previewResetEvents(ctx context.Context, name, confirm string) (*daemonv1.ResetEventsResponse, error) {
	if confirm != name {
		return nil, rpcFailure(ctx, Errf("bad_request", "confirm_route must name the route"))
	}
	if _, e := s.d.route(name); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ResetEventsResponse{Version: s.contract(), Plan: planOf("reset", name, "discard the route's event journal and start a new epoch", true)}, nil
}

func (s *messageService) previewEnroll(ctx context.Context, name string, q EnrollRequest) (*daemonv1.EnrollResponse, error) {
	rt, e := s.d.route(name)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	id, e := s.d.liveIdentity(ctx, name, callHeader(ctx).Get(HeaderPane))
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		_, e := rt.admitEnroll(q.Name, q.Role, id)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.EnrollResponse{Version: s.contract(), Plan: planOf("enroll", q.Name, "propose the calling pane as "+q.Role, false)}, nil
}

func (s *messageService) previewSend(ctx context.Context, route string, q SendRequest) (*daemonv1.SendResponse, error) {
	rt, me, e := s.d.previewParty(ctx, route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	var earlier *SendResult
	if e := rt.do(ctx, func() *APIError {
		var e *APIError
		earlier, _, _, e = rt.admitSend(me, q)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if earlier != nil {
		return &daemonv1.SendResponse{Version: s.contract(), Plan: planOf("send", q.To, "answer with message "+earlier.ID+", already sent under this key", false)}, nil
	}
	if q.Kind == "side_agent_data" {
		var authority string
		if e := rt.do(ctx, func() *APIError {
			authority, _ = rt.authority(ctx, s.d.herdrFor(rt.name))
			return nil
		}); e != nil {
			return nil, rpcFailure(ctx, e)
		}
		if authority != "RELAY" {
			return nil, rpcFailure(ctx, Errf("human_boundary", "HUMAN boundary; side-agent data not queued"))
		}
	}
	description := "queue a " + q.Kind + " message"
	return &daemonv1.SendResponse{Version: s.contract(), Plan: planOf("send", q.To, description, false)}, nil
}

func (s *messageService) previewResolveOperation(ctx context.Context, req *daemonv1.ResolveOperationRequest) (*daemonv1.ResolveOperationResponse, error) {
	if _, e := resolveDecision(ResolveRequest{Decision: req.GetDecision()}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if err := s.d.ops.CheckResolve(req.GetId(), int(req.GetStep())); err != nil {
		return nil, rpcFailure(ctx, resolveRefusal(req.GetId(), err))
	}
	return &daemonv1.ResolveOperationResponse{Version: s.contract(), Plan: planOf("resolve", req.GetId(), "mark the step "+req.GetDecision(), false)}, nil
}

func (s *messageService) previewPutSetup(ctx context.Context, name string, q SetupRequest) (*daemonv1.PutSetupResponse, error) {
	rt, e := s.d.route(name)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		_, _, _, e := rt.admitSetup(q)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.PutSetupResponse{Version: s.contract(), Plan: planOf("record", name, "record the setup proof", false)}, nil
}

func (s *messageService) previewApprovePair(ctx context.Context, name string, q ApproveRequest) (*daemonv1.ApprovePairResponse, error) {
	rt, e := s.d.route(name)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		_, e := rt.admitApprove(q, true)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ApprovePairResponse{Version: s.contract(), Plan: planOf("approve", name, "approve the pending registry and issue its credentials", false)}, nil
}

func (s *messageService) previewRetirePair(ctx context.Context, route string, q RetireRequest) (*daemonv1.RetirePairResponse, error) {
	rt, e := s.d.route(route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		_, e := rt.admitRetire(q.Name, q.SHA256)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	name := q.Name
	return &daemonv1.RetirePairResponse{Version: s.contract(), Plan: planOf("retire", name, "retire the party and revoke its credential", true)}, nil
}

func (s *messageService) previewRevokeCredential(ctx context.Context, route, name string) (*daemonv1.RevokeCredentialResponse, error) {
	rt, e := s.d.route(route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError { return rt.admitRevoke(name) }); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RevokeCredentialResponse{Version: s.contract(), Plan: planOf("revoke", name, "revoke the party's credential", true)}, nil
}

func (s *messageService) previewClaim(ctx context.Context, route string, q ClaimRequest) (*daemonv1.ClaimResponse, error) {
	rt, me, e := s.d.previewParty(ctx, route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		if e := rt.deliveryAllowed(); e != nil {
			return e
		}
		_, _, e := rt.admitClaim(me, q)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ClaimResponse{Version: s.contract(), Plan: planOf("claim", route, "claim the caller's ready messages", false)}, nil
}

func (s *messageService) previewAck(ctx context.Context, route, claim string, q AckRequest) (*daemonv1.AckResponse, error) {
	rt, me, e := s.d.previewParty(ctx, route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError {
		_, _, e := rt.admitAck(me, claim, q)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	id := q.ID
	return &daemonv1.AckResponse{Version: s.contract(), Plan: planOf("ack", id, "acknowledge the claimed message", true)}, nil
}

func (s *messageService) previewRelease(ctx context.Context, route, claim string) (*daemonv1.ReleaseResponse, error) {
	rt, me, e := s.d.previewParty(ctx, route)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if e := rt.do(ctx, func() *APIError { return rt.admitRelease(me, claim) }); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ReleaseResponse{Version: s.contract(), Plan: planOf("release", claim, "give back the acknowledged claim", false)}, nil
}

// admitHoldPreview runs a hold's checks for a preview.
func (s *messageService) admitHoldPreview(ctx context.Context, route, target string, reason *string) *APIError {
	rt, me, e := s.d.previewParty(ctx, route)
	if e != nil {
		return e
	}
	return rt.do(ctx, func() *APIError {
		_, _, e := rt.admitHold(me, target, reason)
		return e
	})
}

func (s *messageService) previewHold(ctx context.Context, route, target, reason string) (*daemonv1.HoldResponse, error) {
	if e := s.admitHoldPreview(ctx, route, target, &reason); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.HoldResponse{Version: s.contract(), Plan: planOf("hold", target, "place the caller's hold on the target", false)}, nil
}

func (s *messageService) previewUnhold(ctx context.Context, route, target string) (*daemonv1.UnholdResponse, error) {
	if e := s.admitHoldPreview(ctx, route, target, nil); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.UnholdResponse{Version: s.contract(), Plan: planOf("unhold", target, "remove the caller's hold from the target", false)}, nil
}
