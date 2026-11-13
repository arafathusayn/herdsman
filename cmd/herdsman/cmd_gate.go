package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/gate"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/settings"
	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// gateInventory is the boundary check's agent list for a herdr-core route:
// its inbox is daemon-owned and its agents have no Herdr pane, so the live
// agents are the daemon's setup inventory of the route (the shared-core
// faults of spec section 6), read through the SDK, not the TUI's agent
// list. A TUI caller keeps the TUI's list.
func gateInventory(dir string, getenv func(string) string) inbox.Inventory {
	if dir == "" || !inbox.DaemonOwned(dir) || getenv("HERDR_PANE_ID") != "" {
		return nil
	}
	return inbox.ListedInventory{List: func(ctx context.Context) ([]inbox.Unknown, error) {
		sdk, err := client.Dial(client.Options{Getenv: getenv})
		if err != nil {
			return nil, err
		}
		defer sdk.Close()
		routes, err := sdk.Message.ListRoutes(ctx, &daemonv1.ListRoutesRequest{})
		if err != nil {
			return nil, err
		}
		var route *daemonv1.RouteInfo
		for _, r := range routes.GetRoutes() {
			if r.GetInbox() == dir {
				route = r
			}
		}
		if route == nil {
			return nil, fmt.Errorf("the daemon serves no route at %s", dir)
		}
		setup, err := sdk.Message.GetSetup(ctx, &daemonv1.GetSetupRequest{Route: route.GetRoute()})
		if err != nil {
			return nil, err
		}
		out := make([]inbox.Unknown, 0, len(setup.GetInventory()))
		for _, a := range setup.GetInventory() {
			session := json.RawMessage(a.GetSessionJson())
			if len(session) == 0 {
				session = json.RawMessage("null")
			}
			out = append(out, inbox.Unknown{Pane: a.GetPane(), Agent: a.GetAgent(), Session: session})
		}
		return out, nil
	}}
}

func init() {
	register(command{name: "approval-gate", summary: "Bind a relayed go before its effect: approval-gate consume ... | complete <request-id> <outcome>.", help: gateHelp, run: runGate})
}

// inboxOptions are the inbox settings of the environment. HEALTH_AFTER
// (1 to 86400 seconds) and LOCK_WAIT (1 to 60) go through the strict
// parser (A25) and reach the inbox as plain decimals; empty keeps the
// inbox's own default.
func inboxOptions() (inbox.Options, error) {
	o := inbox.Options{
		Dir:       os.Getenv("HERDSMAN_INBOX"),
		Preflight: os.Getenv("HERDSMAN_PREFLIGHT"),
	}
	for _, s := range []struct {
		name   string
		hi     int64
		target *string
	}{{"HEALTH_AFTER", 86400, &o.HealthAfter}, {"LOCK_WAIT", 60, &o.LockWait}} {
		v := os.Getenv(s.name)
		if v == "" {
			continue
		}
		n, err := settings.Int(s.name, v, 0, 1, s.hi)
		if err != nil {
			return o, err
		}
		*s.target = strconv.FormatInt(n, 10)
	}
	return o, nil
}

func runGate(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	args, asJSON := takeJSON(args)
	if asJSON {
		stdout = newEventJSON(stdout)
	}
	opts, err := inboxOptions()
	if err != nil {
		fmt.Fprintf(stderr, "herdsman approval-gate: %v\n", err)
		return settings.ExitInvalid
	}
	if opts.Dir == "" {
		fmt.Fprintf(stderr, "herdsman approval-gate: %v\n", settings.Missing("HERDSMAN_INBOX"))
		return settings.ExitInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts.Sleep = ctxSleep(ctx)
	g := &gate.Gate{Options: opts, Client: herdrClient(), Inventory: gateInventory(opts.Dir, os.Getenv), Stdout: stdout, Stderr: stderr}
	// As the script's traps: an interrupted gate frees its locks and exits
	// 130, 143 or 129. The running gate owns that cleanup: the signal only
	// stops it at its next write and ends its context; Run then frees the
	// locks on its own goroutine, so no write follows a freed lock.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigs)
	defer signal.Stop(sigs)
	go func() {
		s, ok := <-sigs
		if !ok {
			return
		}
		g.Stop(128 + int(s.(syscall.Signal)))
		cancel()
	}()
	return g.Run(ctx, args)
}

// ctxSleep is a lock wait that ends at once when ctx ends, so a stopped
// gate does not wait out LOCK_WAIT.
func ctxSleep(ctx context.Context) func(time.Duration) {
	return func(d time.Duration) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
}
