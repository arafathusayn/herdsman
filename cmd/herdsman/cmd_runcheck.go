package main

import (
	"fmt"
	"io"
	"os"

	"github.com/arafathusayn/herdsman/internal/runcheck"
	"github.com/arafathusayn/herdsman/internal/settings"
)

func init() {
	register(command{name: "run-check", summary: "Run one heavy check at a time on this machine, behind a load gate (--publish <hours>: the load reading).", help: runCheckHelp, run: runRunCheck})
}

func runRunCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, err := runcheck.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "herdsman run-check: %v\n", err)
		return settings.ExitInvalid
	}
	if len(args) > 0 && args[0] == "--publish" {
		hours := ""
		if len(args) > 1 {
			hours = args[1]
		}
		longRunning()
		return runcheck.Publish(c, hours, stderr)
	}
	return runcheck.Run(c, args, stdin, stdout, stderr)
}
