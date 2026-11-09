package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/routedb"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func TestSDKFindsTheDaemonSocket(t *testing.T) {
	getenv := func(string) string { return "" }
	t.Setenv("HOME", "/somewhere")
	got, err := client.SocketPath(getenv)
	if err != nil {
		t.Fatal(err)
	}
	home, err := routedb.PrivateHome("", getenv)
	if err != nil || got != SocketPath(home) {
		t.Fatalf("sdk %q, daemon %q, %v", got, SocketPath(home), err)
	}
}

func dialHarness(t *testing.T, h *harness) *client.Client {
	t.Helper()
	sdk, err := client.Dial(client.Options{Socket: filepath.Join(h.ph, "run", "daemon.sock")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sdk.Close)
	return sdk
}

func TestRPCRequiresARoute(t *testing.T) {
	sdk := dialHarness(t, newHarness(t))
	_, err := sdk.Message.Status(context.Background(), &daemonv1.StatusRequest{})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.InvalidArgument {
		t.Fatalf("error %v", err)
	}
}

func TestRPCAnswersWithoutAParty(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	res, err := dialHarness(t, h).Message.Status(context.Background(), &daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetDaemon().GetState() != "up" || res.GetVersion().GetBuild() != testVersion || res.GetVersion().GetContract() == "" {
		t.Fatalf("response %v", res)
	}
}

func TestRPCUnknownRouteCarriesTheDaemonCode(t *testing.T) {
	sdk := dialHarness(t, newHarness(t))
	_, err := sdk.Message.Status(context.Background(), &daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "missing"}})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.NotFound {
		t.Fatalf("error %v", err)
	}
	var detail corev1.Error
	if !failure.Detail(&detail) || detail.GetCode() != "unknown_route" {
		t.Fatalf("detail %v", &detail)
	}
}

func TestRPCBodyBound(t *testing.T) {
	h := newHarness(t)
	rec := &responseRecorder{header: http.Header{}}
	body := bytes.NewReader(make([]byte, maxBody+1))
	req, err := http.NewRequest(http.MethodPost, rpc.Path(daemonv1.MessageServiceName, "Status"), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", rpc.ContentTypeProto)
	h.d.rpcHandler().ServeHTTP(rec, req)
	if rec.status != http.StatusTooManyRequests {
		t.Fatalf("status %d", rec.status)
	}
}

type responseRecorder struct {
	header http.Header
	status int
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) WriteHeader(status int)      { r.status = status }
func (r *responseRecorder) Write(b []byte) (int, error) { return len(b), nil }

func TestRPCCodeCoversTheDaemonTable(t *testing.T) {
	for code := range errorTable {
		if got := rpcCode(code); !got.Valid() || got == rpc.Unknown {
			t.Errorf("%s maps to %q", code, got)
		}
	}
	if got := rpcCode("never_heard_of"); got != rpc.Unknown {
		t.Errorf("an unlisted code maps to %q", got)
	}
}

func TestRPCFailurePrefersTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rpcFailure(ctx, Errf("busy", "x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

func TestRPCFailureReportsAnUnencodableDetail(t *testing.T) {
	err := rpcFailure(context.Background(), &APIError{Code: "busy", Message: string([]byte{0xff})})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.Internal {
		t.Fatalf("error %v", err)
	}
}

func TestCallErrorSorts(t *testing.T) {
	withDetail := rpc.Errorf(rpc.NotFound, "wire")
	if err := withDetail.AddDetail(&corev1.Error{Code: "unknown_route", Message: "the message"}); err != nil {
		t.Fatal(err)
	}
	wrapped := func(err error) error { return &url.Error{Op: "Post", URL: "x", Err: err} }
	cases := []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{"detail", wrapped(withDetail), func(e error) bool {
			a, ok := AsAPIError(e)
			return ok && a.Code == "unknown_route" && a.Message == "the message"
		}},
		{"deadline", wrapped(rpc.Errorf(rpc.DeadlineExceeded, "late")), IsDown},
		{"canceled", wrapped(rpc.Errorf(rpc.Canceled, "gone")), IsDown},
		{"no detail", wrapped(rpc.Errorf(rpc.Unavailable, "odd")), func(e error) bool {
			a, ok := AsAPIError(e)
			return ok && a.Code == "internal" && a.Message == "odd"
		}},
		{"transport", wrapped(errors.New("refused")), IsDown},
		{"unreadable", errors.New("proto: bad"), func(e error) bool {
			_, api := AsAPIError(e)
			return !api && !IsDown(e)
		}},
	}
	for _, c := range cases {
		if got := callError(c.err); !c.check(got) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestStatusStopsAtTheVersionCheck(t *testing.T) {
	h := newHarness(t)
	c := NewClient(filepath.Join(h.ph, "run", "daemon.sock"), "999.0.0")
	defer c.Close()
	_, err := c.Status(context.Background(), "r")
	if a, ok := AsAPIError(err); !ok || a.Code != "version_mismatch" {
		t.Fatalf("error %v", err)
	}
}

func TestRPCStatusReportsACanceledCall(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&messageService{d: h.d}).Status(ctx, &daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "r"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

func TestStatusProtoRoundTrip(t *testing.T) {
	age := int64(3)
	want := RouteStatus{
		Daemon: DaemonState{State: "up", Version: "1.2.3", HeartbeatAge: &age},
		StatusReport: StatusReport{Route: "r", Inbox: "i", Epoch: "e", LastSeq: 9, Status: "s", Flags: []string{"f"},
			Registry: RegistryState{State: "pending", PendingSHA256: "abc"},
			Peers:    []PeerInfo{{Name: "n", Role: "ro", Pane: "p", Session: "se"}},
			Boxes:    []BoxCount{{Name: "b", New: 1, Ready: 2, Cur: 3, Unacked: 4, Quarantine: 5, Claim: true, Orphan: true}},
			Holds:    []HoldInfo{{Target: "t", Owners: []string{"o"}}}},
		Messages: []MessageHeader{{Box: "b", ID: "1", From: "a", To: "c", Kind: "k", ReplyTo: "0", State: "new", Acknowledged: true}},
	}
	want.Alerts = append(want.Alerts, []byte(`{"a": 1}`))
	if got := statusFromProto(statusToProto(want)); fmt.Sprintf("%+v", deref(got)) != fmt.Sprintf("%+v", deref(want)) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// deref replaces the age pointer, which %+v would print as an address.
func deref(st RouteStatus) RouteStatus {
	if st.Daemon.HeartbeatAge != nil {
		age := *st.Daemon.HeartbeatAge
		st.Daemon.HeartbeatAge = nil
		st.Daemon.Version += fmt.Sprintf(" age=%d", age)
	}
	return st
}
