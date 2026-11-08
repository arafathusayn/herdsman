package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// partyContext is a service call's context carrying a party's identity.
func partyContext(ctx context.Context, p *party) context.Context {
	r, _ := http.NewRequest("POST", "/", nil)
	r.Header.Set(HeaderPane, p.pane)
	r.Header.Set(HeaderName, p.name)
	r.Header.Set("Authorization", "Bearer "+p.c.Credential)
	return context.WithValue(ctx, requestKey{}, r.WithContext(ctx))
}

func TestWaitAnswersEmptyWithAResumeCursor(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	svc := &messageService{d: h.d}
	start := time.Now()
	res, err := svc.Wait(partyContext(context.Background(), orch), &daemonv1.WaitRequest{Route: routeID("r"), Cursor: cursor, WaitMs: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetFrames()) != 0 || res.GetNextCursor() != cursor || time.Since(start) < 20*time.Millisecond {
		t.Fatalf("%v after %v", res, time.Since(start))
	}
}

func TestWaitWakesOnALiveEvent(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	svc := &messageService{d: h.d}
	got := make(chan *daemonv1.WaitResponse, 1)
	go func() {
		res, err := svc.Wait(partyContext(context.Background(), orch), &daemonv1.WaitRequest{Route: routeID("r"), Cursor: cursor, WaitMs: 10000})
		if err != nil {
			t.Error(err)
		}
		got <- res
	}()
	rt, _ := h.d.route("r")
	for i := 0; subCount(rt) == 0; i++ {
		if i == 500 {
			t.Fatal("the call never attached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m := adj.send(t, "r", orch.name, "x")
	select {
	case res := <-got:
		f := res.GetFrames()
		if len(f) == 0 || f[0].GetType() != "message" || !strings.Contains(f[0].GetPayload(), m.ID) || f[0].GetCursor() == "" || res.GetNextCursor() != "" {
			t.Fatalf("%v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not wake")
	}
}

func TestWaitCapsTheWaitAtTheMaximum(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	ctx, cancel := context.WithTimeout(partyContext(context.Background(), orch), 30*time.Millisecond)
	defer cancel()
	cursor := mustCursor(t, h, orch)
	_, err := (&messageService{d: h.d}).Wait(ctx, &daemonv1.WaitRequest{Route: routeID("r"), Cursor: cursor, WaitMs: 1 << 30})
	if err != context.DeadlineExceeded {
		t.Fatalf("error %v", err)
	}
}

func TestWaitRefusesABadCursorAndAStranger(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	svc := &messageService{d: h.d}
	if _, err := svc.Wait(partyContext(context.Background(), orch), &daemonv1.WaitRequest{Route: routeID("r"), Cursor: "nope"}); code(callError(err)) != "bad_request" {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, err := svc.Wait(context.Background(), &daemonv1.WaitRequest{Route: routeID("r")}); code(callError(err)) != "unauthorized" {
		t.Fatalf("no party: %v", err)
	}
	if _, err := svc.Wait(partyContext(context.Background(), adj), &daemonv1.WaitRequest{Route: routeID("zz")}); code(callError(err)) != "unknown_route" {
		t.Fatalf("unknown route: %v", err)
	}
}

// TestRouteRemovedBetweenCalls: a route removed while no call is open ends the
// reader as the daemon's own event would.
func TestRouteRemovedBetweenCalls(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	ev, err := orch.c.Events(context.Background(), "r", mustCursor(t, h, orch))
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	h.do("DELETE", "/v1/routes/r", nil, nil)
	f, err := ev.Next()
	if err != nil || f.Event != "route_removed" {
		t.Fatalf("%+v %v", f, err)
	}
}

// TestReaderStopsOnAFailedPoll: any other refusal reaches the caller.
func TestReaderStopsOnAFailedPoll(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	ev, err := orch.c.Events(context.Background(), "r", mustCursor(t, h, orch))
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	ev.cursor = "nope"
	if _, err := ev.Next(); code(err) != "bad_request" {
		t.Fatalf("error %v", err)
	}
}

// TestHelloFromAnOlderDaemon: a daemon that answers but has no RPC is told apart
// from one that is down, with the same advice as any older daemon.
func TestHelloFromAnOlderDaemon(t *testing.T) {
	ph := fakeDaemonWithout(t)
	c := NewClient(SocketPath(ph), testVersion)
	defer c.Close()
	err := c.Check(context.Background())
	a, ok := AsAPIError(err)
	if !ok || a.Code != "version_mismatch" || a.Update != "daemon" || !strings.Contains(a.Message, "herdsman daemon restart") {
		t.Fatalf("error %v", err)
	}
}

// TestHelloFailuresSort: a refused hello keeps its code, and an answer that is
// not a hello is reported as unreadable.
func TestHelloFailuresSort(t *testing.T) {
	busy := rpcFailure(context.Background(), Errf("busy", "full"))
	if a, ok := AsAPIError(helloError(busy)); !ok || a.Code != "busy" {
		t.Fatalf("busy: %v", a)
	}
	late := rpc.Errorf(rpc.DeadlineExceeded, "late")
	if !IsDown(helloError(late)) {
		t.Fatal("a late hello is not down")
	}
	if !IsDown(helloError(context.Canceled)) {
		t.Fatal("a canceled hello is not down")
	}
	if a, ok := AsAPIError(helloError(errors.New("proto: bad"))); !ok || a.Code != "version_malformed" {
		t.Fatalf("unreadable: %v", a)
	}
}

// TestSemaphoreRefusesInTheRPCShape: at the request limit a call is refused with
// a retryable failure a client can read, before its body is read.
func TestSemaphoreRefusesInTheRPCShape(t *testing.T) {
	h := newHarness(t)
	for range cap(h.d.sem) {
		h.d.sem <- struct{}{}
	}
	defer func() {
		for range cap(h.d.sem) {
			<-h.d.sem
		}
	}()
	c := h.client()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Hello(ctx)
	if a, ok := AsAPIError(err); !ok || a.Code != "busy" || !a.Retryable {
		t.Fatalf("error %v", err)
	}
}

func TestBodyOverTheBoundIsABadRequest(t *testing.T) {
	err := callError(rpc.Errorf(rpc.ResourceExhausted, "too big"))
	if a, ok := AsAPIError(err); !ok || a.Code != "bad_request" {
		t.Fatalf("error %v", err)
	}
}

func TestVersionRefusalKeepsTheSideToUpdate(t *testing.T) {
	e := versionErr("version_mismatch", "daemon", "restart it")
	a, ok := AsAPIError(callError(rpcFailure(context.Background(), e)))
	if !ok || a.Update != "daemon" {
		t.Fatalf("%+v", a)
	}
}

// TestClientMethodsReportFailures: every typed call returns the daemon's refusal
// rather than a half-filled answer.
func TestClientMethodsReportFailures(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	ctx := context.Background()
	const missing = "zz"
	calls := map[string]func() error{
		"AdoptRoute":  func() error { _, err := c.AdoptRoute(ctx, "../bad", AdoptRequest{}); return err },
		"ResetEvents": func() error { _, err := c.ResetEvents(ctx, missing, ResetRequest{ConfirmRoute: missing}); return err },
		"CreateRoute": func() error { _, err := c.CreateRoute(ctx, CreateRequest{Repo: "../x", Route: "y"}); return err },
		"GetSetup":    func() error { _, err := c.GetSetup(ctx, missing); return err },
		"PutSetup": func() error {
			_, err := c.PutSetup(ctx, missing, SetupRequest{Probes: []Probe{{Identity: json.RawMessage(`{}`), RecordedBy: "h"}}})
			return err
		},
		"Enroll":           func() error { _, err := c.Enroll(ctx, missing, EnrollRequest{}); return err },
		"ApprovePair":      func() error { _, err := c.ApprovePair(ctx, missing, ApproveRequest{}); return err },
		"RetirePair":       func() error { return c.RetirePair(ctx, missing, RetireRequest{}) },
		"RevokeCredential": func() error { return c.RevokeCredential(ctx, missing, "n") },
		"Send":             func() error { _, err := c.Send(ctx, missing, SendRequest{}); return err },
		"Claim":            func() error { _, err := c.Claim(ctx, missing, ClaimRequest{}); return err },
		"Ack":              func() error { _, err := c.Ack(ctx, missing, "c", AckRequest{}); return err },
		"Release":          func() error { return c.Release(ctx, missing, "c") },
		"Hold":             func() error { _, err := c.Hold(ctx, missing, "t", "r"); return err },
		"Unhold":           func() error { _, err := c.Unhold(ctx, missing, "t"); return err },
		"Authority":        func() error { _, err := c.Authority(ctx, missing); return err },
		"Status":           func() error { _, err := c.Status(ctx, missing); return err },
		"RemoveRoute":      func() error { return c.RemoveRoute(ctx, missing) },
		"Shutdown":         func() error { return c.Shutdown(ctx, "nonsense") },
	}
	for name, fn := range calls {
		if _, ok := AsAPIError(fn()); !ok {
			t.Errorf("%s: no daemon refusal", name)
		}
	}
	h.stop()
	down := NewClient(c.Socket, testVersion)
	defer down.Close()
	for name, fn := range map[string]func() error{
		"Health":     func() error { _, err := down.Health(ctx); return err },
		"ListRoutes": func() error { _, err := down.ListRoutes(ctx); return err },
	} {
		if !IsDown(fn()) {
			t.Errorf("%s: not reported as down", name)
		}
	}
}

func TestGetSetupCarriesTheProof(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	inv, err := h.client().GetSetup(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Proof == nil || inv.Proof.Version != 2 || len(inv.Inventory) == 0 || len(inv.Inventory[0].Session) == 0 {
		t.Fatalf("%+v", inv)
	}
}

func TestGetSetupRefusesAnUnreadableProof(t *testing.T) {
	ph := fakeDaemon(t, func(m *http.ServeMux) {
		m.Handle(rpc.Path(daemonv1.MessageServiceName, "GetSetup"), rpc.NewMux(rpc.Unary(daemonv1.MessageServiceName, "GetSetup",
			func(context.Context, *daemonv1.GetSetupRequest) (*daemonv1.GetSetupResponse, error) {
				return &daemonv1.GetSetupResponse{ProofJson: "not json"}, nil
			})))
	})
	c := NewClient(SocketPath(ph), testVersion)
	defer c.Close()
	if _, err := c.GetSetup(context.Background(), "r"); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("error %v", err)
	}
}

func TestListRoutesReportsABrokenRegistry(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.ph, "daemon", "routes.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client().ListRoutes(context.Background()); code(err) != "internal" {
		t.Fatalf("error %v", err)
	}
}

func TestRPCFailureReportsAnUnencodableRefusal(t *testing.T) {
	err := rpcFailure(context.Background(), &APIError{Code: "busy", Message: "ok", Update: string([]byte{0xff})})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.Internal {
		t.Fatalf("error %v", err)
	}
}

// waitFrom runs one Wait for orch with no time to wait.
func waitFrom(h *harness, orch *party, cursor string) (*daemonv1.WaitResponse, error) {
	return (&messageService{d: h.d}).Wait(partyContext(context.Background(), orch), &daemonv1.WaitRequest{Route: routeID("r"), Cursor: cursor})
}

// TestWaitDrainsAndStopsAtTheBatchLimit: a backlog larger than one reply is
// split, and each reply's last cursor resumes the next.
func TestWaitDrainsAndStopsAtTheBatchLimit(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	for i := range maxWaitFrames + 3 {
		adj.send(t, "r", orch.name, "m"+string(rune('a'+i%26)))
	}
	res, err := waitFrom(h, orch, cursor)
	if err != nil || len(res.GetFrames()) != maxWaitFrames {
		t.Fatalf("%d frames, %v", len(res.GetFrames()), err)
	}
	next, err := waitFrom(h, orch, res.GetFrames()[maxWaitFrames-1].GetCursor())
	if err != nil || len(next.GetFrames()) != 3 {
		t.Fatalf("%d frames, %v", len(next.GetFrames()), err)
	}
}

// TestWaitDeliversWhatQueuedBehindALiveEvent: frames that arrive with the first
// one are in the same reply.
func TestWaitBatchDrainClosedQueue(t *testing.T) {
	s := newSubscriber("r", "orch")
	s.q <- frame{ev: &Event{Type: "health"}}
	s.close()
	b := &waitBatch{route: "r"}
	if b.drain(s) || len(b.frames) != 1 {
		t.Fatalf("%d frames", len(b.frames))
	}
	full := &waitBatch{route: "r", done: true}
	if !full.drain(newSubscriber("r", "orch")) {
		t.Fatal("a full batch reports a closed queue")
	}
}

// TestWaitRefusesWhenTheQueueClosesEmpty: a route that ends a call with nothing
// to deliver answers busy, so the client backs off instead of spinning.
func TestWaitRefusesWhenTheQueueClosesEmpty(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	rt, _ := h.d.route("r")
	errs := make(chan error, 1)
	go func() {
		_, err := (&messageService{d: h.d}).Wait(partyContext(context.Background(), orch), &daemonv1.WaitRequest{Route: routeID("r"), Cursor: cursor, WaitMs: 10000})
		errs <- err
	}()
	var s *subscriber
	for i := 0; s == nil; i++ {
		if i == 500 {
			t.Fatal("the call never attached")
		}
		time.Sleep(5 * time.Millisecond)
		rt.always(func() {
			for x := range rt.subs {
				s = x
			}
		})
	}
	s.close()
	if err := <-errs; code(callError(err)) != "busy" {
		t.Fatalf("error %v", err)
	}
}

// TestWaitReportsAnUnreadableJournal: a replay that cannot read the journal is
// a failure, not an empty answer.
func TestWaitReportsAnUnreadableJournal(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	adj.send(t, "r", orch.name, "x")
	rt, _ := h.d.route("r")
	rt.always(func() {
		rt.ring = nil
		p := filepath.Join(rt.j.dir, journalCurrent)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		if _, err := f.Write(make([]byte, 2*maxRecord+4096)); err != nil {
			t.Error(err)
		}
	})
	if _, err := waitFrom(h, orch, cursor); code(callError(err)) != "internal" {
		t.Fatalf("error %v", err)
	}
}

// TestWaitReplaysAFinishedRange: a cursor older than the ring is served from the
// journal, up to the end of its range.
func TestWaitReplaysAFinishedRange(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	cursor := mustCursor(t, h, orch)
	m := adj.send(t, "r", orch.name, "x")
	rt, _ := h.d.route("r")
	rt.always(func() { rt.ring = nil })
	res, err := waitFrom(h, orch, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if f := res.GetFrames(); len(f) != 1 || !strings.Contains(f[0].GetPayload(), m.ID) {
		t.Fatalf("%v", res)
	}
}

// TestWaitTextLines pins the lines the skill reads: callers parse the first
// word.
func TestWaitTextLines(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{Event{Type: "message", ID: "m1", From: "adj", Kind: "brief"}, "MESSAGE me m1 from adj kind brief cursor c: run herdsman message read"},
		{Event{Type: "receipt", ID: "m1", By: "orch"}, "RECEIPT m1 by orch cursor c"},
		{Event{Type: "notice", ID: "m1", State: "quarantined"}, "NOTICE m1 quarantined cursor c: inspect herdsman message status"},
		{Event{Type: "health", Flags: []string{"a", "b"}}, "HEALTH a,b cursor c"},
		{Event{Type: "health"}, "HEALTH ok cursor c"},
		{Event{Type: "pair", State: "pending"}, "PAIR pending cursor c"},
		{Event{Type: "question"}, "QUESTION cursor c"},
	}
	for _, c := range cases {
		if got := eventLine("me", "c", c.ev); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
