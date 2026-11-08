package unary

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// statuses is the HTTP status each code travels under, as TestStatusTable has it.
var statuses = map[rpc.Code]int{rpc.Canceled: 499, rpc.Unknown: 500, rpc.InvalidArgument: 400, rpc.DeadlineExceeded: 504, rpc.NotFound: 404,
	rpc.AlreadyExists: 409, rpc.PermissionDenied: 403, rpc.ResourceExhausted: 429, rpc.FailedPrecondition: 400, rpc.Aborted: 409,
	rpc.OutOfRange: 400, rpc.Unimplemented: 501, rpc.Internal: 500, rpc.Unavailable: 503, rpc.DataLoss: 500, rpc.Unauthenticated: 401}

// scripted picks its answer from the route, so one server covers every scenario.
func scripted(ctx context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
	route := req.GetRoute().GetValue()
	switch {
	case route == "wait":
		<-ctx.Done()
		return nil, ctx.Err()
	case route == "deadline":
		var seq int64
		if _, ok := ctx.Deadline(); ok {
			seq = 1
		}
		return &daemonv1.StatusResponse{LastSeq: seq}, nil
	case strings.HasPrefix(route, "fail:"):
		failure := rpc.Errorf(rpc.Code(strings.TrimPrefix(route, "fail:")), "the reason")
		if err := failure.AddDetail(&corev1.Error{Code: "daemon_code"}); err != nil {
			return nil, err
		}
		return nil, failure
	}
	return &daemonv1.StatusResponse{Route: req.GetRoute(), LastSeq: 7}, nil
}

// TestTypeScriptClient runs the TypeScript client over the scenarios above,
// through a Unix socket as a local daemon serves it.
func TestTypeScriptClient(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("this test needs bun: ", err)
	}
	// t.TempDir can exceed the length limit of a socket path.
	dir, err := os.MkdirTemp("", "hs-unary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: rpc.NewMux(rpc.Unary(daemonv1.MessageServiceName, "Status", scripted))}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	if len(statuses) != len(rpc.Codes()) {
		t.Fatalf("%d statuses, binding has %d codes", len(statuses), len(rpc.Codes()))
	}
	table, err := json.Marshal(statuses)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "run", filepath.Join("..", "..", "ts", "conformance", "unary.ts"), socket, string(table))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
