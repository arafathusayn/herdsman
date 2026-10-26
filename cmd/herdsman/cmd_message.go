package main

import (
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/inbox"
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
// the daemon serves the action and the route's inbox is owned by the
// daemon (its state/owner.json says so). The inbox is the route's entry
// in routes.json (the route from --route or HERDSMAN_ROUTE), else
// HERDSMAN_INBOX, whose route is then found in routes.json. enroll always
// takes the daemon path. It returns the arguments without --route and
// --json.
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
	if len(rest) > 0 && rest[0] == "enroll" {
		return route, rest, json, true
	}
	if len(rest) > 0 && fileOnly[rest[0]] {
		return route, rest, json, false
	}
	var routes daemon.RoutesFile
	if ph, err := daemon.PrivateHome("", getenv); err == nil {
		routes, _ = daemon.ReadRoutes(ph)
	}
	// A route missing from this private home's registry may be served by
	// a daemon of another private home: its daemon-owned HERDSMAN_INBOX
	// still selects the daemon path, whose discovery names that home (3.6).
	dir := routes.Routes[route].Inbox
	if dir == "" {
		dir = getenv("HERDSMAN_INBOX")
		for name, e := range routes.Routes {
			if route == "" && dir != "" && e.Inbox == dir {
				route = name
			}
		}
	}
	if route != "" && dir == "" {
		// No inbox to judge by: a live daemon of another private home
		// takes the daemon path, whose discovery names that home (3.6,
		// 6.2: parties need no HERDSMAN_INBOX).
		return route, rest, json, daemon.OtherHome(getenv) != ""
	}
	return route, rest, json, route != "" && inbox.DaemonOwned(dir)
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
