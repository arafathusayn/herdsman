package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// NoticeSocketEnv names the daemon's worker socket in a managed agent's
// environment. The core wrapper sets it; nothing else serves NoticeService.
const NoticeSocketEnv = "HERDSMAN_NOTICE_SOCKET"

// ErrNotManaged is DialNotice's answer outside a managed execution.
var ErrNotManaged = errors.New("the notice commands serve managed herdr-core executions: " + NoticeSocketEnv + " is not set; a person's terminal has no notices")

// DialNotice prepares a NoticeService client on the worker socket socket, or
// on the one getenv names when socket is empty. It opens no connection. The
// worker socket answers only the calling process's execution.
func DialNotice(socket string, getenv func(string) string, header http.Header) (daemonv1.NoticeService, func(), error) {
	if socket == "" {
		if getenv == nil {
			return nil, nil, ErrNotManaged
		}
		socket = getenv(NoticeSocketEnv)
	}
	if socket == "" {
		return nil, nil, ErrNotManaged
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:       1,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	hc := &http.Client{Transport: &rpc.Transport{Base: transport, Header: header}}
	return daemonv1.NewNoticeServiceClient(hc, base), transport.CloseIdleConnections, nil
}
