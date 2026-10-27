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
	"github.com/arafathusayn/herdsman/internal/settings"
)

func init() {
	register(command{name: "checkpointer", summary: "Checkpoint the orchestrator's memory and compact it: TARGET=<pane> checkpointer [--once|--probe] | checkpointer guard <name> <pane>.",
		subHelp: map[string]string{"guard": guardHelp}, run: runCheckpointer})
}

const checkpointerUsage = `usage: TARGET=<pane> herdsman checkpointer [--once|--probe [--json]]
       herdsman checkpointer guard <orchestrator-name> <pane>

Settings (environment): TARGET (required), INTERVAL=900 (seconds, or with
an s, m, h or d suffix), LIMIT=250000, LOG=$TMPDIR/checkpointer.log,
COMPACT=/compact, PROMPT, TRANSCRIPT, CODEX_HOME; adjutant mode:
HERDSMAN_ORCHESTRATOR, HERDSMAN_INBOX, HERDSMAN_PREFLIGHT, LOCK_WAIT=6.

With --json, --probe prints one object: {"why","hint","file","context"}
(context: the tokens read, or null).

Exit codes:
  0  --probe printed; --once sent its checkpoint (and compact when due); guard OK
  1  --once: no transcript, an input not sent, or a wait that failed
  2  usage, a bad setting, or a guard refusal
`

const guardHelp = checkpoint.GuardUsage + `

A read-only check, right before each input in adjutant mode: the approved
registry names <pane> as <orchestrator-name>, and its live session still
matches. Settings (environment): HERDSMAN_INBOX (required),
HERDSMAN_PREFLIGHT.

Exit codes:
  0  OK <orchestrator-name> <pane>
  2  usage, a bad setting, or a refusal (the reason on stderr)
`

// checkpointerWorkingTimeout bounds the wait for a prompted turn (0: the
// script's 30 s); tests shorten it.
var checkpointerWorkingTimeout time.Duration

// checkpointerPrompt replaces the prompt send in tests (nil: herdr's).
var checkpointerPrompt func(ctx context.Context, target, text string) error

func runCheckpointer(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	ctx := context.Background()
	if len(args) > 0 && args[0] == "guard" {
		// The guard always reads the inbox: an unset or empty
		// HERDSMAN_INBOX is the shared required line, exit 2.
		o, err := checkpoint.InboxSettings(os.Getenv)
		if err == nil && o.Dir == "" {
			err = settings.Missing("HERDSMAN_INBOX")
		}
		if err != nil {
			fmt.Fprintln(stderr, "herdsman checkpointer:", err)
			return settings.ExitInvalid
		}
		out, errText, code := checkpoint.Guard(ctx, herdrClient(), o, args[1:])
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
	jsonOut := len(args) == 2 && mode == "--probe" && args[1] == "--json"
	if (len(args) > 1 && !jsonOut) || (mode != "" && mode != "--once" && mode != "--probe") {
		fmt.Fprint(stderr, checkpointerUsage)
		return 2
	}
	pwd, _ := os.Getwd()
	cfg, err := checkpoint.FromEnv(os.Getenv, pwd)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman checkpointer:", err)
		return settings.ExitInvalid
	}
	c := &checkpoint.Checkpointer{Config: cfg, Client: herdrClient(), Stdout: stdout, Stderr: stderr, WorkingTimeout: checkpointerWorkingTimeout,
		Prompt: checkpointerPrompt}
	if jsonOut {
		return c.ProbeJSON(ctx)
	}
	if mode == "--probe" {
		return c.Probe(ctx)
	}
	// A signal only ends the run (its waits, lock wait, send and pause end
	// with the context) and exits 128+n. The input lock is freed by the
	// input's deferred Release once the send has stopped, never while a
	// prompt may still be on its way.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	got := make(chan int, 1)
	go func() {
		select {
		case s := <-sigs:
			got <- 128 + int(s.(syscall.Signal))
			cancel()
		case <-ctx.Done():
		}
	}()
	code := 0
	if mode == "--once" {
		code = c.Once(ctx)
	} else {
		_ = c.Loop(ctx) // runs until a signal
	}
	select {
	case n := <-got:
		return n
	default:
		return code
	}
}
