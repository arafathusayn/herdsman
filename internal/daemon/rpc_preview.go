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
	if _, e := s.d.route(name); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if _, e := s.d.liveIdentity(ctx, name, callHeader(ctx).Get(HeaderPane)); e != nil {
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
	description := "queue a " + q.Kind + " message"
	switch {
	case earlier != nil:
		description = "answer with message " + earlier.ID + ", already sent under this key"
	case q.Kind == "side_agent_data":
		description += ", if the route's authority is RELAY when it is sent"
	}
	return &daemonv1.SendResponse{Version: s.contract(), Plan: planOf("send", q.To, description, false)}, nil
}

func (s *messageService) previewResolveOperation(ctx context.Context, req *daemonv1.ResolveOperationRequest) (*daemonv1.ResolveOperationResponse, error) {
	if d := req.GetDecision(); d != "done" && d != "rerun" {
		return nil, rpcFailure(ctx, Errf("bad_request", "decision must be done or rerun, got %q", d))
	}
	if _, e := s.d.getOp(req.GetId()); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ResolveOperationResponse{Version: s.contract(), Plan: planOf("resolve", req.GetId(), "mark the step "+req.GetDecision(), false)}, nil
}

func (s *messageService) previewPutSetup(ctx context.Context, name string) (*daemonv1.PutSetupResponse, error) {
	if _, e := s.d.route(name); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.PutSetupResponse{Version: s.contract(), Plan: planOf("record", name, "record the setup proof", false)}, nil
}

func (s *messageService) previewApprovePair(ctx context.Context, name string) (*daemonv1.ApprovePairResponse, error) {
	if _, e := s.d.route(name); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ApprovePairResponse{Version: s.contract(), Plan: planOf("approve", name, "approve the pending registry and issue its credentials", false)}, nil
}

func (s *messageService) previewRetirePair(ctx context.Context, route, name string) (*daemonv1.RetirePairResponse, error) {
	if _, e := s.d.route(route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RetirePairResponse{Version: s.contract(), Plan: planOf("retire", name, "retire the party and revoke its credential", true)}, nil
}

func (s *messageService) previewRevokeCredential(ctx context.Context, route, name string) (*daemonv1.RevokeCredentialResponse, error) {
	if _, e := s.d.route(route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RevokeCredentialResponse{Version: s.contract(), Plan: planOf("revoke", name, "revoke the party's credential", true)}, nil
}

func (s *messageService) previewClaim(ctx context.Context, route string) (*daemonv1.ClaimResponse, error) {
	if _, _, e := s.d.previewParty(ctx, route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ClaimResponse{Version: s.contract(), Plan: planOf("claim", route, "claim the caller's ready messages", false)}, nil
}

func (s *messageService) previewAck(ctx context.Context, route, id string) (*daemonv1.AckResponse, error) {
	if _, _, e := s.d.previewParty(ctx, route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.AckResponse{Version: s.contract(), Plan: planOf("ack", id, "acknowledge the claimed message", true)}, nil
}

func (s *messageService) previewRelease(ctx context.Context, route, claim string) (*daemonv1.ReleaseResponse, error) {
	if _, _, e := s.d.previewParty(ctx, route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ReleaseResponse{Version: s.contract(), Plan: planOf("release", claim, "give back the acknowledged claim", false)}, nil
}

func (s *messageService) previewHold(ctx context.Context, route, target string) (*daemonv1.HoldResponse, error) {
	if _, _, e := s.d.previewParty(ctx, route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.HoldResponse{Version: s.contract(), Plan: planOf("hold", target, "place the caller's hold on the target", false)}, nil
}

func (s *messageService) previewUnhold(ctx context.Context, route, target string) (*daemonv1.UnholdResponse, error) {
	if _, _, e := s.d.previewParty(ctx, route); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.UnholdResponse{Version: s.contract(), Plan: planOf("unhold", target, "remove the caller's hold from the target", false)}, nil
}
