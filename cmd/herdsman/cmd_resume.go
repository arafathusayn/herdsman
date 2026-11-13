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
	register(command{name: "resume", summary: "Continue a paused route, or restart a stopped one from its record.", help: resumeHelp, run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return runControl(resumeVerb, args, stdout, stderr)
	}})
}

const resumeUsage = "usage: herdsman resume <route> [-p <text>] [-y] [--dry-run]\n"

const resumeHelp = resumeUsage + `
On a paused route, lifts the channel hold, starts the checkpointer again and
tells each agent to continue. On a stopped route, also starts the agents and
service panes again with the commands recorded by up; the launch prelude does
not run again, and the pair is attested and approved again.
-p adds a note to every continue prompt.
`

var resumeVerb = controlVerb{name: "resume", usage: resumeUsage,
	flags: func(fs *flag.FlagSet, f *controlFlags) {
		fs.StringVar(&f.note, "p", "", "a note for every continue prompt")
	},
	call: func(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string, dry bool, f controlFlags) (*daemonv1.RouteControl, error) {
		caller := upCaller()
		caller.ConfirmedChecksum = f.confirmed
		res, err := sdk.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo, DryRun: dry, Note: f.note, Caller: caller})
		return res.GetResult(), err
	}}
