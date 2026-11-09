package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

const projectContextHelp = `usage: herdsman project context init
       herdsman project context check
       herdsman project context note --actor <who> [--task <task>] [--repo <repo>]... [--parent <id>]... <text>
       herdsman project context log [--since <date>] [--task <task>] [--json]

Run inside a folder bound to a project.

init lays out the context repo and prints its folder.
check validates the history and prints each problem.
note adds a note to the history and prints its id. Without --parent it joins
every open branch of the history.
log prints the history; it is the same command as herdsman project log.

exit codes:
  0  done; for check, the history is sound
  1  check found problems, the daemon refused, or it is not running
  2  bad usage
`

// projectCallTimeout bounds one call to the daemon.
const projectCallTimeout = 30 * time.Second

// projectFlags parses a verb's flags. When done is true the verb ends with code.
func projectFlags(flags *flag.FlagSet, args []string, help string, stdout io.Writer) (code int, done bool) {
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, help)
		return 0, true
	}
	if err != nil {
		return 2, true
	}
	return 0, false
}

// projectCall runs one call with a client and the caller's folder as scope.
func projectCall(verb string, stderr io.Writer, call func(context.Context, *client.Client, *daemonv1.Scope) error) int {
	folder, err := os.Getwd()
	if err == nil {
		var sdk *client.Client
		if sdk, err = client.Dial(client.Options{}); err == nil {
			defer sdk.Close()
			ctx, cancel := context.WithTimeout(context.Background(), projectCallTimeout)
			defer cancel()
			err = call(ctx, sdk, &daemonv1.Scope{Folder: folder})
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", verb, err)
		return 1
	}
	return 0
}

func runProjectContext(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, projectContextHelp)
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "--help", "-h", "help":
		fmt.Fprint(stdout, projectContextHelp)
		return 0
	case "init", "check":
		return runContextPlain(verb, rest, stdout, stderr)
	case "note":
		return runContextNote(rest, stdout, stderr)
	case "log":
		return runProjectLog(rest, nil, stdout, stderr)
	}
	fmt.Fprintf(stderr, "project context: unknown verb %q\n", verb)
	return 2
}

// runContextPlain runs the verbs that take no flags or arguments.
func runContextPlain(verb string, args []string, stdout, stderr io.Writer) int {
	name := "project context " + verb
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	if code, done := projectFlags(flags, args, projectContextHelp, stdout); done {
		return code
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	if verb == "init" {
		return projectCall(name, stderr, func(ctx context.Context, sdk *client.Client, scope *daemonv1.Scope) error {
			res, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope})
			if err == nil {
				fmt.Fprintln(stdout, res.GetFolder())
			}
			return err
		})
	}
	sound := true
	code := projectCall(name, stderr, func(ctx context.Context, sdk *client.Client, scope *daemonv1.Scope) error {
		res, err := sdk.Project.ContextCheck(ctx, &daemonv1.ProjectServiceContextCheckRequest{Scope: scope})
		if err != nil {
			return err
		}
		for _, p := range res.GetProblems() {
			sound = false
			fmt.Fprintf(stdout, "%s: %s: %s\n", p.GetFile(), p.GetCode(), p.GetDetail())
		}
		if sound {
			fmt.Fprintf(stdout, "%d events, no problems\n", res.GetEvents())
		}
		return nil
	})
	if code == 0 && !sound {
		return 1
	}
	return code
}

func runContextNote(args []string, stdout, stderr io.Writer) int {
	const name = "project context note"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	req := &daemonv1.ProjectServiceContextNoteRequest{}
	flags.StringVar(&req.Actor, "actor", "", "who is writing: a person or an agent")
	flags.StringVar(&req.Task, "task", "", "the task the note belongs to")
	flags.Func("repo", "a repository the note is about; repeatable", func(v string) error {
		req.Repos = append(req.Repos, v)
		return nil
	})
	flags.Func("parent", "an event this note follows; repeatable", func(v string) error {
		req.Parents = append(req.Parents, v)
		return nil
	})
	if code, done := projectFlags(flags, args, projectContextHelp, stdout); done {
		return code
	}
	req.Body = strings.Join(flags.Args(), " ")
	if req.Actor == "" || req.Body == "" {
		fmt.Fprintf(stderr, "%s: --actor and the note's text are required\n", name)
		return 2
	}
	return projectCall(name, stderr, func(ctx context.Context, sdk *client.Client, scope *daemonv1.Scope) error {
		req.Scope = scope
		res, err := sdk.Project.ContextNote(ctx, req)
		if err == nil {
			fmt.Fprintln(stdout, res.GetEvent().GetId())
		}
		return err
	})
}
