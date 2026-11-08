package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestSocketPathUsesThePrivateHome(t *testing.T) {
	got, err := SocketPath(env(map[string]string{"HERDSMAN_PRIVATE_HOME": "/x/y"}))
	if err != nil || got != filepath.Join("/x/y", "run", "daemon.sock") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSocketPathDefaultsUnderHome(t *testing.T) {
	t.Setenv("HOME", "/fixture-home")
	got, err := SocketPath(env(nil))
	if err != nil || got != filepath.Join("/fixture-home", ".herdsman-private", "run", "daemon.sock") {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSocketPathNeedsAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := SocketPath(env(nil)); err == nil {
		t.Fatal("no error without a home")
	}
	if _, err := Dial(Options{Getenv: env(nil)}); err == nil {
		t.Fatal("Dial found a socket without a home")
	}
}

// serveSocket answers Status on a socket in a directory short enough for the
// platform's limit on socket paths.
func serveSocket(t *testing.T, fn func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error), seen *http.Header) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "run", "daemon.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := rpc.NewMux(rpc.Unary(daemonv1.MessageServiceName, "Status", fn))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Clone()
		mux.ServeHTTP(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func TestDialCallsOverTheSocket(t *testing.T) {
	var seen http.Header
	socket := serveSocket(t, func(_ context.Context, req *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		return &daemonv1.StatusResponse{Route: req.GetRoute(), Status: "ok"}, nil
	}, &seen)
	c, err := Dial(Options{Getenv: env(map[string]string{"HERDSMAN_PRIVATE_HOME": filepath.Dir(filepath.Dir(socket))}), Header: http.Header{"X-Party": {"p1"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Message.Status(context.Background(), &daemonv1.StatusRequest{Route: &corev1.RouteId{Value: "alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetRoute().GetValue() != "alpha" || seen.Get("X-Party") != "p1" || seen.Get(rpc.HeaderProtocolVersion) != "1" {
		t.Fatalf("%v, headers %v", res, seen)
	}
}

func TestFailureKeepsItsCodeAndDetails(t *testing.T) {
	var seen http.Header
	socket := serveSocket(t, func(context.Context, *daemonv1.StatusRequest) (*daemonv1.StatusResponse, error) {
		failure := rpc.Errorf(rpc.NotFound, "gone")
		if err := failure.AddDetail(&corev1.Error{Code: "unknown_route"}); err != nil {
			return nil, err
		}
		return nil, failure
	}, &seen)
	c, err := Dial(Options{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Message.Status(context.Background(), &daemonv1.StatusRequest{})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.NotFound {
		t.Fatalf("error %v", err)
	}
	var detail corev1.Error
	if !failure.Detail(&detail) || detail.GetCode() != "unknown_route" {
		t.Fatalf("detail %v", &detail)
	}
}

func TestDownDaemonIsTheFirstCallsError(t *testing.T) {
	c, err := Dial(Options{Socket: filepath.Join(t.TempDir(), "none.sock")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Message.Status(context.Background(), &daemonv1.StatusRequest{})
	var failure *rpc.Error
	if err == nil || errors.As(err, &failure) {
		t.Fatalf("error %v", err)
	}
}

func TestDialReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("HERDSMAN_PRIVATE_HOME", t.TempDir())
	c, err := Dial(Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestSocketPathPrefersTheStateDirectoryOverride(t *testing.T) {
	got, err := SocketPath(env(map[string]string{"HERDSMAN_STATE_DIR": "/s", "HERDSMAN_PRIVATE_HOME": "/x/y"}))
	if err != nil || got != filepath.Join("/s", "run", "daemon.sock") {
		t.Fatalf("%q %v", got, err)
	}
}
