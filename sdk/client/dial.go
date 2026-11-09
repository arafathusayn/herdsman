// Package client connects to the local herdsman daemon through the SDK alone.
package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// base is the URL host of every call; the socket decides where it goes.
const base = "http://herdsman"

// Options says how to reach the daemon. The zero value finds it the way the
// herdsman command does.
type Options struct {
	// Socket is the daemon's socket; empty finds it from the environment.
	Socket string
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
	// Header is sent with every call.
	Header http.Header
	// CallHeader, when set, is asked for headers on every call, with that
	// call's context. It suits an identity that can change between calls. Its
	// names replace the same names in Header.
	CallHeader func(context.Context) http.Header
}

// callHeaders adds a call's own headers on the way to the socket.
type callHeaders struct {
	next   http.RoundTripper
	header func(context.Context) http.Header
}

// RoundTrip implements http.RoundTripper.
func (t *callHeaders) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	for name, values := range t.header(req.Context()) {
		out.Header[http.CanonicalHeaderKey(name)] = values
	}
	return t.next.RoundTrip(out)
}

// Client holds one service client per daemon service.
type Client struct {
	Message daemonv1.MessageService
	Ops     daemonv1.OpsService
	Ledger  daemonv1.LedgerService
	Route   daemonv1.RouteService
	Project daemonv1.ProjectService
	Task    daemonv1.TaskService

	http *http.Client
}

// SocketPath is the daemon's socket: under HERDSMAN_STATE_DIR when it is set,
// then HERDSMAN_PRIVATE_HOME, otherwise under ~/.herdsman-private.
func SocketPath(getenv func(string) string) (string, error) {
	home := getenv("HERDSMAN_STATE_DIR")
	if home == "" {
		home = getenv("HERDSMAN_PRIVATE_HOME")
	}
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(user, ".herdsman-private")
	}
	return filepath.Join(home, "run", "daemon.sock"), nil
}

// Dial prepares a client. It opens no connection, so a daemon that is down
// shows up as the first call's error.
func Dial(options Options) (*Client, error) {
	socket := options.Socket
	if socket == "" {
		getenv := options.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		found, err := SocketPath(getenv)
		if err != nil {
			return nil, err
		}
		socket = found
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:       2,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	var next http.RoundTripper = transport
	if options.CallHeader != nil {
		next = &callHeaders{next: transport, header: options.CallHeader}
	}
	hc := &http.Client{Transport: &rpc.Transport{Base: next, Header: options.Header}}
	return &Client{
		Message: daemonv1.NewMessageServiceClient(hc, base),
		Ops:     daemonv1.NewOpsServiceClient(hc, base),
		Ledger:  daemonv1.NewLedgerServiceClient(hc, base),
		Route:   daemonv1.NewRouteServiceClient(hc, base),
		Project: daemonv1.NewProjectServiceClient(hc, base),
		Task:    daemonv1.NewTaskServiceClient(hc, base),
		http:    &http.Client{Transport: transport},
	}, nil
}

// Close frees the client's idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }
