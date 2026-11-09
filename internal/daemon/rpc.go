package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"time"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"github.com/arafathusayn/herdsman/sdk/version"
)

// requestKey holds the HTTP request in a service call's context: authority is
// read from the request's headers, which a generated service method cannot see.
type requestKey struct{}

// callHeader is the headers of the call in ctx; a context with no call has
// none.
func callHeader(ctx context.Context) http.Header {
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		return r.Header
	}
	return http.Header{}
}

// rpcHandler serves the RPC procedures under the daemon's body bound. Every
// method but Hello refuses a client whose declared version the daemon does not
// match; a caller that declares none is not a herdsman build and is let in.
func (d *Daemon) rpcHandler() http.Handler {
	mux := rpc.NewMux(d.rpcProcedures()...)
	return http.MaxBytesHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Base(r.URL.Path) != "Hello" {
			e := CheckVersion(r.Header, d.cfg.Version)
			if e == nil {
				e = d.refuseInInspect(r)
			}
			if e != nil {
				rpc.WriteError(w, rpcFailure(r.Context(), e))
				return
			}
		}
		if heldDuringExport(r.URL.Path) {
			// The server notices a client leaving only once the body has
			// been read, and this call may wait a long time.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				// The RPC layer refuses a bad body in its own words.
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), failedRead{err}))
				mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			release, e := d.enterMutation(r.Context())
			if e != nil {
				rpc.WriteError(w, rpcFailure(r.Context(), e))
				return
			}
			defer release()
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
	}), maxBody)
}

// failedRead replays the error of a body that was already read.
type failedRead struct{ err error }

func (f failedRead) Read([]byte) (int, error) { return 0, f.err }

// messageService is the daemon's MessageService.
type messageService struct{ d *Daemon }

// contract is the Version every reply carries.
func (s *messageService) contract() *corev1.Version {
	return &corev1.Version{Contract: version.Version, Build: s.d.cfg.Version}
}

func routeID(name string) *corev1.RouteId { return &corev1.RouteId{Value: name} }

func wireMessageID(id string) *corev1.MessageId { return &corev1.MessageId{Value: id} }

func (s *messageService) Hello(_ context.Context, _ *daemonv1.HelloRequest) (*daemonv1.HelloResponse, error) {
	h := s.d.helloBody()
	return &daemonv1.HelloResponse{Version: s.contract(), DaemonVersion: h.DaemonVersion, Features: h.Features,
		CredentialOptions: h.CredentialOptions, DefaultCredential: h.DefaultCredential}, nil
}

func (s *messageService) Health(ctx context.Context, _ *daemonv1.HealthRequest) (*daemonv1.HealthResponse, error) {
	h := s.d.healthBody(ctx)
	res := &daemonv1.HealthResponse{Version: s.contract(), Status: h.Status}
	for _, r := range h.Routes {
		res.Routes = append(res.Routes, &daemonv1.RouteHealth{Route: routeID(r.Route), Status: r.Status, Flags: r.Flags, Reason: r.Reason})
	}
	for _, x := range h.Herdr {
		res.Herdr = append(res.Herdr, &daemonv1.HerdrHealth{Socket: x.Socket, Status: x.Status})
	}
	return res, nil
}

func (s *messageService) Shutdown(ctx context.Context, req *daemonv1.ShutdownRequest) (*daemonv1.ShutdownResponse, error) {
	if req.GetDryRun() {
		return s.previewShutdown(ctx, req)
	}
	if e := s.d.shutdown(req.GetReason()); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ShutdownResponse{Version: s.contract()}, nil
}

func (s *messageService) ListRoutes(ctx context.Context, _ *daemonv1.ListRoutesRequest) (*daemonv1.ListRoutesResponse, error) {
	routes, e := s.d.listRoutes(ctx)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	res := &daemonv1.ListRoutesResponse{Version: s.contract()}
	for _, r := range routes {
		res.Routes = append(res.Routes, &daemonv1.RouteInfo{Route: routeID(r.Route), Inbox: r.Inbox, Owner: r.Owner, Epoch: r.Epoch, LastSeq: r.LastSeq, Status: r.Status})
	}
	return res, nil
}

func (s *messageService) CreateRoute(ctx context.Context, req *daemonv1.CreateRouteRequest) (*daemonv1.CreateRouteResponse, error) {
	if req.GetDryRun() {
		return s.previewCreateRoute(ctx, CreateRequest{Repo: req.GetRepo(), Route: req.GetRoute().GetValue(), HerdrSocket: req.GetHerdrSocket()})
	}
	var out CreateResult
	if e := s.d.createRoute(CreateRequest{Repo: req.GetRepo(), Route: req.GetRoute().GetValue(), HerdrSocket: req.GetHerdrSocket()}, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.CreateRouteResponse{Version: s.contract(), Route: routeID(out.Route), Inbox: out.Inbox, Epoch: out.Epoch}, nil
}

func (s *messageService) AdoptRoute(ctx context.Context, req *daemonv1.AdoptRouteRequest) (*daemonv1.AdoptRouteResponse, error) {
	if req.GetDryRun() {
		return s.previewAdoptRoute(ctx, req.GetRoute().GetValue(), AdoptRequest{Inbox: req.GetInbox(), ConfirmPairSHA256: req.GetConfirmPairSha256()})
	}
	var out AdoptResult
	if e := s.d.adoptRoute(ctx, req.GetRoute().GetValue(), AdoptRequest{Inbox: req.GetInbox(), ConfirmPairSHA256: req.GetConfirmPairSha256()}, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.AdoptRouteResponse{Version: s.contract(), Route: routeID(out.Route), Epoch: out.Epoch, LastMessageId: out.LastMessageID, LegacyProof: out.LegacyProof}, nil
}

func (s *messageService) RemoveRoute(ctx context.Context, req *daemonv1.RemoveRouteRequest) (*daemonv1.RemoveRouteResponse, error) {
	if req.GetDryRun() {
		return s.previewRemoveRoute(ctx, req.GetRoute().GetValue())
	}
	if e := s.d.removeRoute(req.GetRoute().GetValue()); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RemoveRouteResponse{Version: s.contract(), Removed: true, InboxKept: true}, nil
}

func (s *messageService) ResetEvents(ctx context.Context, req *daemonv1.ResetEventsRequest) (*daemonv1.ResetEventsResponse, error) {
	if req.GetDryRun() {
		return s.previewResetEvents(ctx, req.GetRoute().GetValue(), req.GetConfirmRoute())
	}
	var out ResetResult
	if e := s.d.resetEvents(ctx, req.GetRoute().GetValue(), ResetRequest{ConfirmRoute: req.GetConfirmRoute()}, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ResetEventsResponse{Version: s.contract(), Epoch: out.Epoch, CheckpointSeq: out.CheckpointSeq}, nil
}

// Status serves the route status: a call that names a pane must be that pane's
// party.
func (s *messageService) Status(ctx context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // the route's actor could still answer a call already over
	}
	name := req.GetRoute().GetValue()
	if name == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no route")
	}
	rt, e := s.d.route(name)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if callHeader(ctx).Get(HeaderPane) != "" {
		if _, e := s.d.authParty(ctx, rt); e != nil {
			return nil, rpcFailure(ctx, e)
		}
	}
	var st RouteStatus
	if e := rt.do(ctx, func() *APIError { st = rt.fullStatus(); return nil }); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return statusToProto(st), nil
}

func (s *messageService) GetSetup(ctx context.Context, req *daemonv1.GetSetupRequest) (*daemonv1.GetSetupResponse, error) {
	var out SetupInventory
	if e := s.d.getSetup(ctx, req.GetRoute().GetValue(), &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	res := &daemonv1.GetSetupResponse{Version: s.contract()}
	for _, a := range out.Inventory {
		res.Inventory = append(res.Inventory, &daemonv1.InventoryAgent{Pane: a.Pane, SessionJson: string(a.Session), Agent: a.Agent})
	}
	if out.Proof != nil {
		b, _ := encode(out.Proof)
		res.ProofJson = string(b)
	}
	return res, nil
}

func (s *messageService) PutSetup(ctx context.Context, req *daemonv1.PutSetupRequest) (*daemonv1.PutSetupResponse, error) {
	if req.GetDryRun() {
		return s.previewPutSetup(ctx, req.GetRoute().GetValue())
	}
	q := SetupRequest{Rules: req.GetRules(), Binary: req.GetBinary(), PolicyVerified: req.GetPolicyVerified(),
		Boundary: rawJSON(req.GetBoundaryJson()), WatchTargets: rawJSON(req.GetWatchTargetsJson()), ResumeTargets: rawJSON(req.GetResumeTargetsJson())}
	for _, p := range req.GetProbes() {
		q.Probes = append(q.Probes, Probe{Identity: rawJSON(p.GetIdentityJson()), InboxWriteDenied: p.GetInboxWriteDenied(),
			SocketConnectDenied: p.GetSocketConnectDenied(), RecordedBy: p.GetRecordedBy()})
	}
	var out SetupResult
	if e := s.d.putSetup(ctx, req.GetRoute().GetValue(), q, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.PutSetupResponse{Version: s.contract(), ProofSha256: out.ProofSHA256}, nil
}

// rawJSON is a JSON field of a message; absent stays absent.
func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func (s *messageService) Enroll(ctx context.Context, req *daemonv1.EnrollRequest) (*daemonv1.EnrollResponse, error) {
	if req.GetDryRun() {
		return s.previewEnroll(ctx, req.GetRoute().GetValue(), EnrollRequest{Name: req.GetName(), Role: req.GetRole()})
	}
	var sum string
	if e := s.d.enroll(ctx, req.GetRoute().GetValue(), EnrollRequest{Name: req.GetName(), Role: req.GetRole()}, &sum); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.EnrollResponse{Version: s.contract(), PendingSha256: sum}, nil
}

func (s *messageService) ApprovePair(ctx context.Context, req *daemonv1.ApprovePairRequest) (*daemonv1.ApprovePairResponse, error) {
	if req.GetDryRun() {
		return s.previewApprovePair(ctx, req.GetRoute().GetValue())
	}
	var out ApproveResult
	q := ApproveRequest{SHA256: req.GetSha256(), Interaction: req.GetInteraction(), AskedBy: req.GetAskedBy(), Credentials: req.GetCredentials()}
	if e := s.d.approve(ctx, req.GetRoute().GetValue(), q, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	res := &daemonv1.ApprovePairResponse{Version: s.contract(), Approved: out.Approved, Revoked: out.Revoked}
	for _, c := range out.Credentials {
		res.Credentials = append(res.Credentials, &daemonv1.ApprovedCredential{Name: c.Name, Option: c.Option})
	}
	return res, nil
}

func (s *messageService) RetirePair(ctx context.Context, req *daemonv1.RetirePairRequest) (*daemonv1.RetirePairResponse, error) {
	if req.GetDryRun() {
		return s.previewRetirePair(ctx, req.GetRoute().GetValue(), req.GetName())
	}
	if e := s.d.retire(ctx, req.GetRoute().GetValue(), RetireRequest{Name: req.GetName(), SHA256: req.GetSha256()}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RetirePairResponse{Version: s.contract(), Retired: req.GetName(), Revoked: true}, nil
}

func (s *messageService) RevokeCredential(ctx context.Context, req *daemonv1.RevokeCredentialRequest) (*daemonv1.RevokeCredentialResponse, error) {
	if req.GetDryRun() {
		return s.previewRevokeCredential(ctx, req.GetRoute().GetValue(), req.GetName())
	}
	if e := s.d.revokeCredential(ctx, req.GetRoute().GetValue(), req.GetName()); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.RevokeCredentialResponse{Version: s.contract(), Revoked: true}, nil
}

func (s *messageService) Send(ctx context.Context, req *daemonv1.SendRequest) (*daemonv1.SendResponse, error) {
	if req.GetDryRun() {
		return s.previewSend(ctx, req.GetRoute().GetValue(), SendRequest{To: req.GetTo(), Kind: req.GetKind(), Body: req.GetBody(), ReplyTo: req.GetReplyTo(), Key: req.GetKey()})
	}
	var out SendResult
	q := SendRequest{To: req.GetTo(), Kind: req.GetKind(), Body: req.GetBody(), ReplyTo: req.GetReplyTo(), Key: req.GetKey()}
	if e := s.d.send(ctx, req.GetRoute().GetValue(), q, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.SendResponse{Version: s.contract(), Id: wireMessageID(out.ID), Seq: out.Seq, Duplicate: out.Duplicate}, nil
}

func (s *messageService) Claim(ctx context.Context, req *daemonv1.ClaimRequest) (*daemonv1.ClaimResponse, error) {
	if req.GetDryRun() {
		return s.previewClaim(ctx, req.GetRoute().GetValue())
	}
	var out ClaimResult
	q := ClaimRequest{ReadKey: req.GetReadKey(), Token: req.GetToken(), ExtendKey: req.GetExtendKey()}
	if e := s.d.claim(ctx, req.GetRoute().GetValue(), q, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	res := &daemonv1.ClaimResponse{Version: s.contract(), Claim: out.Claim, PendingNew: int32(out.Summary.PendingNew), Quarantine: int32(out.Summary.Quarantine)}
	for _, m := range out.Manifest {
		res.Manifest = append(res.Manifest, &daemonv1.ManifestEntry{Id: wireMessageID(m.ID), From: m.From, To: m.To, Kind: m.Kind, Time: m.Time, ReplyTo: m.ReplyTo, Path: m.Path})
	}
	return res, nil
}

func (s *messageService) Ack(ctx context.Context, req *daemonv1.AckRequest) (*daemonv1.AckResponse, error) {
	if req.GetDryRun() {
		return s.previewAck(ctx, req.GetRoute().GetValue(), req.GetId().GetValue())
	}
	var out AckResult
	if e := s.d.ack(ctx, req.GetRoute().GetValue(), req.GetClaim(), AckRequest{ID: req.GetId().GetValue(), Record: req.GetRecord()}, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.AckResponse{Version: s.contract(), Id: wireMessageID(out.ID), Seq: out.Seq, Already: out.Already}, nil
}

func (s *messageService) Release(ctx context.Context, req *daemonv1.ReleaseRequest) (*daemonv1.ReleaseResponse, error) {
	if req.GetDryRun() {
		return s.previewRelease(ctx, req.GetRoute().GetValue(), req.GetClaim())
	}
	if e := s.d.release(ctx, req.GetRoute().GetValue(), req.GetClaim()); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.ReleaseResponse{Version: s.contract(), Released: true}, nil
}

// holdsToProto is a hold's owners as the reply carries them.
func holdsToProto(holds map[string]HoldOwner) map[string]*daemonv1.HoldOwner {
	out := make(map[string]*daemonv1.HoldOwner, len(holds))
	for owner, h := range holds {
		out[owner] = &daemonv1.HoldOwner{Reason: h.Reason, Time: h.Time}
	}
	return out
}

func (s *messageService) Hold(ctx context.Context, req *daemonv1.HoldRequest) (*daemonv1.HoldResponse, error) {
	if req.GetDryRun() {
		return s.previewHold(ctx, req.GetRoute().GetValue(), req.GetTarget())
	}
	var out HoldResult
	reason := req.GetReason()
	if e := s.d.holdOp(ctx, req.GetRoute().GetValue(), req.GetTarget(), &reason, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.HoldResponse{Version: s.contract(), Holds: holdsToProto(out.Holds)}, nil
}

func (s *messageService) Unhold(ctx context.Context, req *daemonv1.UnholdRequest) (*daemonv1.UnholdResponse, error) {
	if req.GetDryRun() {
		return s.previewUnhold(ctx, req.GetRoute().GetValue(), req.GetTarget())
	}
	var out HoldResult
	if e := s.d.holdOp(ctx, req.GetRoute().GetValue(), req.GetTarget(), nil, &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.UnholdResponse{Version: s.contract(), Holds: holdsToProto(out.Holds)}, nil
}

func (s *messageService) Authority(ctx context.Context, req *daemonv1.AuthorityRequest) (*daemonv1.AuthorityResponse, error) {
	var out AuthorityResult
	if e := s.d.authority(ctx, req.GetRoute().GetValue(), &out); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return &daemonv1.AuthorityResponse{Version: s.contract(), Authority: out.Authority, Reasons: out.Reasons}, nil
}

func (s *messageService) DaemonStatus(ctx context.Context, _ *daemonv1.DaemonStatusRequest) (*daemonv1.DaemonStatusResponse, error) {
	st, err := s.d.collectStatus()
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "status: %v", err))
	}
	return &daemonv1.DaemonStatusResponse{Version: s.contract(), StateDir: st.StateDir, Inspect: st.Inspect,
		SchemaCurrent: int32(st.Schema.Current), SchemaExpected: int32(st.Schema.Expected),
		RouteCount: int32(st.Counts.Routes), TaskCount: int32(st.Counts.Tasks), MessageCount: int32(st.Counts.Messages), Digest: st.Digest}, nil
}

func (s *messageService) ListOperations(ctx context.Context, _ *daemonv1.ListOperationsRequest) (*daemonv1.ListOperationsResponse, error) {
	list, e := s.d.listOps()
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	res := &daemonv1.ListOperationsResponse{Version: s.contract()}
	for _, op := range list {
		b, _ := encode(op)
		res.OperationsJson = append(res.OperationsJson, string(b))
	}
	return res, nil
}

func (s *messageService) GetOperation(ctx context.Context, req *daemonv1.GetOperationRequest) (*daemonv1.GetOperationResponse, error) {
	op, e := s.d.getOp(req.GetId())
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	b, _ := encode(op)
	return &daemonv1.GetOperationResponse{Version: s.contract(), OperationJson: string(b)}, nil
}

func (s *messageService) ResolveOperation(ctx context.Context, req *daemonv1.ResolveOperationRequest) (*daemonv1.ResolveOperationResponse, error) {
	if req.GetDryRun() {
		return s.previewResolveOperation(ctx, req)
	}
	op, e := s.d.resolveOp(req.GetId(), int(req.GetStep()), ResolveRequest{Decision: req.GetDecision()})
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	b, _ := encode(op)
	return &daemonv1.ResolveOperationResponse{Version: s.contract(), OperationJson: string(b)}, nil
}

// Bounds of one Wait reply and of how long one call may wait.
const (
	maxWaitFrames = 64
	maxWaitBytes  = 2 << 20
	maxWaitTime   = 30 * time.Second
)

// waitBatch gathers one Wait reply.
type waitBatch struct {
	route  string
	frames []*daemonv1.EventFrame
	size   int
	// done is set by a frame that ends the party's events.
	done bool
}

func (b *waitBatch) full() bool {
	return b.done || len(b.frames) >= maxWaitFrames || b.size >= maxWaitBytes
}

func (b *waitBatch) add(f frame) {
	out := eventFrame(b.route, f)
	b.frames = append(b.frames, out)
	b.size += len(out.Payload)
	b.done = b.done || f.last
}

// drain adds what is already queued, without waiting. It reports false when the
// queue is closed.
func (b *waitBatch) drain(s *subscriber) bool {
	for !b.full() {
		select {
		case f, ok := <-s.q:
			if !ok {
				return false
			}
			b.add(f)
		default:
			return true
		}
	}
	return true
}

// Wait answers with the party's events after the cursor, as soon as there are
// any. Each call subscribes at the route's visible sequence, so nothing is
// missed between calls: the next call resumes from the last cursor.
func (s *messageService) Wait(ctx context.Context, req *daemonv1.WaitRequest) (*daemonv1.WaitResponse, error) {
	d := s.d
	rt, e := d.route(req.GetRoute().GetValue())
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	me, e := d.authParty(ctx, rt)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	done, e := d.streamSlot(rt.name + "/" + me)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	defer done()
	sub := newSubscriber(rt.name, me)
	var first []frame
	var barrier string
	if e := rt.do(ctx, func() *APIError {
		var e *APIError
		first, e = rt.subscribe(sub, req.GetCursor())
		barrier = rt.epoch + "-" + strconv.FormatInt(rt.visible, 10)
		return e
	}); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	defer func() {
		sub.close()
		rt.detach(sub)
		sub.replay.close()
	}()
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok {
		releaseSlot(r)
	}
	res := &daemonv1.WaitResponse{Version: s.contract()}
	batch := &waitBatch{route: rt.name}
	for _, f := range first {
		batch.add(f)
	}
	for sub.replay != nil && !batch.full() {
		rec, err := sub.replay.next()
		if err != nil {
			return nil, rpcFailure(ctx, Errf("internal", "events: %v", err))
		}
		if rec == nil {
			break
		}
		if sub.wants(rec) {
			batch.add(frame{rec: rec, route: rt.name})
		}
	}
	open := batch.drain(sub)
	if len(batch.frames) == 0 && open {
		timer := time.NewTimer(min(time.Duration(req.GetWaitMs())*time.Millisecond, maxWaitTime))
		defer timer.Stop()
		select {
		case f, ok := <-sub.q:
			open = ok
			if ok {
				batch.add(f)
				batch.drain(sub)
			}
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if len(batch.frames) == 0 && !open {
		return nil, rpcFailure(ctx, Errf("busy", "the event queue closed; call again"))
	}
	res.Frames = batch.frames
	if len(batch.frames) == 0 {
		res.NextCursor = barrier
	}
	return res, nil
}

// rpcCode is the wire code of a daemon error. The daemon's own code travels
// beside it as a detail.
func rpcCode(code string) rpc.Code {
	switch code {
	case "bad_request", "version_malformed":
		return rpc.InvalidArgument
	case "unauthorized":
		return rpc.Unauthenticated
	case "identity_mismatch", "wrong_route":
		return rpc.PermissionDenied
	case "unknown_route", "unknown_name", "unknown_target", "no_hold", "not_found":
		return rpc.NotFound
	case "route_exists", "key_conflict", "role_taken":
		return rpc.AlreadyExists
	case "ack_too_large", "backlog_full", "budget_full":
		return rpc.ResourceExhausted
	case "busy", "route_unowned", "herdr_unreachable", "identity_unavailable", "key_window_full", "adopt_busy":
		return rpc.Unavailable
	case "internal":
		return rpc.Internal
	}
	if _, known := errorTable[code]; known {
		return rpc.FailedPrecondition
	}
	return rpc.Unknown
}

// rpcFailure turns a daemon error into a wire failure. When the call's own
// deadline or cancellation caused it, that is the failure to report.
func rpcFailure(ctx context.Context, e *APIError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	failure := rpc.Errorf(rpcCode(e.Code), "%s", e.Message)
	if err := failure.AddDetail(&corev1.Error{Code: e.Code, Message: e.Message}); err != nil {
		return rpc.Errorf(rpc.Internal, "cannot encode the failure")
	}
	if e.Update != "" {
		if err := failure.AddDetail(&daemonv1.VersionRefusal{Update: e.Update}); err != nil {
			return rpc.Errorf(rpc.Internal, "cannot encode the failure")
		}
	}
	return failure
}

// statusToProto is a route status as its contract carries it.
func statusToProto(st RouteStatus) *daemonv1.StatusResponse {
	res := &daemonv1.StatusResponse{
		Version:  &corev1.Version{Contract: version.Version, Build: st.Daemon.Version},
		Daemon:   &daemonv1.DaemonState{State: st.Daemon.State, Version: st.Daemon.Version, HeartbeatAge: st.Daemon.HeartbeatAge},
		Route:    &corev1.RouteId{Value: st.Route},
		Inbox:    st.Inbox,
		Epoch:    st.Epoch,
		LastSeq:  st.LastSeq,
		Status:   st.Status,
		Flags:    st.Flags,
		Registry: &daemonv1.RegistryState{State: st.Registry.State, PendingSha256: st.Registry.PendingSHA256},
	}
	for _, p := range st.Peers {
		res.Peers = append(res.Peers, &daemonv1.PeerInfo{Name: p.Name, Role: p.Role, Pane: p.Pane, Session: p.Session})
	}
	for _, b := range st.Boxes {
		res.Boxes = append(res.Boxes, &daemonv1.BoxCount{Name: b.Name, New: int32(b.New), Ready: int32(b.Ready), Cur: int32(b.Cur),
			Unacked: int32(b.Unacked), Quarantine: int32(b.Quarantine), Claim: b.Claim, Orphan: b.Orphan})
	}
	for _, h := range st.Holds {
		res.Holds = append(res.Holds, &daemonv1.HoldInfo{Target: h.Target, Owners: h.Owners})
	}
	for _, m := range st.Messages {
		res.Messages = append(res.Messages, &corev1.MessageHeader{Box: m.Box, Id: &corev1.MessageId{Value: m.ID}, From: m.From, To: m.To,
			Kind: m.Kind, ReplyTo: &corev1.MessageId{Value: m.ReplyTo}, State: m.State, Acknowledged: m.Acknowledged})
	}
	for _, a := range st.Alerts {
		res.Alerts = append(res.Alerts, string(a))
	}
	return res
}
