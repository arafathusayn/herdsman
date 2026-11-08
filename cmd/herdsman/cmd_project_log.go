package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const projectLogHelp = `usage: herdsman project log [--since <date>] [--task <task>] [--json]

Print the history of the project bound to this folder, parents before
children, then by time. --since takes YYYY-MM-DD (UTC) or an RFC 3339 time.

exit codes:
  0  printed
  1  the daemon refused, or it is not running
  2  bad usage
`

// parseSince reads a --since value as milliseconds since the Unix epoch.
func parseSince(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("%q is neither YYYY-MM-DD nor an RFC 3339 time", value)
}

func runProjectLog(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	const name = "project log"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	req := &daemonv1.ProjectServiceLogRequest{}
	since := flags.String("since", "", "read from this date or time")
	flags.StringVar(&req.Task, "task", "", "only this task's events")
	flags.BoolVar(&req.Json, "json", false, "print JSON")
	if code, done := projectFlags(flags, args, projectLogHelp, stdout); done {
		return code
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	var err error
	if req.Since, err = parseSince(*since); err != nil {
		fmt.Fprintf(stderr, "%s: --since: %v\n", name, err)
		return 2
	}
	return projectCall(name, stderr, func(ctx context.Context, sdk *client.Client, scope *daemonv1.Scope) error {
		req.Scope = scope
		res, err := sdk.Project.Log(ctx, req)
		if err == nil {
			fmt.Fprint(stdout, res.GetText())
		}
		return err
	})
}
