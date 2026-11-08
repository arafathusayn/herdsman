package main

import (
	"context"
	"flag"
	"io"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func init() {
	register(command{name: "stop", summary: "End a route's sessions and service panes; the work stays on disk.", help: stopHelp, run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return runControl(stopVerb, args, stdout, stderr)
	}})
}

const stopUsage = "usage: herdsman stop <route> [--grace <seconds>] [-y] [--dry-run]\n"

const stopHelp = stopUsage + `
Asks each agent for its report, waits up to --grace seconds (default 120), then
closes the agent and service panes and retires the pair. Worktrees, branches,
the route folder and the inbox stay. A stopped route can be resumed.
`

var stopVerb = controlVerb{name: "stop", usage: stopUsage, flags: graceFlag,
	call: func(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string, dry bool, f controlFlags) (*daemonv1.RouteControl, error) {
		res, err := sdk.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: id, Repo: repo, DryRun: dry, GraceSeconds: int32(f.grace)})
		return res.GetResult(), err
	}}

func graceFlag(fs *flag.FlagSet, f *controlFlags) {
	fs.IntVar(&f.grace, "grace", 0, "seconds agents get to write a report")
}
