package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint"
)

func init() {
	register(command{name: "checkpointer", summary: "Checkpoint the orchestrator's memory and compact it: TARGET=<pane> checkpointer [--once|--probe] | checkpointer guard <name> <pane>.", run: runCheckpointer})
}

const checkpointerUsage = `usage: TARGET=<pane> herdsman checkpointer [--once|--probe]
       herdsman checkpointer guard <orchestrator-name> <pane>

Settings (environment): TARGET (required), INTERVAL=900 (seconds, or with
an s, m, h or d suffix), LIMIT=250000, LOG=$TMPDIR/checkpointer.log,
COMPACT=/compact, PROMPT, TRANSCRIPT, CODEX_HOME; adjutant mode:
HERDSMAN_ORCHESTRATOR, HERDSMAN_INBOX, HERDSMAN_PREFLIGHT,
HERDSMAN_MESSAGES_DIR, LOCK_WAIT=6.

Exit codes:
  0  --probe printed; --once sent its checkpoint (and compact when due); guard OK
  1  --once: no transcript, an input not sent, or a wait that failed
  2  usage, a bad setting, or a guard refusal
`

// checkpointerExit is os.Exit, replaced in tests.
var checkpointerExit = os.Exit

// checkpointerWorkingTimeout bounds the wait for a prompted turn (0: the
// script's 30 s); tests shorten it.
var checkpointerWorkingTimeout time.Duration

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
	if len(args) == 1 && (mode == "--help" || mode == "-h") {
		fmt.Fprint(stdout, checkpointerUsage)
		return 0
	}
	if len(args) > 1 || (mode != "" && mode != "--once" && mode != "--probe") {
		fmt.Fprint(stderr, checkpointerUsage)
		return 2
	}
	pwd, _ := os.Getwd()
	cfg, err := checkpoint.FromEnv(os.Getenv, pwd)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman checkpointer:", err)
		return 2
	}
	c := &checkpoint.Checkpointer{Config: cfg, Client: herdrClient(), Stdout: stdout, Stderr: stderr, WorkingTimeout: checkpointerWorkingTimeout}
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
	if mode == "--once" {
		return c.Once(ctx)
	}
	_ = c.Loop(ctx) // runs until a signal ends the process
	return 0
}
