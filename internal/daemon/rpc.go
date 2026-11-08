package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"github.com/arafathusayn/herdsman/sdk/version"
)

// requestKey holds the HTTP request in a service call's context: authority is
// read from the request's headers, which a generated service method cannot see.
type requestKey struct{}

// rpcHandler serves the RPC procedures, under the same body bound as the 0.x
// routes.
func (d *Daemon) rpcHandler() http.Handler {
	mux := rpc.NewMux(d.rpcProcedures()...)
	return http.MaxBytesHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, r)))
	}), maxBody)
}

// messageService is the daemon's MessageService.
type messageService struct{ d *Daemon }

// Status serves the route status under the rules of the 0.x status route: a
// request that names a pane must be that pane's party.
func (s *messageService) Status(ctx context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
	name := req.GetRoute().GetValue()
	if name == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no route")
	}
	rt, e := s.d.route(name)
	if e != nil {
		return nil, rpcFailure(ctx, e)
	}
	if r, ok := ctx.Value(requestKey{}).(*http.Request); ok && r.Header.Get(HeaderPane) != "" {
		if _, e := s.d.authParty(r, rt); e != nil {
			return nil, rpcFailure(ctx, e)
		}
	}
	var st RouteStatus
	if e := rt.do(ctx, func() *APIError { st = rt.fullStatus(); return nil }); e != nil {
		return nil, rpcFailure(ctx, e)
	}
	return statusToProto(st), nil
}

// rpcCode is the wire code of a daemon error. The daemon's own code travels
// beside it as a detail.
func rpcCode(code string) rpc.Code {
	switch code {
	case "bad_request", "version_missing", "version_malformed":
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
	return failure
}

// StatusOverRPC reads a route's status through the SDK client, with the same
// version check and the same failures as the 0.x request.
func StatusOverRPC(ctx context.Context, c *Client, route string, out *RouteStatus) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	header := http.Header{}
	if c.Pane != "" {
		header.Set(HeaderPane, c.Pane)
	}
	if c.Name != "" {
		header.Set(HeaderName, c.Name)
	}
	if c.Credential != "" {
		header.Set("Authorization", "Bearer "+c.Credential)
	}
	sdk, err := client.Dial(client.Options{Socket: c.Socket, Header: header})
	if err != nil {
		return err
	}
	defer sdk.Close()
	res, err := sdk.Message.Status(ctx, &daemonv1.StatusRequest{Route: &corev1.RouteId{Value: route}})
	if err != nil {
		return statusCallError(err)
	}
	*out = statusFromProto(res)
	return nil
}

// statusCallError sorts a failed call the way the 0.x client does: a daemon
// refusal, a daemon that does not answer, or an unreadable answer.
func statusCallError(err error) error {
	var failure *rpc.Error
	if errors.As(err, &failure) {
		var detail corev1.Error
		switch {
		case failure.Detail(&detail):
			return Errf(detail.GetCode(), "%s", detail.GetMessage())
		case failure.Code == rpc.DeadlineExceeded || failure.Code == rpc.Canceled:
			return &DownError{Err: failure}
		}
		return Errf("internal", "%s", failure.Message)
	}
	var transport *url.Error
	if errors.As(err, &transport) {
		return &DownError{Err: err}
	}
	return fmt.Errorf("unreadable answer: %w", err)
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

// statusFromProto is the reverse. A repeated field has no null on the wire, and
// the 0.x output always prints a list, so every list is non-nil here.
func statusFromProto(res *daemonv1.StatusResponse) RouteStatus {
	st := RouteStatus{
		Daemon: DaemonState{State: res.GetDaemon().GetState(), Version: res.GetDaemon().GetVersion(), HeartbeatAge: res.GetDaemon().HeartbeatAge},
		StatusReport: StatusReport{
			Route: res.GetRoute().GetValue(), Inbox: res.GetInbox(), Epoch: res.GetEpoch(), LastSeq: res.GetLastSeq(), Status: res.GetStatus(),
			Flags:    append([]string{}, res.GetFlags()...),
			Registry: RegistryState{State: res.GetRegistry().GetState(), PendingSHA256: res.GetRegistry().GetPendingSha256()},
			Peers:    []PeerInfo{}, Boxes: []BoxCount{}, Holds: []HoldInfo{},
		},
		Messages: []MessageHeader{}, Alerts: []json.RawMessage{},
	}
	for _, p := range res.GetPeers() {
		st.Peers = append(st.Peers, PeerInfo{Name: p.GetName(), Role: p.GetRole(), Pane: p.GetPane(), Session: p.GetSession()})
	}
	for _, b := range res.GetBoxes() {
		st.Boxes = append(st.Boxes, BoxCount{Name: b.GetName(), New: int(b.GetNew()), Ready: int(b.GetReady()), Cur: int(b.GetCur()),
			Unacked: int(b.GetUnacked()), Quarantine: int(b.GetQuarantine()), Claim: b.GetClaim(), Orphan: b.GetOrphan()})
	}
	for _, h := range res.GetHolds() {
		st.Holds = append(st.Holds, HoldInfo{Target: h.GetTarget(), Owners: append([]string{}, h.GetOwners()...)})
	}
	for _, m := range res.GetMessages() {
		st.Messages = append(st.Messages, MessageHeader{Box: m.GetBox(), ID: m.GetId().GetValue(), From: m.GetFrom(), To: m.GetTo(),
			Kind: m.GetKind(), ReplyTo: m.GetReplyTo().GetValue(), State: m.GetState(), Acknowledged: m.GetAcknowledged()})
	}
	for _, a := range res.GetAlerts() {
		st.Alerts = append(st.Alerts, json.RawMessage(a))
	}
	return st
}
