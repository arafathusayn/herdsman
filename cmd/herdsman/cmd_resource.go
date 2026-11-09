package main

import (
	"context"
	"fmt"
	"io"
	"os"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const resourceHelp = `usage: herdsman resource add --kind <kind> --id <id> [--task <task>] [--execution <id>]

Records that the current execution created a resource, which is what lets
teardown stop it. A process is recorded with its start time and command line.
Run it from the worktree, or name the task.

exit codes:
  0  recorded
  1  it could not be recorded (the message says why)
  2  bad usage
`

func init() {
	register(command{name: "resource", summary: "Record a resource a task created: resource add.", help: resourceHelp, run: runResource})
}

func runResource(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		return usageError(stderr, "usage: herdsman resource add --kind <kind> --id <id>")
	}
	fs := verbFlags("resource add", stderr)
	kind := fs.String("kind", "", "the kind of resource")
	id := fs.String("id", "", "its id")
	task := fs.String("task", "", "the task; default: the current worktree's")
	execution := fs.String("execution", os.Getenv("HERDSMAN_EXEC"), "the execution; default: the pane's, else the task's newest")
	if _, _, err := parseVerb(fs, args[1:]); err != nil {
		return 2
	}
	if *kind == "" || *id == "" {
		return usageError(stderr, "--kind and --id are required")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	service, closeService, err := dialLedger()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	if _, err := service.RegisterResource(context.Background(), &daemonv1.RegisterResourceRequest{Dir: dir, Task: *task, Execution: *execution, Kind: *kind, Id: *id}); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "recorded %s %s\n", *kind, *id)
	return 0
}
