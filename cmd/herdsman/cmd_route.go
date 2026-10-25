package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
)

// now is the clock of the commands; tests replace it.
var now = time.Now

func init() {
	register(command{name: "route", summary: "Check a route folder (route check) or append a run-state line (route log).", run: runRoute})
}

const (
	routeCheckUsage = "usage: herdsman route check <route folder> [plan|launch|close]"
	routeLogUsage   = `usage: herdsman route log <path to 00-route.md> "<text>" (the file must exist)`
)

func runRoute(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "check":
		stage := route.StagePlan
		if len(args) > 1 {
			stage = args[1]
		}
		if len(args) == 0 || args[0] == "" || !isDir(args[0]) {
			fmt.Fprintln(stderr, routeCheckUsage)
			return 2
		}
		switch stage {
		case route.StagePlan, route.StageLaunch, route.StageClose:
		default:
			fmt.Fprintln(stderr, routeCheckUsage)
			return 2
		}
		return route.Check(args[0], stage, stdout)
	case "log":
		if len(args) < 2 || args[1] == "" || !isFile(args[0]) {
			fmt.Fprintln(stderr, routeLogUsage)
			return 2
		}
		if err := route.AppendLogLine(args[0], args[1], now()); err != nil {
			fmt.Fprintln(stderr, "herdsman route log:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, routeCheckUsage)
	fmt.Fprintln(stderr, routeLogUsage)
	return 2
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
