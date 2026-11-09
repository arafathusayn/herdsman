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
	register(command{name: "delete", summary: "Remove a route, once a verified snapshot makes that undoable.", help: deleteHelp, run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return runControl(deleteVerb, args, stdout, stderr)
	}})
}

const deleteUsage = "usage: herdsman delete <route> [--force] [--keep-folder] [--keep-worktrees] [--grace <seconds>] [-y] [--dry-run]\n"

const deleteHelp = deleteUsage + `
Refuses while an agent of the route runs, unless --force, which stops the route
first. Takes a snapshot of the route folder and private inbox through the
recovery pipeline, and removes nothing unless that snapshot verifies and covers
every path it removes. The result names the operation to give "herdsman undo".
--keep-folder leaves the route folder in place; --keep-worktrees leaves the
route's tasks and their worktrees alone.
`

var deleteVerb = controlVerb{name: "delete", usage: deleteUsage,
	flags: func(fs *flag.FlagSet, f *controlFlags) {
		fs.BoolVar(&f.force, "force", false, "stop running agents first")
		fs.BoolVar(&f.keepFolder, "keep-folder", false, "leave the route folder in place")
		fs.BoolVar(&f.keepWorktrees, "keep-worktrees", false, "leave the route's tasks and worktrees alone")
		graceFlag(fs, f)
	},
	call: func(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string, dry bool, f controlFlags) (*daemonv1.RouteControl, error) {
		res, err := sdk.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, DryRun: dry, Force: f.force, KeepFolder: f.keepFolder, KeepWorktrees: f.keepWorktrees, GraceSeconds: int32(f.grace)})
		return res.GetResult(), err
	}}
