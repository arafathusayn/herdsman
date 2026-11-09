package main

import (
	"fmt"
	"io"
)

const projectHelp = `usage: herdsman project context init|check|note|log ...
       herdsman project log [--since <date>] [--task <task>] [--json]
       herdsman project create <name> [--description <text>] [--repo <spec>]... [--folder <path>] [--bind] [--json]
       herdsman project show [<project>] [--json]
       herdsman project list [--archived] [--json]
       herdsman project update [<project>] [--name <name>] [--description <text>]
                               [--add-repo <spec>]... [--remove-repo <name>]... [--json]
       herdsman project delete [<project>] [--force]
       herdsman project bind [<project>] [--folder <path>] [--json]
       herdsman project unbind [--folder <path>] [--json]

context and log work on the context repo of the project bound to this folder.

Keeps the projects, the folders bound to them and their repositories. A <spec>
is [<name>=]<path>[:<branch>[:<remote>]]; the name defaults to the folder's.
create binds --folder, or this folder with --bind.
<project> is a name, an id or @; left out, it is the project bound to this
folder. delete refuses a project with open tasks or tasks on a running route;
--force archives it instead and keeps its records.

exit codes:
  0  done
  1  check found problems, the record was not found, the daemon refused, or it is not running
  2  bad usage
`

// projectVerbs are the verbs of the project command.
var projectVerbs = map[string]func([]string, io.Reader, io.Writer, io.Writer) int{
	"context": runProjectContext,
	"log":     runProjectLog,
	"create":  plainVerb(runProjectCreate),
	"show":    plainVerb(runProjectShow),
	"list":    plainVerb(runProjectList),
	"update":  plainVerb(runProjectUpdate),
	"delete":  plainVerb(runProjectDelete),
	"bind":    plainVerb(runProjectBind),
	"unbind":  plainVerb(runProjectUnbind),
}

func init() {
	register(command{name: "project", summary: "Keep projects, their folders and repositories, and work on their context repo.", help: projectHelp,
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
