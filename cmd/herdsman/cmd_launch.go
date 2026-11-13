package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/arafathusayn/herdsman/internal/coreview"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/profile"
)

func init() {
	register(command{name: "launch", summary: "Run one agent launch in a herdr-core terminal; herdr-core runs it, not a person.", help: launchHelp, run: runLaunch})
}

const launchHelp = `usage: herdsman launch --socket <path> --intent <id>
       herdsman launch --view <descriptor>

The program herdr-core starts in the terminal of a reserved launch. It proves
to the daemon that it is that terminal's process, runs the prelude, registers,
and then becomes the harness. A person never runs it; up does, through core.
The optional TUI view uses --view to attach a helper to an existing execution.
Closing that view never stops the execution.

exit codes:
  0   never: on success the process becomes the harness
  64  usage error
  70  the daemon refused the launch
  71  a prelude step failed
  72  the launch was not registered
  73  the harness could not start
  74  the daemon's launch socket could not be reached
`

func runLaunch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "the daemon's launch socket")
	view := fs.String("view", "", "the optional TUI attachment descriptor")
	intent := fs.String("intent", "", "the reserved launch intent")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, launchHelp)
		return launch.ExitUsage
	}
	if *view != "" && *socket == "" && *intent == "" {
		if err := coreview.Run(context.Background(), *view, stdin, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return launch.ExitConnection
		}
		return 0
	}
	if *view != "" || *socket == "" || *intent == "" {
		fmt.Fprint(stderr, launchHelp)
		return launch.ExitUsage
	}
	w := &launch.Wrapper{Socket: *socket, Intent: *intent, Stderr: stderr, VerifySandbox: profile.CheckCoreLaunch}
	return w.Main(context.Background())
}
