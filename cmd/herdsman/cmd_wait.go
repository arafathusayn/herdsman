package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/watch"
)

func init() {
	register(command{name: "wait", summary: "Wait for the next event of the loop (a report, a review, a blocked or stalled agent, a health flag), or TICK.", help: waitHelp, run: runWait})
	register(command{name: "watch", summary: "Watch implementers, the reviewer or pull requests: herdsman watch implementers|reviewer|prs.", help: watchHelp, run: runWatch})
}

func watchDeps(stdout, stderr io.Writer) watch.Deps {
	return watch.Deps{Client: herdrClient(), Now: now, Stdout: stdout, Stderr: stderr, Status: statusSnapshot, Gh: watch.RunGh(stderr)}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runWait(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: herdsman wait (settings in the environment)")
		return 2
	}
	// LIMIT_MIN feeds the in-process health check.
	if _, _, err := statusLimit(); err != nil {
		fmt.Fprintf(stderr, "herdsman wait: %v\n", err)
		return 2
	}
	ctx, cancel := signalContext()
	defer cancel()
	return watch.Wait(ctx, os.LookupEnv, watchDeps(stdout, stderr))
}

const watchUsage = "usage: herdsman watch implementers|reviewer|prs (settings in the environment)"

func runWatch(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, watchUsage)
		return 2
	}
	ctx, cancel := signalContext()
	defer cancel()
	d := watchDeps(stdout, stderr)
	switch args[0] {
	case "implementers":
		return watch.Implementers(ctx, os.LookupEnv, d)
	case "reviewer":
		return watch.Reviewer(ctx, os.LookupEnv, d)
	case "prs":
		// Appended, so the caller's own PATH (and its gh) wins; the two
		// folders only help a bare background shell find gh.
		home, _ := os.UserHomeDir()
		_ = os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)+"/opt/homebrew/bin")
		return watch.PRs(ctx, os.LookupEnv, d)
	}
	fmt.Fprintln(stderr, watchUsage)
	return 2
}
