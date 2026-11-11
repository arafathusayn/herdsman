package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Controller is the part of the core client pause and stop use.
type Controller interface {
	TerminalReader
	Interrupt(ctx context.Context, terminal string, timeoutSeconds float64) (hc.InterruptResult, error)
	Kill(ctx context.Context, terminal string) (hc.Terminal, error)
}

// Proven checks that an execution's terminal still runs its registered
// process, now. It returns the terminal; ok is false when the address holds
// anything else, such as a recovery shell, or has ended.
func Proven(ctx context.Context, c TerminalReader, procs ProcessTable, e runtime.Execution) (hc.Terminal, bool, error) {
	// A terminal on another machine is never an execution's: its pid is
	// that machine's, whatever this machine's process table says of it.
	if !registered(e) || e.TerminalAddress == "" || hc.MachineOf(e.TerminalAddress) != "" {
		return hc.Terminal{}, false, nil
	}
	t, err := c.Get(ctx, e.TerminalAddress)
	if hc.IsKind(err, hc.KindNotFound) {
		return hc.Terminal{}, false, nil
	}
	if err != nil {
		return hc.Terminal{}, false, err
	}
	if !t.Local() || !t.Live() || t.PID == nil || int(*t.PID) != e.Evidence.PID {
		return t, false, nil
	}
	alive, err := Alive(procs, e.Evidence)
	return t, alive, err
}

// PauseLedger remembers, per pause, which executions it interrupted, so a
// pause never sends a second interrupt, also across a daemon restart.
type PauseLedger interface {
	Interrupted(pause, execution string) (bool, error)
	MarkInterrupted(pause, execution string) error
}

// FilePauseLedger keeps one marker file per interrupt under Dir.
type FilePauseLedger struct{ Dir string }

func (f FilePauseLedger) path(pause, execution string) string {
	safe := strings.NewReplacer("/", "-", ":", "-").Replace(pause)
	return filepath.Join(f.Dir, "interrupt-"+safe+"-"+execution)
}

// Interrupted implements PauseLedger.
func (f FilePauseLedger) Interrupted(pause, execution string) (bool, error) {
	_, err := os.Stat(f.path(pause, execution))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// MarkInterrupted implements PauseLedger.
func (f FilePauseLedger) MarkInterrupted(pause, execution string) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.path(pause, execution), nil, 0o600)
}

// Pause outcomes for one execution.
const (
	PauseLeftWaiting = "left_waiting" // at rest: nothing to stop, no key sent
	PauseInterrupted = "interrupted"
	PauseAlready     = "already_interrupted" // this pause sent its one interrupt before
	PausePending     = "pause_pending"       // a turn that ends by itself; no interrupt
	PauseLost        = "lost"                // the address no longer runs the execution
)

// PauseInput is one execution's part of a pause.
type PauseInput struct {
	Pause     string
	Execution runtime.Execution
	// InTurn is the daemon's record that the execution is in a turn, not its
	// status alone (spec section 12).
	InTurn bool
	// PersonAsked: a person asked for the interrupt of a harness whose
	// interrupted turn needs a person to go on.
	PersonAsked bool
	Ledger      PauseLedger
	Procs       ProcessTable
}

// PauseResult is what the pause did to one execution.
type PauseResult struct {
	Execution string
	Outcome   string
	// Observed is core's answer to the interrupt, when one was sent.
	Observed string
}

// Pause applies a pause to one execution: at most one interrupt per pause,
// none to an execution at rest, none to a harness whose interrupted turn a
// person must resume unless a person asked, and never to an address that no
// longer runs the execution. It never escalates to a kill. The interrupt is
// marked sent before it is sent, so a crash between the two can only lose
// it, never send it twice.
func Pause(ctx context.Context, c Controller, in PauseInput) (PauseResult, error) {
	out := PauseResult{Execution: in.Execution.ID}
	t, ok, err := Proven(ctx, c, in.Procs, in.Execution)
	if err != nil {
		return out, err
	}
	if !ok {
		out.Outcome = PauseLost
		return out, nil
	}
	if !in.InTurn {
		out.Outcome = PauseLeftWaiting
		return out, nil
	}
	if done, err := in.Ledger.Interrupted(in.Pause, in.Execution.ID); err != nil {
		return out, err
	} else if done {
		out.Outcome = PauseAlready
		return out, nil
	}
	a, err := AdapterFor(in.Execution.HarnessKind)
	if err != nil {
		return out, err
	}
	if !a.InterruptResumable && !in.PersonAsked {
		out.Outcome = PausePending
		return out, nil
	}
	if err := in.Ledger.MarkInterrupted(in.Pause, in.Execution.ID); err != nil {
		return out, err
	}
	res, err := c.Interrupt(ctx, t.Address, 0)
	if err != nil {
		return out, fmt.Errorf("interrupt %s: %w", t.Address, err)
	}
	out.Outcome, out.Observed = PauseInterrupted, res.Outcome
	return out, nil
}

// StopInput is one execution's stop.
type StopInput struct {
	Execution runtime.Execution
	Procs     ProcessTable
	// Recorded are processes the ledger records under the execution: work
	// the harness started that may outlive its terminal.
	Recorded []runtime.ProcessEvidence
	// Grace is how long a recorded process gets after SIGTERM (default 5 s).
	Grace time.Duration
}

// StopResult is what a stop did and verified.
type StopResult struct {
	// Already: the terminal had ended before the stop.
	Already bool
	// Shell: the address runs another process (a recovery shell); it was
	// left alone, as nothing proves it is the execution.
	Shell bool
	// Killed: terminal.kill was sent to the proven execution.
	Killed bool
	// Ended and Left are the recorded processes that ended and that are
	// still running after the grace period.
	Ended, Left []runtime.ProcessEvidence
	// Verified: the terminal has ended, the harness process is gone, and no
	// recorded process is left.
	Verified bool
}

// Stop ends one proven owned execution (spec section 12). Immediately before
// terminal.kill it checks that the address still holds the recorded process;
// a recovery shell is never killed. It then ends every process the ledger
// records under the execution, each checked by (pid, start time) before it is
// signalled, and verifies termination before it reports success.
func Stop(ctx context.Context, c Controller, in StopInput) (StopResult, error) {
	var out StopResult
	if in.Grace == 0 {
		in.Grace = 5 * time.Second
	}
	recorded := append([]runtime.ProcessEvidence(nil), in.Recorded...)
	t, ok, err := Proven(ctx, c, in.Procs, in.Execution)
	if err != nil {
		return out, err
	}
	switch {
	case ok:
		// Work under the harness now is the execution's own.
		if table, err := in.Procs.List(ctx); err == nil {
			for _, p := range descendants(table, in.Execution.Evidence.PID) {
				if ev, err := Evidence(in.Procs, p.PID); err == nil && !containsEvidence(recorded, ev) {
					recorded = append(recorded, ev)
				}
			}
		}
		// The check right before the kill: the address must still hold the
		// recorded process.
		if _, still, err := Proven(ctx, c, in.Procs, in.Execution); err != nil {
			return out, err
		} else if !still {
			out.Shell = true
			break
		}
		if _, err := c.Kill(ctx, t.Address); err != nil && !hc.IsKind(err, hc.KindFinished) {
			return out, fmt.Errorf("kill %s: %w", t.Address, err)
		}
		out.Killed = true
	case t.Address != "" && t.Live():
		out.Shell = true
	default:
		out.Already = true
	}
	for _, ev := range recorded {
		ended, err := endProcess(ctx, in.Procs, ev, in.Grace)
		if err != nil {
			return out, err
		}
		if ended {
			out.Ended = append(out.Ended, ev)
		} else {
			out.Left = append(out.Left, ev)
		}
	}
	harnessGone, err := waitGone(ctx, in.Procs, in.Execution.Evidence, in.Grace)
	if err != nil {
		return out, err
	}
	ended := true
	if in.Execution.TerminalAddress != "" && !out.Shell {
		after, err := c.Get(ctx, in.Execution.TerminalAddress)
		if err == nil && after.Live() && after.PID != nil && int(*after.PID) == in.Execution.Evidence.PID {
			ended = false
		} else if err != nil && !hc.IsKind(err, hc.KindNotFound) {
			return out, err
		}
	}
	out.Verified = ended && harnessGone && len(out.Left) == 0
	return out, nil
}

func containsEvidence(list []runtime.ProcessEvidence, e runtime.ProcessEvidence) bool {
	for _, x := range list {
		if x == e {
			return true
		}
	}
	return false
}

// endProcess ends one recorded process: TERM, then KILL after the grace
// period. A pid whose start time differs is another process and is left.
func endProcess(ctx context.Context, procs ProcessTable, ev runtime.ProcessEvidence, grace time.Duration) (bool, error) {
	alive, err := Alive(procs, ev)
	if err != nil || !alive {
		return err == nil, err
	}
	_ = procs.Signal(ev.PID, syscall.SIGTERM)
	if gone, err := waitGone(ctx, procs, ev, grace); err != nil || gone {
		return gone, err
	}
	if alive, err := Alive(procs, ev); err != nil || !alive {
		return err == nil, err
	}
	_ = procs.Signal(ev.PID, syscall.SIGKILL)
	return waitGone(ctx, procs, ev, grace)
}

func waitGone(ctx context.Context, procs ProcessTable, ev runtime.ProcessEvidence, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		alive, err := Alive(procs, ev)
		if err != nil {
			return false, err
		}
		if !alive {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
