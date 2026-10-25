package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/gate"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

func init() {
	register(command{name: "approval-gate", summary: "Bind a relayed go before its effect: approval-gate consume ... | complete <request-id> <outcome>.", run: runGate})
}

// inboxOptions are the inbox settings of the environment.
func inboxOptions() inbox.Options {
	return inbox.Options{
		Dir:         os.Getenv("HERDSMAN_INBOX"),
		Preflight:   os.Getenv("HERDSMAN_PREFLIGHT"),
		Installed:   os.Getenv("HERDSMAN_MESSAGES_DIR"),
		HealthAfter: os.Getenv("HEALTH_AFTER"),
		LockWait:    os.Getenv("LOCK_WAIT"),
	}
}

func runGate(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	g := &gate.Gate{Options: inboxOptions(), Client: herdrClient(), Stdout: stdout, Stderr: stderr}
	// As the script's traps: an interrupted gate frees its locks and exits
	// 130, 143 or 129.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigs)
	defer signal.Stop(sigs)
	go func() {
		s, ok := <-sigs
		if !ok {
			return
		}
		g.Release()
		os.Exit(128 + int(s.(syscall.Signal)))
	}()
	return g.Run(context.Background(), args)
}
