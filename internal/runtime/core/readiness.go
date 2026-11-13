package core

import (
	"context"
	"fmt"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// ReadinessInput is one execution's readiness check (spec section 8, step 7).
type ReadinessInput struct {
	Address  string
	Evidence runtime.ProcessEvidence
	// Registered is when the execution registered; only a status reported
	// after it counts.
	Registered time.Time
	Timeout    time.Duration
	Poll       time.Duration
	Procs      ProcessTable
}

// Readiness is the check's answer.
type Readiness struct {
	Readiness runtime.Readiness
	Program   runtime.Program
	Raw       string
	// Reason says what a person must do when Readiness is needs_attention,
	// or why identity is unknown.
	Reason string
}

// AwaitReadiness waits for the registered harness to report its first status
// since registration, with its identity checked on every read: the terminal's
// program is the registered process. A status alone never makes an execution
// ready, and a blocked status (a trust, login or update dialog) needs a
// person; nothing is typed into it.
func AwaitReadiness(ctx context.Context, c TerminalReader, in ReadinessInput) (Readiness, error) {
	if in.Timeout == 0 {
		in.Timeout = 90 * time.Second
	}
	if in.Poll == 0 {
		in.Poll = 250 * time.Millisecond
	}
	if !registered(runtime.Execution{Evidence: in.Evidence}) {
		return Readiness{Readiness: runtime.ReadinessIdentityUnknown, Reason: "the execution has not registered"}, nil
	}
	deadline := time.Now().Add(in.Timeout)
	for {
		t, err := c.Get(ctx, in.Address)
		if err != nil {
			return Readiness{}, err
		}
		if !t.Live() || t.PID == nil || int(*t.PID) != in.Evidence.PID {
			return Readiness{Readiness: runtime.ReadinessIdentityUnknown, Reason: fmt.Sprintf("terminal %s no longer runs the registered process %d", in.Address, in.Evidence.PID)}, nil
		}
		if alive, err := Alive(in.Procs, in.Evidence); err != nil {
			return Readiness{}, err
		} else if !alive {
			return Readiness{Readiness: runtime.ReadinessIdentityUnknown, Reason: errEvidence(in.Evidence).Error()}, nil
		}
		fresh := t.Status != nil && (t.StatusMS == nil || int64(*t.StatusMS) >= in.Registered.UnixMilli())
		if fresh {
			raw := t.Status.State
			out := Readiness{Program: runtime.ProgramOf(raw), Raw: raw}
			switch out.Program {
			case runtime.ProgramBlocked:
				out.Readiness = runtime.ReadinessNeedsAttention
				out.Reason = fmt.Sprintf("the harness in terminal %s shows a dialog (%s); answer it in the app, then run up again with --continue", in.Address, dialogOf(t.Status.Msg, t.Status.Title))
			case runtime.ProgramError:
				out.Readiness = runtime.ReadinessNeedsAttention
				out.Reason = fmt.Sprintf("the harness in terminal %s reports an error; look at it in the app", in.Address)
			default:
				out.Readiness = runtime.ReadinessReady
			}
			return out, nil
		}
		if !time.Now().Before(deadline) {
			return Readiness{Readiness: runtime.ReadinessNeedsAttention, Program: runtime.ProgramUnknown,
				Reason: fmt.Sprintf("the harness in terminal %s reported no status within %s; it may wait at a prompt the app shows", in.Address, in.Timeout)}, nil
		}
		select {
		case <-ctx.Done():
			return Readiness{}, ctx.Err()
		case <-time.After(in.Poll):
		}
	}
}

func dialogOf(msg, title string) string {
	switch {
	case title != "":
		return title
	case msg != "":
		return msg
	}
	return "blocked"
}
