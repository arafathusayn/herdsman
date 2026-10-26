package main

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/message"
)

func init() {
	register(command{name: "message", summary: "The adjutant message channel: message status|wait|read|release|ack|send|hold|unhold, and register|courier|card-watch|resume-goal|authority|leak-gate.", run: runMessage})
}

// executable is the running binary, links resolved, for the binary setup proof.
func executable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// fileOnly are the message commands that stay file-mode programs: the
// script services and the local leak check (6.2).
var fileOnly = map[string]bool{"register": true, "courier": true, "card-watch": true, "resume-goal": true, "leak-gate": true}

// daemonPath picks the daemon path of a message command (design 12.1):
// a route named by --route or HERDSMAN_ROUTE, and an action the daemon
// serves. It returns the arguments without --route and --json.
func daemonPath(args []string, getenv func(string) string) (route string, rest []string, json, ok bool) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			json = true
		case args[i] == "--route" && i+1 < len(args):
			route = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if route == "" {
		route = getenv("HERDSMAN_ROUTE")
	}
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help" || rest[0] == "help") {
		return route, rest, json, false
	}
	if len(rest) > 0 && rest[0] == "enroll" {
		return route, rest, json, true
	}
	return route, rest, json, route != "" && (len(rest) == 0 || !fileOnly[rest[0]])
}

func runMessage(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if route, rest, asJSON, ok := daemonPath(args, os.Getenv); ok {
		return daemon.RunMessage(&daemon.MessageEnv{Getenv: os.Getenv, Stdout: stdout, Stderr: stderr,
			Version: daemonVersion(), JSON: asJSON, Route: route}, rest)
	}
	e := &message.Env{Getenv: os.Getenv, Stdout: stdout, Stderr: stderr, Client: herdrClient(), Executable: executable()}
	// As the scripts' traps: an interrupted command frees its locks and
	// exits 130, 143 or 129.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigs)
	defer signal.Stop(sigs)
	go func() {
		s, ok := <-sigs
		if !ok {
			return
		}
		e.Release()
		os.Exit(128 + int(s.(syscall.Signal)))
	}()
	return e.Main(args)
}
