package main

import (
	"fmt"
	"io"
)

const projectHelp = `usage: herdsman project context init|check|note|log ...
       herdsman project log [--since <date>] [--task <task>] [--json]

Work on the context repo of the project bound to this folder.

exit codes:
  0  done
  1  check found problems, the daemon refused, or it is not running
  2  bad usage
`

// projectVerbs are the verbs of the project command.
var projectVerbs = map[string]func([]string, io.Reader, io.Writer, io.Writer) int{
	"context": runProjectContext,
	"log":     runProjectLog,
}

func init() {
	register(command{name: "project", summary: "Work on a project's context repo: project context init|check|note, project log.", help: projectHelp,
		subHelp: map[string]string{"context": projectContextHelp, "log": projectLogHelp}, run: runProject})
}

func runProject(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, projectHelp)
		return 2
	}
	run, ok := projectVerbs[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "project: unknown verb %q\n%s", args[0], projectHelp)
		return 2
	}
	return run(args[1:], stdin, stdout, stderr)
}
