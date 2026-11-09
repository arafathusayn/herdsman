package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/status"
	"github.com/arafathusayn/herdsman/internal/watch"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func init() {
	register(command{name: "wait", summary: "Wait for the next event of the loop (a report, a review, a blocked or stalled agent, a health flag), or TICK.", help: waitHelp, run: runWait})
	register(command{name: "watch", summary: "Watch implementers, the reviewer or pull requests: herdsman watch implementers|reviewer|prs.", help: watchHelp,
		subHelp: map[string]string{"implementers": watchHelp, "reviewer": watchHelp, "prs": watchHelp}, run: runWatch})
}

func watchDeps(stdout, stderr io.Writer) watch.Deps {
	return watch.Deps{Client: herdrClient(), Now: now, Stdout: stdout, Stderr: stderr, Status: statusWithLedger, Gh: watch.RunGh(stderr), GhStream: watch.RunGhStream(stderr)}
}

// statusWithLedger adds to the waiter's health check a flag for each agent
// that reports done while its task still runs something it owns. A failed
// read adds no flag: this check only tells, and the status check still runs.
func statusWithLedger(ctx context.Context, w io.Writer, specs string) {
	statusSnapshot(ctx, w, specs)
	client := herdrClient()
	for _, spec := range status.ParseSpecs(specs) {
		if spec.Tree == "" {
			continue
		}
		agent, err := client.AgentGet(ctx, spec.Name)
		if err != nil || string(agent.Status) != "done" {
			continue
		}
		running := ledgerRunning(ctx, spec.Tree)
		if len(running) == 0 {
			continue
		}
		fmt.Fprintf(w, "== %s (ledger)\n   <-- OWNED RESOURCES OUTLIVE DONE: %s %s and %d in all\n", spec.Name, running[0].GetKind(), running[0].GetId(), len(running))
	}
}

// ledgerRunning adds to a status check, so a daemon that cannot answer leaves
// the check as it was.
func ledgerRunning(ctx context.Context, tree string) []*daemonv1.ResourceLine {
	service, closeService, err := dialLedger()
	if err != nil {
		return nil
	}
	defer closeService()
	health, err := service.LedgerHealth(ctx, &daemonv1.LedgerHealthRequest{Dir: tree})
	if err != nil {
		return nil
	}
	return health.GetRunning()
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runWait(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	longRunning()
	args, asJSON := takeJSON(args)
	if asJSON {
		stdout = newEventJSON(stdout)
	}
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
	longRunning()
	args, asJSON := takeJSON(args)
	if asJSON {
		stdout = newEventJSON(stdout)
	}
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

// longRunning sets a soft memory limit for the long-running commands (A24:
// under 30 MB of resident memory): the collector runs earlier as the heap
// nears it, instead of letting it grow to twice the live data.
func longRunning() { debug.SetMemoryLimit(longRunningMemory) }

const longRunningMemory = 14 << 20
