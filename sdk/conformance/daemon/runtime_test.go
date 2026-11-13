package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// runtimePath is where a method of the runtime service is served.
func runtimePath(method string) string {
	return "http://herdsman/rpc/" + daemonv1.RuntimeServiceName + "/" + method
}

type runtimeSide struct {
	t       *testing.T
	hc      *http.Client
	options []connect.ClientOption
}

func callRuntime[Req, Res any](s runtimeSide, method string, req *Req) (*Res, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := connect.NewClient[Req, Res](s.hc, runtimePath(method), s.options...).CallUnary(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestRuntimeServiceConformance(t *testing.T) {
	_, outside := os.LookupEnv("HERDSMAN_CONFORMANCE_SOCKET")
	socket := startDaemon(t)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	encodings := []struct {
		name    string
		options []connect.ClientOption
	}{{"proto", nil}, {"json", []connect.ClientOption{connect.WithProtoJSON()}}}
	for _, encoding := range encodings {
		side := runtimeSide{hc: &http.Client{Transport: transport}, options: encoding.options}
		t.Run(encoding.name, func(t *testing.T) {
			side.t = t
			runtimeScenarios(t, side, !outside)
		})
	}
}

func runtimeScenarios(t *testing.T, s runtimeSide, fresh bool) {
	def := must[daemonv1.RuntimeServiceGetDefaultResponse](t)(callRuntime[daemonv1.RuntimeServiceGetDefaultRequest, daemonv1.RuntimeServiceGetDefaultResponse](s, "GetDefault", &daemonv1.RuntimeServiceGetDefaultRequest{}))
	if def.GetBackend() != "herdr-core" && def.GetBackend() != "herdr-tui" {
		t.Fatalf("default = %v", def)
	}
	// The daemon this test started is a new installation; the first
	// encoding sees what its migration wrote, the second what the first set.
	if fresh && def.GetSource() != "new-store" && def.GetSource() != "set" {
		t.Fatalf("a new installation's default came from %q", def.GetSource())
	}
	original := def.GetBackend()
	other := map[string]string{"herdr-core": "herdr-tui", "herdr-tui": "herdr-core"}[original]

	bad, err := callRuntime[daemonv1.RuntimeServiceSetDefaultRequest, daemonv1.RuntimeServiceSetDefaultResponse](s, "SetDefault", &daemonv1.RuntimeServiceSetDefaultRequest{Backend: "herdr"})
	refused(t, bad, err, connect.CodeInvalidArgument)

	set := must[daemonv1.RuntimeServiceSetDefaultResponse](t)(callRuntime[daemonv1.RuntimeServiceSetDefaultRequest, daemonv1.RuntimeServiceSetDefaultResponse](s, "SetDefault", &daemonv1.RuntimeServiceSetDefaultRequest{Backend: other}))
	t.Cleanup(func() {
		_, _ = callRuntime[daemonv1.RuntimeServiceSetDefaultRequest, daemonv1.RuntimeServiceSetDefaultResponse](s, "SetDefault", &daemonv1.RuntimeServiceSetDefaultRequest{Backend: original})
	})
	if set.GetBackend() != other || set.GetPrevious() != original {
		t.Fatalf("set = %v", set)
	}
	now := must[daemonv1.RuntimeServiceGetDefaultResponse](t)(callRuntime[daemonv1.RuntimeServiceGetDefaultRequest, daemonv1.RuntimeServiceGetDefaultResponse](s, "GetDefault", &daemonv1.RuntimeServiceGetDefaultRequest{}))
	if now.GetBackend() != other || now.GetSource() != "set" || now.GetWrittenAt() <= 0 {
		t.Fatalf("after set = %v", now)
	}

	doc := must[daemonv1.RuntimeServiceDoctorResponse](t)(callRuntime[daemonv1.RuntimeServiceDoctorRequest, daemonv1.RuntimeServiceDoctorResponse](s, "Doctor", &daemonv1.RuntimeServiceDoctorRequest{Backend: "herdr-tui"}))
	if doc.GetSelection().GetBackend() != "herdr-tui" || doc.GetSelection().GetSource() != "flag" || doc.GetVersion().GetContract() == "" {
		t.Fatalf("doctor = %v", doc)
	}
	if len(doc.GetBackends()) != 2 || doc.GetInstallation().GetBackend() != other {
		t.Fatalf("doctor backends = %v", doc)
	}
	misspelled := must[daemonv1.RuntimeServiceDoctorResponse](t)(callRuntime[daemonv1.RuntimeServiceDoctorRequest, daemonv1.RuntimeServiceDoctorResponse](s, "Doctor", &daemonv1.RuntimeServiceDoctorRequest{Backend: "herdr-tiu"}))
	if misspelled.GetSelection() != nil {
		t.Fatalf("a misspelled backend was selected: %v", misspelled.GetSelection())
	}

	must[daemonv1.RuntimeServiceListExecutionsResponse](t)(callRuntime[daemonv1.RuntimeServiceListExecutionsRequest, daemonv1.RuntimeServiceListExecutionsResponse](s, "ListExecutions", &daemonv1.RuntimeServiceListExecutionsRequest{}))
	half, err := callRuntime[daemonv1.RuntimeServiceListExecutionsRequest, daemonv1.RuntimeServiceListExecutionsResponse](s, "ListExecutions", &daemonv1.RuntimeServiceListExecutionsRequest{Route: route("conformance")})
	refused(t, half, err, connect.CodeInvalidArgument)

	none, err := callRuntime[daemonv1.RuntimeServiceGetExecutionRequest, daemonv1.RuntimeServiceGetExecutionResponse](s, "GetExecution", &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: "00000000-0000-4000-8000-000000000000"})
	refused(t, none, err, connect.CodeNotFound)
	empty, err := callRuntime[daemonv1.RuntimeServiceGetExecutionRequest, daemonv1.RuntimeServiceGetExecutionResponse](s, "GetExecution", &daemonv1.RuntimeServiceGetExecutionRequest{})
	refused(t, empty, err, connect.CodeInvalidArgument)
}
