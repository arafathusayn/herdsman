// Package daemon runs a stock Connect client against a real daemon on its
// socket. It starts the daemon from the workspace's own command, or uses the
// one at HERDSMAN_CONFORMANCE_SOCKET.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const startWait = 30 * time.Second

// startDaemon returns the socket of a daemon that lives as long as the test.
func startDaemon(t *testing.T) string {
	t.Helper()
	if socket := os.Getenv("HERDSMAN_CONFORMANCE_SOCKET"); socket != "" {
		return socket
	}
	// Socket paths have a short limit, and t.TempDir can exceed it.
	dir, err := os.MkdirTemp("", "hcd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	binary := filepath.Join(dir, "herdsman")
	build := exec.Command("go", "build", "-o", binary, "github.com/arafathusayn/herdsman/cmd/herdsman")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "h")
	private := filepath.Join(dir, "p")
	for _, path := range []string{home, private} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	daemon := exec.Command(binary, "daemon", "run")
	daemon.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "HERDSMAN_PRIVATE_HOME=" + private}
	logFile, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	daemon.Stdout, daemon.Stderr = logFile, logFile
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	})
	socket := filepath.Join(private, "run", "daemon.sock")
	for end := time.Now().Add(startWait); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if conn, err := net.Dial("unix", socket); err == nil {
			_ = conn.Close()
			return socket
		}
	}
	log, _ := os.ReadFile(logFile.Name())
	t.Fatalf("the daemon did not listen on its socket:\n%s", log)
	return ""
}

// procedure is the path of one MessageService method.
func procedure(method string) string {
	return "http://herdsman/rpc/" + daemonv1.MessageServiceName + "/" + method
}

// client reaches the daemon's socket with a stock HTTP client.
func httpClient(t *testing.T, socket string) *http.Client {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// call is one method called the stock way; it returns the wire code, or "" on
// success.
type call func(ctx context.Context, hc *http.Client, options []connect.ClientOption) (connect.Code, error)

func unary[Req, Res any](method string, req *Req, check func(*Res) error) call {
	return func(ctx context.Context, hc *http.Client, options []connect.ClientOption) (connect.Code, error) {
		res, err := connect.NewClient[Req, Res](hc, procedure(method), options...).CallUnary(ctx, connect.NewRequest(req))
		if err != nil {
			var failure *connect.Error
			if !errors.As(err, &failure) {
				return 0, err
			}
			return failure.Code(), nil
		}
		if check != nil {
			if err := check(res.Msg); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
}

func route(name string) *corev1.RouteId { return &corev1.RouteId{Value: name} }

func messageID(value string) *corev1.MessageId { return &corev1.MessageId{Value: value} }

// versioned checks that a reply carries the contract's Version.
func versioned[Res interface{ GetVersion() *corev1.Version }](res Res) error {
	if res.GetVersion().GetContract() == "" {
		return errors.New("the reply carries no version")
	}
	return nil
}

const (
	missing  = "no-such-route"
	okay     = connect.Code(0)
	badToken = "0000000000000000000000000000000000000000000000000000000000000000"
)

// scenario is one method's call and the outcome a daemon with no parties gives.
type scenario struct {
	method string
	call   call
	want   connect.Code
}

// scenarios covers every method on a route named name, in an order where each
// call finds the state it needs; RemoveRoute comes last.
func scenarios(name string) []scenario {
	return []scenario{
		{"Hello", unary("Hello", &daemonv1.HelloRequest{}, func(r *daemonv1.HelloResponse) error {
			if r.GetDaemonVersion() == "" {
				return errors.New("hello names no daemon version")
			}
			return versioned(r)
		}), okay},
		{"Health", unary("Health", &daemonv1.HealthRequest{}, versioned[*daemonv1.HealthResponse]), okay},
		{"Shutdown", unary[daemonv1.ShutdownRequest, daemonv1.ShutdownResponse]("Shutdown", &daemonv1.ShutdownRequest{Reason: "nonsense"}, nil), connect.CodeInvalidArgument},
		{"CreateRoute", unary("CreateRoute", &daemonv1.CreateRouteRequest{Repo: "repo", Route: route(name)}, func(r *daemonv1.CreateRouteResponse) error {
			if r.GetRoute().GetValue() != name || r.GetEpoch() == "" {
				return fmt.Errorf("created %v", r)
			}
			return versioned(r)
		}), okay},
		{"ListRoutes", unary("ListRoutes", &daemonv1.ListRoutesRequest{}, func(r *daemonv1.ListRoutesResponse) error {
			for _, info := range r.GetRoutes() {
				if info.GetRoute().GetValue() == name {
					return versioned(r)
				}
			}
			return fmt.Errorf("%s is not listed", name)
		}), okay},
		{"AdoptRoute", unary[daemonv1.AdoptRouteRequest, daemonv1.AdoptRouteResponse]("AdoptRoute", &daemonv1.AdoptRouteRequest{Route: route(name), Inbox: "relative", ConfirmPairSha256: badToken}, nil), connect.CodeAlreadyExists},
		{"ResetEvents", unary("ResetEvents", &daemonv1.ResetEventsRequest{Route: route(name), ConfirmRoute: name}, versioned[*daemonv1.ResetEventsResponse]), okay},
		{"Status", unary("Status", &daemonv1.StatusRequest{Route: route(name)}, func(r *daemonv1.StatusResponse) error {
			if r.GetRoute().GetValue() != name {
				return fmt.Errorf("status of %q", r.GetRoute().GetValue())
			}
			return versioned(r)
		}), okay},
		{"GetSetup", unary[daemonv1.GetSetupRequest, daemonv1.GetSetupResponse]("GetSetup", &daemonv1.GetSetupRequest{Route: route(missing)}, nil), connect.CodeNotFound},
		{"PutSetup", unary[daemonv1.PutSetupRequest, daemonv1.PutSetupResponse]("PutSetup", &daemonv1.PutSetupRequest{Route: route(missing)}, nil), connect.CodeNotFound},
		{"Enroll", unary[daemonv1.EnrollRequest, daemonv1.EnrollResponse]("Enroll", &daemonv1.EnrollRequest{Route: route(name), Name: "adj", Role: "adjutant"}, nil), connect.CodeUnauthenticated},
		{"ApprovePair", unary[daemonv1.ApprovePairRequest, daemonv1.ApprovePairResponse]("ApprovePair", &daemonv1.ApprovePairRequest{Route: route(missing), Sha256: badToken}, nil), connect.CodeNotFound},
		{"RetirePair", unary[daemonv1.RetirePairRequest, daemonv1.RetirePairResponse]("RetirePair", &daemonv1.RetirePairRequest{Route: route(missing), Name: "adj", Sha256: badToken}, nil), connect.CodeNotFound},
		{"RevokeCredential", unary[daemonv1.RevokeCredentialRequest, daemonv1.RevokeCredentialResponse]("RevokeCredential", &daemonv1.RevokeCredentialRequest{Route: route(name), Name: "nobody"}, nil), connect.CodeNotFound},
		{"Send", unary[daemonv1.SendRequest, daemonv1.SendResponse]("Send", &daemonv1.SendRequest{Route: route(name), To: "orch", Kind: "brief", Body: "x", Key: "k"}, nil), connect.CodeUnauthenticated},
		{"Claim", unary[daemonv1.ClaimRequest, daemonv1.ClaimResponse]("Claim", &daemonv1.ClaimRequest{Route: route(name), ReadKey: "k"}, nil), connect.CodeUnauthenticated},
		{"Ack", unary[daemonv1.AckRequest, daemonv1.AckResponse]("Ack", &daemonv1.AckRequest{Route: route(name), Claim: "c", Id: messageID("m"), Record: "r"}, nil), connect.CodeUnauthenticated},
		{"Release", unary[daemonv1.ReleaseRequest, daemonv1.ReleaseResponse]("Release", &daemonv1.ReleaseRequest{Route: route(name), Claim: "c"}, nil), connect.CodeUnauthenticated},
		{"Hold", unary[daemonv1.HoldRequest, daemonv1.HoldResponse]("Hold", &daemonv1.HoldRequest{Route: route(name), Target: "t", Reason: "r"}, nil), connect.CodeUnauthenticated},
		{"Unhold", unary[daemonv1.UnholdRequest, daemonv1.UnholdResponse]("Unhold", &daemonv1.UnholdRequest{Route: route(name), Target: "t"}, nil), connect.CodeUnauthenticated},
		{"Authority", unary[daemonv1.AuthorityRequest, daemonv1.AuthorityResponse]("Authority", &daemonv1.AuthorityRequest{Route: route(missing)}, nil), connect.CodeNotFound},
		{"DaemonStatus", unary("DaemonStatus", &daemonv1.DaemonStatusRequest{}, func(r *daemonv1.DaemonStatusResponse) error {
			if r.GetStateDir() == "" || r.GetDigest() == "" {
				return fmt.Errorf("status %v", r)
			}
			return versioned(r)
		}), okay},
		{"ListOperations", unary("ListOperations", &daemonv1.ListOperationsRequest{}, versioned[*daemonv1.ListOperationsResponse]), okay},
		{"GetOperation", unary[daemonv1.GetOperationRequest, daemonv1.GetOperationResponse]("GetOperation", &daemonv1.GetOperationRequest{Id: "missing"}, nil), connect.CodeNotFound},
		{"ResolveOperation", unary[daemonv1.ResolveOperationRequest, daemonv1.ResolveOperationResponse]("ResolveOperation", &daemonv1.ResolveOperationRequest{Id: "missing", Decision: "nonsense"}, nil), connect.CodeInvalidArgument},
		{"Wait", unary[daemonv1.WaitRequest, daemonv1.WaitResponse]("Wait", &daemonv1.WaitRequest{Route: route(name)}, nil), connect.CodeUnauthenticated},
		{"RemoveRoute", unary("RemoveRoute", &daemonv1.RemoveRouteRequest{Route: route(name)}, func(r *daemonv1.RemoveRouteResponse) error {
			if !r.GetRemoved() || !r.GetInboxKept() {
				return fmt.Errorf("removed %v", r)
			}
			return versioned(r)
		}), okay},
	}
}

// TestScenariosCoverEveryMethod fails when the service grows a method that no
// scenario calls.
func TestScenariosCoverEveryMethod(t *testing.T) {
	methods := (&daemonv1.StatusRequest{}).ProtoReflect().Descriptor().ParentFile().Services().ByName("MessageService").Methods()
	covered := map[string]bool{}
	for _, s := range scenarios("x") {
		covered[s.method] = true
	}
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		if !covered[name] {
			t.Errorf("no scenario calls %s", name)
		}
		delete(covered, name)
	}
	for name := range covered {
		t.Errorf("scenario for %s, which the service does not have", name)
	}
}

func TestEveryMethodAgainstTheDaemon(t *testing.T) {
	hc := httpClient(t, startDaemon(t))
	encodings := map[string][]connect.ClientOption{"proto": nil, "json": {connect.WithProtoJSON()}}
	for encoding, options := range encodings {
		t.Run(encoding, func(t *testing.T) {
			for _, s := range scenarios("conformance-" + encoding) {
				t.Run(s.method, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					got, err := s.call(ctx, hc, options)
					if err != nil {
						t.Fatal(err)
					}
					if got != s.want {
						t.Fatalf("code %v, want %v", got, s.want)
					}
				})
			}
		})
	}
}

func TestUnknownMethodIsUnimplemented(t *testing.T) {
	hc := httpClient(t, startDaemon(t))
	got, err := unary[daemonv1.StatusRequest, daemonv1.StatusResponse]("Nothing", &daemonv1.StatusRequest{Route: route("x")}, nil)(context.Background(), hc, nil)
	if err != nil || got != connect.CodeUnimplemented {
		t.Fatalf("code %v, error %v", got, err)
	}
}

// TestOnlyTheRPCPathIsServed walks the paths of the retired surface, and a few
// others: every one is a plain 404, and none answers as the daemon.
func TestOnlyTheRPCPathIsServed(t *testing.T) {
	hc := httpClient(t, startDaemon(t))
	for _, target := range []string{
		"GET /", "GET /v1/hello", "GET /v1/health", "POST /v1/shutdown", "GET /v1/routes", "POST /v1/routes",
		"GET /v1/events", "GET /v1/status", "GET /v1/ops", "POST /v1/ops/x/steps/0/resolve", "GET /v1/routes/r/status", "GET /v1/routes/r/events", "POST /v1/routes/r/messages",
		"GET /api/health", "GET /rpc", "GET /metrics",
	} {
		method, path, _ := strings.Cut(target, " ")
		req, err := http.NewRequest(method, "http://herdsman"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound || strings.Contains(string(body), "daemon_version") {
			t.Errorf("%s: %d %s", target, res.StatusCode, body)
		}
	}
}

// TestShutdownStopsTheDaemon runs on a daemon of its own, as it ends it.
func TestShutdownStopsTheDaemon(t *testing.T) {
	if os.Getenv("HERDSMAN_CONFORMANCE_SOCKET") != "" {
		t.Skip("does not stop a daemon it did not start")
	}
	socket := startDaemon(t)
	hc := httpClient(t, socket)
	got, err := unary[daemonv1.ShutdownRequest, daemonv1.ShutdownResponse]("Shutdown", &daemonv1.ShutdownRequest{Reason: "stop"}, nil)(context.Background(), hc, nil)
	if err != nil || got != okay {
		t.Fatalf("code %v, error %v", got, err)
	}
	for end := time.Now().Add(startWait); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return
		}
		_ = conn.Close()
	}
	t.Fatal("the daemon still listens after Shutdown")
}
