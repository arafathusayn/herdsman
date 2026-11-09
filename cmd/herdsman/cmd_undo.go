package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const undoHelp = `usage: herdsman undo <snapshot> [--to <path>] [--json]

Restores a snapshot: the tree, its index, the branch and the ignored files it
kept. Without --to it goes back to its original path, or beside it when that
path is taken. Nothing is overwritten; a conflict stops with both sides named.

exit codes:
  0  restored
  1  refused or failed
  2  bad usage
`

// dialOps reaches the daemon through the SDK; tests replace it.
var dialOps = func() (daemonv1.OpsService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return sdk.Ops, sdk.Close, nil
}

func init() {
	register(command{name: "undo", summary: "Restore a snapshot.", help: undoHelp, run: runUndo})
}

func runUndo(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("undo", stderr)
	to := fs.String("to", "", "restore to this path")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "undo takes one snapshot")
	}
	service, closeService, err := dialOps()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	result, err := service.Undo(context.Background(), &daemonv1.UndoRequest{Snapshot: positional[0], Target: *to})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		data, err := json.Marshal(struct {
			Operation string `json:"operation"`
			Target    string `json:"target"`
			Original  bool   `json:"original"`
		}{result.GetOperationId(), result.GetTarget(), result.GetOriginal()})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintln(stdout, "restored to", result.GetTarget())
	return 0
}
