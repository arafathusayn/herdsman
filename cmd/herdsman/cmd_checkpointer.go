package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/checkpoint"
)

func init() {
	register(command{name: "checkpointer", summary: "Checkpoint the orchestrator's memory and compact it: TARGET=<pane> checkpointer [--once|--probe] | checkpointer guard <name> <pane>.", run: runCheckpointer})
}

const checkpointerUsage = "usage: TARGET=<pane> herdsman checkpointer [--once|--probe]\n       herdsman checkpointer guard <orchestrator-name> <pane>"

// checkpointerExit is os.Exit, replaced in tests.
var checkpointerExit = os.Exit

func runCheckpointer(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	ctx := context.Background()
	if len(args) > 0 && args[0] == "guard" {
		out, errText, code := checkpoint.Guard(ctx, herdrClient(), inboxOptions(), args[1:])
		fmt.Fprint(stdout, out)
		fmt.Fprint(stderr, errText)
		return code
	}
	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	if len(args) > 1 || (mode != "" && mode != "--once" && mode != "--probe") {
		fmt.Fprintln(stderr, checkpointerUsage)
		return 2
	}
	pwd, _ := os.Getwd()
	cfg, err := checkpoint.FromEnv(os.Getenv, pwd)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman checkpointer:", err)
		return 1
	}
	c := &checkpoint.Checkpointer{Config: cfg, Client: herdrClient(), Stdout: stdout, Stderr: stderr}
	if mode == "--probe" {
		return c.Probe(ctx)
	}
	// As the script's EXIT trap: an interrupted checkpointer frees the
	// input lock it holds.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigs)
	defer signal.Stop(sigs)
	go func() {
		s, ok := <-sigs
		if !ok {
			return
		}
		c.Release()
		checkpointerExit(128 + int(s.(syscall.Signal)))
	}()
	var code int
	if mode == "--once" {
		code, err = c.Once(ctx)
	} else {
		interval, ok := checkpoint.Interval(cfg.Interval)
		if !ok {
			fmt.Fprintf(stderr, "herdsman checkpointer: invalid INTERVAL: %s\n", cfg.Interval)
			return 2
		}
		err = c.Loop(ctx, interval)
	}
	if errors.Is(err, checkpoint.ErrNoInbox) {
		fmt.Fprintln(stderr, "herdsman checkpointer:", err)
		return 1
	}
	return code
}
