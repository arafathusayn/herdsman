// Package daemon runs a stock Connect client against a real daemon on its
// socket. It starts the daemon from the workspace's own command, or uses the
// one at HERDSMAN_CONFORMANCE_SOCKET.
package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func TestStockClientAgainstTheDaemon(t *testing.T) {
	socket := startDaemon(t)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	hc := &http.Client{Transport: transport}
	encodings := map[string][]connect.ClientOption{"proto": nil, "json": {connect.WithProtoJSON()}}
	cases := []struct {
		name, path, route string
		code              connect.Code
	}{
		{"unknown route", "/rpc/herdsman.daemon.v1.MessageService/Status", "no-such-route", connect.CodeNotFound},
		{"no route", "/rpc/herdsman.daemon.v1.MessageService/Status", "", connect.CodeInvalidArgument},
		{"unknown method", "/rpc/herdsman.daemon.v1.MessageService/Nothing", "x", connect.CodeUnimplemented},
	}
	for name, options := range encodings {
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				client := connect.NewClient[daemonv1.StatusRequest, daemonv1.StatusResponse](hc, "http://herdsman"+c.path, options...)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, err := client.CallUnary(ctx, connect.NewRequest(&daemonv1.StatusRequest{Route: &corev1.RouteId{Value: c.route}}))
				var failure *connect.Error
				if !errors.As(err, &failure) || failure.Code() != c.code {
					t.Fatalf("error %v, want %v", err, c.code)
				}
			})
		}
	}
}
