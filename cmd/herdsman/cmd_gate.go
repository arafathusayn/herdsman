package main

import (
	"context"
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
)

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
		Installed: os.Getenv("HERDSMAN_MESSAGES_DIR"),
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
	g := &gate.Gate{Options: opts, Client: herdrClient(), Stdout: stdout, Stderr: stderr}
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
