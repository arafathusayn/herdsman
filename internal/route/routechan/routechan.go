// Package routechan runs a route's channel actions through the message
// command's own code. It is apart from package route because the message
// package already depends on route.
package routechan

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/message"
	"github.com/arafathusayn/herdsman/internal/route"
)

// Run performs one channel action as the call's pane would.
func Run(ctx context.Context, call route.ChannelCall) error {
	values := map[string]string{
		"HERDSMAN_INBOX":     call.Inbox,
		"HERDSMAN_PREFLIGHT": filepath.Join(call.Inbox, "setup.json"),
		"HERDR_PANE_ID":      call.Pane,
	}
	var stdout, stderr bytes.Buffer
	env := &message.Env{Getenv: func(k string) string { return values[k] }, Stdout: &stdout, Stderr: &stderr,
		Client: herdr.New(call.Socket), Executable: call.Executable, Ctx: ctx}
	defer env.Release()
	var code int
	switch call.Action {
	case route.ChannelRegister:
		code = env.Register(call.Args)
	case route.ChannelSend:
		code = env.Send(call.Args)
	case route.ChannelHold:
		code = env.Hold(call.Args)
	default:
		return fmt.Errorf("unknown channel action %q", call.Action)
	}
	if code != 0 {
		return fmt.Errorf("%s: %s", call.Action, strings.TrimSpace(stderr.String()))
	}
	return nil
}
