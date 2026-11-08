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
	register(command{name: "delete", summary: "Archive a route, verify the archive, then remove the route.", help: deleteHelp, run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return runControl(deleteVerb, args, stdout, stderr)
	}})
}

const deleteUsage = "usage: herdsman delete <route> [--force] [--keep-folder] [--grace <seconds>] [-y] [--dry-run]\n"

const deleteHelp = deleteUsage + `
Refuses while an agent of the route runs, unless --force, which stops the route
first. The route folder and private inbox are archived under the repository's
common git folder and the archive is verified; nothing is removed without a
verified archive. --keep-folder archives the route folder and leaves it in place.
`

var deleteVerb = controlVerb{name: "delete", usage: deleteUsage,
	flags: func(fs *flag.FlagSet, f *controlFlags) {
		fs.BoolVar(&f.force, "force", false, "stop running agents first")
		fs.BoolVar(&f.keepFolder, "keep-folder", false, "leave the route folder in place")
		graceFlag(fs, f)
	},
	call: func(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string, dry bool, f controlFlags) (*daemonv1.RouteControl, error) {
		res, err := sdk.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, DryRun: dry, Force: f.force, KeepFolder: f.keepFolder, GraceSeconds: int32(f.grace)})
		return res.GetResult(), err
	}}
