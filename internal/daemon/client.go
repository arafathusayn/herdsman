package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// Client is a client of the daemon socket: the CLI's message and daemon
// commands, and herdsman web.
type Client struct {
	Socket  string
	Version string
	// Party identity, for party requests (5.1).
	Pane       string
	Name       string
	Credential string

	sdk *client.Client
	// mu guards checked and inflight: concurrent first requests share one
	// hello, and none holds mu across the network.
	mu       sync.Mutex
	checked  bool
	inflight chan struct{}
}

// DownError is a daemon that does not answer on its socket.
type DownError struct{ Err error }

func (e *DownError) Error() string { return "the herdsman daemon does not answer: " + e.Err.Error() }

// NewClient makes a client of the daemon at socket.
func NewClient(socket, version string) *Client {
	c := &Client{Socket: socket, Version: version}
	// Dial can only fail while it looks for a socket, and this one is named.
	c.sdk, _ = client.Dial(client.Options{Socket: socket, CallHeader: c.identity})
	return c
}

// Close frees the client's idle connections.
func (c *Client) Close() { c.sdk.Close() }

// identity is the headers of one call: this client's version and, as they stand
// at the call, its party's pane, name and credential.
func (c *Client) identity(context.Context) http.Header {
	header := http.Header{}
	header.Set(HeaderVersion, c.Version)
	if c.Pane != "" {
		header.Set(HeaderPane, c.Pane)
	}
	if c.Name != "" {
		header.Set(HeaderName, c.Name)
	}
	if c.Credential != "" {
		header.Set("Authorization", "Bearer "+c.Credential)
	}
	return header
}

// service is the SDK's client of the daemon's MessageService.
func (c *Client) service() daemonv1.MessageService { return c.sdk.Message }

// Hello asks the daemon its version; it always answers (5.6).
func (c *Client) Hello(ctx context.Context) (Hello, error) {
	res, err := c.service().Hello(ctx, &daemonv1.HelloRequest{})
	if err != nil {
		return Hello{}, helloError(err)
	}
	return Hello{DaemonVersion: res.GetDaemonVersion(), Features: listOf(res.GetFeatures()),
		CredentialOptions: listOf(res.GetCredentialOptions()), DefaultCredential: res.GetDefaultCredential()}, nil
}

// helloError sorts a failed hello. A daemon that answers but has no such method
// is an older daemon, so the fix is the same as for any older daemon.
func helloError(err error) error {
	var failure *rpc.Error
	if errors.As(err, &failure) && failure.Code != rpc.DeadlineExceeded && failure.Code != rpc.Canceled && !failure.Detail(&corev1.Error{}) {
		return versionErr("version_mismatch", "daemon", "The herdsman daemon is older than this client and does not answer its hello. Restart it to load the installed binary: herdsman daemon restart.")
	}
	mapped := callError(err)
	if _, isAPI := AsAPIError(mapped); !isAPI && !IsDown(mapped) {
		return Errf("version_malformed", "the daemon's hello is not readable")
	}
	return mapped
}

// Check is the client's version check before its first request (5.6).
// One caller asks; the others wait for its answer or their own context,
// and ask themselves when it failed.
func (c *Client) Check(ctx context.Context) error {
	for {
		c.mu.Lock()
		if c.checked {
			c.mu.Unlock()
			return nil
		}
		if ch := c.inflight; ch != nil {
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return &DownError{Err: ctx.Err()}
			}
		}
		ch := make(chan struct{})
		c.inflight = ch
		c.mu.Unlock()
		err := c.hello(ctx)
		c.mu.Lock()
		c.inflight, c.checked = nil, err == nil
		c.mu.Unlock()
		close(ch)
		return err
	}
}

// hello asks the daemon's version and checks it against this client's.
func (c *Client) hello(ctx context.Context) error {
	h, err := c.Hello(ctx)
	if err != nil {
		return err
	}
	if e := ClientCheck(c.Version, h); e != nil {
		return e
	}
	return nil
}

// callError sorts a failed call: a daemon refusal, a daemon that does not
// answer, or an unreadable answer.
func callError(err error) error {
	var failure *rpc.Error
	if errors.As(err, &failure) {
		var detail corev1.Error
		switch {
		case failure.Detail(&detail):
			e := Errf(detail.GetCode(), "%s", detail.GetMessage())
			var refusal daemonv1.VersionRefusal
			if failure.Detail(&refusal) {
				e.Update = refusal.GetUpdate()
			}
			return e
		case failure.Code == rpc.DeadlineExceeded || failure.Code == rpc.Canceled:
			return &DownError{Err: failure}
		case failure.Code == rpc.ResourceExhausted:
			return Errf("bad_request", "%s", failure.Message)
		}
		return Errf("internal", "%s", failure.Message)
	}
	var transport *url.Error
	if errors.As(err, &transport) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &DownError{Err: err}
	}
	return fmt.Errorf("unreadable answer: %w", err)
}

// call makes one version-checked call and sorts its failure.
func call[Res any](ctx context.Context, c *Client, fn func(daemonv1.MessageService) (Res, error)) (Res, error) {
	var zero Res
	if err := c.Check(ctx); err != nil {
		return zero, err
	}
	res, err := fn(c.service())
	if err != nil {
		return zero, callError(err)
	}
	return res, nil
}

// WaitFrame is one event of a party's Wait.
type WaitFrame struct {
	ID    string
	Event string
	Data  []byte
}

// waitPoll is how long one Wait call of an open reader waits. An idle reader
// calls again, so it only limits how long a call stays open.
var waitPoll = 25 * time.Second

// EventReader reads one party's events, one Wait call at a time.
type EventReader struct {
	c      *Client
	ctx    context.Context
	cancel context.CancelFunc
	route  string
	cursor string
	frames []*daemonv1.EventFrame
	// ended is set once the route is gone: there are no more events.
	ended bool
}

// Close ends the reader and any call it has open.
func (r *EventReader) Close() { r.cancel() }

// poll makes one Wait call and takes its frames.
func (r *EventReader) poll(wait time.Duration) error {
	res, err := r.c.service().Wait(r.ctx, &daemonv1.WaitRequest{Route: routeID(r.route), Cursor: r.cursor, WaitMs: int32(wait / time.Millisecond)})
	if err != nil {
		return callError(err)
	}
	r.frames = res.GetFrames()
	if len(r.frames) == 0 && res.GetNextCursor() != "" {
		r.cursor = res.GetNextCursor()
	}
	return nil
}

// Next is the next event. A route that disappears between two calls ends the
// reader the way the daemon's own route_removed event does.
func (r *EventReader) Next() (WaitFrame, error) {
	if r.ended {
		return WaitFrame{}, io.EOF
	}
	for len(r.frames) == 0 {
		if err := r.poll(waitPoll); err != nil {
			if a, ok := AsAPIError(err); ok && a.Code == "unknown_route" {
				r.ended = true
				return WaitFrame{Event: "route_removed", Data: []byte("{}")}, nil
			}
			return WaitFrame{}, err
		}
	}
	f := r.frames[0]
	r.frames = r.frames[1:]
	if f.GetCursor() != "" {
		r.cursor = f.GetCursor()
	}
	r.ended = f.GetType() == "route_removed"
	return WaitFrame{ID: f.GetCursor(), Event: f.GetType(), Data: []byte(f.GetPayload())}, nil
}

// Events opens a party's events on a route, after an optional cursor. The first
// call does not wait, so a refusal or a daemon that is down shows here.
func (c *Client) Events(ctx context.Context, route, cursor string) (*EventReader, error) {
	if err := c.Check(ctx); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &EventReader{c: c, ctx: rctx, cancel: cancel, route: route, cursor: cursor}
	if err := r.poll(0); err != nil {
		cancel()
		return nil, err
	}
	return r, nil
}

// listOf is a repeated field as the command output prints it: a list, never
// null.
func listOf[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Health asks every route's and Herdr link's health.
func (c *Client) Health(ctx context.Context) (Health, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.HealthResponse, error) {
		return s.Health(ctx, &daemonv1.HealthRequest{})
	})
	if err != nil {
		return Health{}, err
	}
	h := Health{Status: res.GetStatus(), Routes: []RouteHealth{}, Herdr: []HerdrHealth{}}
	for _, r := range res.GetRoutes() {
		h.Routes = append(h.Routes, RouteHealth{Route: r.GetRoute().GetValue(), Status: r.GetStatus(), Flags: listOf(r.GetFlags()), Reason: r.GetReason()})
	}
	for _, x := range res.GetHerdr() {
		h.Herdr = append(h.Herdr, HerdrHealth{Socket: x.GetSocket(), Status: x.GetStatus()})
	}
	return h, nil
}

// Shutdown asks the daemon to stop.
func (c *Client) Shutdown(ctx context.Context, reason string) error {
	_, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ShutdownResponse, error) {
		return s.Shutdown(ctx, &daemonv1.ShutdownRequest{Reason: reason})
	})
	return err
}

// ListRoutes lists the registered routes.
func (c *Client) ListRoutes(ctx context.Context) ([]RouteInfo, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ListRoutesResponse, error) {
		return s.ListRoutes(ctx, &daemonv1.ListRoutesRequest{})
	})
	if err != nil {
		return nil, err
	}
	out := []RouteInfo{}
	for _, r := range res.GetRoutes() {
		out = append(out, RouteInfo{Route: r.GetRoute().GetValue(), Inbox: r.GetInbox(), Owner: r.GetOwner(), Epoch: r.GetEpoch(), LastSeq: r.GetLastSeq(), Status: r.GetStatus()})
	}
	return out, nil
}

// CreateRoute registers a new route.
func (c *Client) CreateRoute(ctx context.Context, q CreateRequest) (CreateResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.CreateRouteResponse, error) {
		return s.CreateRoute(ctx, &daemonv1.CreateRouteRequest{Repo: q.Repo, Route: routeID(q.Route), HerdrSocket: q.HerdrSocket})
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Route: res.GetRoute().GetValue(), Inbox: res.GetInbox(), Epoch: res.GetEpoch()}, nil
}

// AdoptRoute moves a file inbox under the daemon.
func (c *Client) AdoptRoute(ctx context.Context, route string, q AdoptRequest) (AdoptResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.AdoptRouteResponse, error) {
		return s.AdoptRoute(ctx, &daemonv1.AdoptRouteRequest{Route: routeID(route), Inbox: q.Inbox, ConfirmPairSha256: q.ConfirmPairSHA256})
	})
	if err != nil {
		return AdoptResult{}, err
	}
	return AdoptResult{Route: res.GetRoute().GetValue(), Epoch: res.GetEpoch(), LastMessageID: res.GetLastMessageId(), LegacyProof: res.GetLegacyProof()}, nil
}

// RemoveRoute forgets a route and keeps its inbox.
func (c *Client) RemoveRoute(ctx context.Context, route string) error {
	_, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.RemoveRouteResponse, error) {
		return s.RemoveRoute(ctx, &daemonv1.RemoveRouteRequest{Route: routeID(route)})
	})
	return err
}

// ResetEvents starts a new event epoch.
func (c *Client) ResetEvents(ctx context.Context, route string, q ResetRequest) (ResetResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ResetEventsResponse, error) {
		return s.ResetEvents(ctx, &daemonv1.ResetEventsRequest{Route: routeID(route), ConfirmRoute: q.ConfirmRoute})
	})
	if err != nil {
		return ResetResult{}, err
	}
	return ResetResult{Epoch: res.GetEpoch(), CheckpointSeq: res.GetCheckpointSeq()}, nil
}

// Status reads a route's status.
func (c *Client) Status(ctx context.Context, route string) (RouteStatus, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.StatusResponse, error) {
		return s.Status(ctx, &daemonv1.StatusRequest{Route: routeID(route)})
	})
	if err != nil {
		return RouteStatus{}, err
	}
	return statusFromProto(res), nil
}

// GetSetup lists a route's live agents and its proof.
func (c *Client) GetSetup(ctx context.Context, route string) (SetupInventory, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.GetSetupResponse, error) {
		return s.GetSetup(ctx, &daemonv1.GetSetupRequest{Route: routeID(route)})
	})
	if err != nil {
		return SetupInventory{}, err
	}
	out := SetupInventory{Inventory: []InventoryAgent{}}
	for _, a := range res.GetInventory() {
		out.Inventory = append(out.Inventory, InventoryAgent{Pane: a.GetPane(), Session: rawJSON(a.GetSessionJson()), Agent: a.GetAgent()})
	}
	if res.GetProofJson() != "" {
		var doc SetupV2
		if err := json.Unmarshal([]byte(res.GetProofJson()), &doc); err != nil {
			return SetupInventory{}, fmt.Errorf("unreadable answer: %w", err)
		}
		out.Proof = &doc
	}
	return out, nil
}

// PutSetup records the human's setup proof.
func (c *Client) PutSetup(ctx context.Context, route string, q SetupRequest) (SetupResult, error) {
	req := &daemonv1.PutSetupRequest{Route: routeID(route), Rules: q.Rules, Binary: q.Binary, PolicyVerified: q.PolicyVerified,
		BoundaryJson: string(q.Boundary), WatchTargetsJson: string(q.WatchTargets), ResumeTargetsJson: string(q.ResumeTargets)}
	for _, p := range q.Probes {
		req.Probes = append(req.Probes, &daemonv1.Probe{IdentityJson: string(p.Identity), InboxWriteDenied: p.InboxWriteDenied,
			SocketConnectDenied: p.SocketConnectDenied, RecordedBy: p.RecordedBy})
	}
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.PutSetupResponse, error) { return s.PutSetup(ctx, req) })
	if err != nil {
		return SetupResult{}, err
	}
	return SetupResult{ProofSHA256: res.GetProofSha256()}, nil
}

// Enroll proposes this pane as a party and returns the pending checksum.
func (c *Client) Enroll(ctx context.Context, route string, q EnrollRequest) (string, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.EnrollResponse, error) {
		return s.Enroll(ctx, &daemonv1.EnrollRequest{Route: routeID(route), Name: q.Name, Role: q.Role})
	})
	if err != nil {
		return "", err
	}
	return res.GetPendingSha256(), nil
}

// ApprovePair approves the pending registry.
func (c *Client) ApprovePair(ctx context.Context, route string, q ApproveRequest) (ApproveResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ApprovePairResponse, error) {
		return s.ApprovePair(ctx, &daemonv1.ApprovePairRequest{Route: routeID(route), Sha256: q.SHA256, Interaction: q.Interaction, AskedBy: q.AskedBy, Credentials: q.Credentials})
	})
	if err != nil {
		return ApproveResult{}, err
	}
	out := ApproveResult{Approved: res.GetApproved(), Credentials: []ApprovedCredential{}, Revoked: listOf(res.GetRevoked())}
	for _, x := range res.GetCredentials() {
		out.Credentials = append(out.Credentials, ApprovedCredential{Name: x.GetName(), Option: x.GetOption()})
	}
	return out, nil
}

// RetirePair retires a party.
func (c *Client) RetirePair(ctx context.Context, route string, q RetireRequest) error {
	_, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.RetirePairResponse, error) {
		return s.RetirePair(ctx, &daemonv1.RetirePairRequest{Route: routeID(route), Name: q.Name, Sha256: q.SHA256})
	})
	return err
}

// RevokeCredential revokes one party's credential.
func (c *Client) RevokeCredential(ctx context.Context, route, name string) error {
	_, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.RevokeCredentialResponse, error) {
		return s.RevokeCredential(ctx, &daemonv1.RevokeCredentialRequest{Route: routeID(route), Name: name})
	})
	return err
}

// Send queues a message.
func (c *Client) Send(ctx context.Context, route string, q SendRequest) (SendResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.SendResponse, error) {
		return s.Send(ctx, &daemonv1.SendRequest{Route: routeID(route), To: q.To, Kind: q.Kind, Body: q.Body, ReplyTo: q.ReplyTo, Key: q.Key})
	})
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{ID: res.GetId().GetValue(), Seq: res.GetSeq(), Duplicate: res.GetDuplicate()}, nil
}

// Claim reads ready messages or extends the active claim.
func (c *Client) Claim(ctx context.Context, route string, q ClaimRequest) (ClaimResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ClaimResponse, error) {
		return s.Claim(ctx, &daemonv1.ClaimRequest{Route: routeID(route), ReadKey: q.ReadKey, Token: q.Token, ExtendKey: q.ExtendKey})
	})
	if err != nil {
		return ClaimResult{}, err
	}
	out := ClaimResult{Claim: res.GetClaim(), Manifest: []ManifestEntry{}, Summary: ClaimSummary{PendingNew: int(res.GetPendingNew()), Quarantine: int(res.GetQuarantine())}}
	for _, m := range res.GetManifest() {
		out.Manifest = append(out.Manifest, ManifestEntry{ID: m.GetId().GetValue(), From: m.GetFrom(), To: m.GetTo(), Kind: m.GetKind(), Time: m.GetTime(), ReplyTo: m.GetReplyTo(), Path: m.GetPath()})
	}
	return out, nil
}

// Ack acknowledges one claimed message.
func (c *Client) Ack(ctx context.Context, route, claim string, q AckRequest) (AckResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.AckResponse, error) {
		return s.Ack(ctx, &daemonv1.AckRequest{Route: routeID(route), Claim: claim, Id: wireMessageID(q.ID), Record: q.Record})
	})
	if err != nil {
		return AckResult{}, err
	}
	return AckResult{ID: res.GetId().GetValue(), Seq: res.GetSeq(), Already: res.GetAlready()}, nil
}

// Release gives back a fully acknowledged claim.
func (c *Client) Release(ctx context.Context, route, claim string) error {
	_, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.ReleaseResponse, error) {
		return s.Release(ctx, &daemonv1.ReleaseRequest{Route: routeID(route), Claim: claim})
	})
	return err
}

// holdsFromProto is a hold's owners as the commands print them.
func holdsFromProto(holds map[string]*daemonv1.HoldOwner) HoldResult {
	out := HoldResult{Holds: make(map[string]HoldOwner, len(holds))}
	for owner, h := range holds {
		out.Holds[owner] = HoldOwner{Reason: h.GetReason(), Time: h.GetTime()}
	}
	return out
}

// Hold places this party's hold on a target.
func (c *Client) Hold(ctx context.Context, route, target, reason string) (HoldResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.HoldResponse, error) {
		return s.Hold(ctx, &daemonv1.HoldRequest{Route: routeID(route), Target: target, Reason: reason})
	})
	if err != nil {
		return HoldResult{}, err
	}
	return holdsFromProto(res.GetHolds()), nil
}

// Unhold removes this party's hold from a target.
func (c *Client) Unhold(ctx context.Context, route, target string) (HoldResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.UnholdResponse, error) {
		return s.Unhold(ctx, &daemonv1.UnholdRequest{Route: routeID(route), Target: target})
	})
	if err != nil {
		return HoldResult{}, err
	}
	return holdsFromProto(res.GetHolds()), nil
}

// Authority reads whether relayed messages carry authority.
func (c *Client) Authority(ctx context.Context, route string) (AuthorityResult, error) {
	res, err := call(ctx, c, func(s daemonv1.MessageService) (*daemonv1.AuthorityResponse, error) {
		return s.Authority(ctx, &daemonv1.AuthorityRequest{Route: routeID(route)})
	})
	if err != nil {
		return AuthorityResult{}, err
	}
	return AuthorityResult{Authority: res.GetAuthority(), Reasons: res.GetReasons()}, nil
}

// statusFromProto is a route status as the commands print it. A repeated field
// has no null on the wire, and the output always prints a list, so every list
// is non-nil here.
func statusFromProto(res *daemonv1.StatusResponse) RouteStatus {
	st := RouteStatus{
		Daemon: DaemonState{State: res.GetDaemon().GetState(), Version: res.GetDaemon().GetVersion(), HeartbeatAge: res.GetDaemon().HeartbeatAge},
		StatusReport: StatusReport{
			Route: res.GetRoute().GetValue(), Inbox: res.GetInbox(), Epoch: res.GetEpoch(), LastSeq: res.GetLastSeq(), Status: res.GetStatus(),
			Flags:    listOf(res.GetFlags()),
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
		st.Holds = append(st.Holds, HoldInfo{Target: h.GetTarget(), Owners: listOf(h.GetOwners())})
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

// IsDown reports whether err is a daemon that does not answer.
func IsDown(err error) bool {
	var d *DownError
	return errors.As(err, &d)
}

// AsAPIError returns err as a daemon error.
func AsAPIError(err error) (*APIError, bool) {
	var e *APIError
	ok := errors.As(err, &e)
	return e, ok
}
