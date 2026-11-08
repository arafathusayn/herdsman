package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// recoveryCall serves undo, export and import. It takes a method name and a
// JSON request, so these files need no server package.
var recoveryCall func(ctx context.Context, method string, request []byte) ([]byte, error)

func callRecovery(method string, request any) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return recoveryCall(context.Background(), method, data)
}

type undoRequest struct {
	Snapshot string `json:"snapshot"`
	Target   string `json:"target,omitempty"`
}

type undoResult struct {
	Target   string `json:"target"`
	Original bool   `json:"original"`
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
	data, err := callRecovery("Undo", undoRequest{Snapshot: positional[0], Target: *to})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	var result undoResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "restored to", result.Target)
	return 0
}
