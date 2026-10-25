package main

import (
	"io"
	"os"

	"github.com/arafathusayn/herdsman/internal/runcheck"
)

func init() {
	register(command{name: "run-check", summary: "Run one heavy check at a time on this machine, behind a load gate (--publish <hours>: the load reading).", run: runRunCheck})
}

func runRunCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := runcheck.FromEnv(os.Getenv)
	if len(args) > 0 && args[0] == "--publish" {
		hours := ""
		if len(args) > 1 {
			hours = args[1]
		}
		return runcheck.Publish(c, hours, stderr)
	}
	return runcheck.Run(c, args, stdin, stdout, stderr)
}
