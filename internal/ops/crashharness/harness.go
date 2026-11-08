// Package crashharness runs one scripted operation in a child process that
// kills itself with SIGKILL at a named step boundary. The parent test then
// recovers the journal and counts what actually happened on disk.
package crashharness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// The child reads its instructions from these variables.
const (
	EnvDir   = "HERDSMAN_CRASH_DIR"
	EnvPhase = "HERDSMAN_CRASH_PHASE" // "run" or "recover"
	EnvPoint = "HERDSMAN_CRASH_AT"    // empty: never kill
	EnvTrace = "HERDSMAN_CRASH_TRACE" // a file that receives every boundary reached
)

// EffectName is the one effect the script uses.
const EffectName = "append-line"

// Keys are the idempotency keys of the scripted steps, in order.
var Keys = []string{"first", "second", "third"}

// Script is the operation the harness drives.
func Script() ops.Spec {
	s := ops.Spec{Key: "harness-op", Kind: "harness", Plan: "append one line per key", Locks: []string{ops.TaskLock("harness")}}
	for _, k := range Keys {
		s.Steps = append(s.Steps, ops.StepSpec{Name: "write " + k, Effect: EffectName, Key: k, Spec: "line", Retry: ops.RetryNever,
			Events: []ops.Event{{Topic: "harness.step", Payload: k}}})
	}
	return s
}

// LinesEffect appends one line per Apply and never deduplicates, so a second
// Apply is visible as a second line.
type LinesEffect struct{ Dir string }

func (l LinesEffect) file(s ops.Step) string { return filepath.Join(l.Dir, s.Key+".log") }

// Apply appends the operation id to the step's file.
func (l LinesEffect) Apply(_ context.Context, s ops.Step) error {
	f, err := os.OpenFile(l.file(s), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(s.OpID + "\n"); err != nil {
		return err
	}
	return f.Sync()
}

// Probe reports whether this operation's line is present.
func (l LinesEffect) Probe(_ context.Context, s ops.Step) (ops.Verdict, string, error) {
	b, err := os.ReadFile(l.file(s))
	if errors.Is(err, os.ErrNotExist) {
		return ops.Absent, "", nil
	}
	if err != nil {
		return ops.Unknown, "", err
	}
	if strings.TrimSpace(string(b)) == s.OpID {
		return ops.Matches, "", nil
	}
	return ops.Different, "the file holds another operation's line", nil
}

// FileSink delivers an event by creating one file named after its key, so a
// repeat of a key is dropped the way a real consumer drops it.
type FileSink struct{ Dir string }

// Deliver records the event once.
func (f FileSink) Deliver(_ context.Context, ev ops.OutboxEvent) error {
	h, err := os.OpenFile(filepath.Join(f.Dir, strings.ReplaceAll(ev.Key, "/", "_")), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer h.Close()
	_, err = fmt.Fprintln(h, ev.Topic, ev.Payload)
	return err
}

// Paths under the harness directory.
func DB(dir string) string      { return filepath.Join(dir, "state", "account.db") }
func Effects(dir string) string { return filepath.Join(dir, "effects") }
func Sink(dir string) string    { return filepath.Join(dir, "sink") }

// Prepare makes the folders one run needs.
func Prepare(dir string) error {
	for _, d := range []string{filepath.Dir(DB(dir)), Effects(dir), Sink(dir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Child runs the phase the environment names and returns the exit code.
func Child() int {
	dir, phase, point, trace := os.Getenv(EnvDir), os.Getenv(EnvPhase), os.Getenv(EnvPoint), os.Getenv(EnvTrace)
	if err := child(dir, phase, point, trace); err != nil {
		fmt.Fprintln(os.Stderr, "crashharness:", err)
		return 1
	}
	return 0
}

func child(dir, phase, point, trace string) error {
	db, err := store.Open(DB(dir), store.Writer, store.KindAccount)
	if err != nil {
		return err
	}
	defer db.Close()
	e := ops.NewEngine(db, ops.Options{
		Effects: map[string]ops.Effect{EffectName: LinesEffect{Dir: Effects(dir)}},
		Fault: func(p string) {
			if trace != "" {
				if f, err := os.OpenFile(trace, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					fmt.Fprintln(f, p)
					f.Close()
				}
			}
			if p == point {
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // the signal is on its way
			}
		},
	})
	ctx := context.Background()
	switch phase {
	case "run":
		op, err := e.Submit(ctx, Script())
		if err != nil {
			return err
		}
		if _, err := e.Drive(ctx, op.ID); err != nil {
			return err
		}
	case "recover":
		if err := e.Recover(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown phase %q", phase)
	}
	return e.DeliverOutbox(ctx, FileSink{Dir: Sink(dir)})
}
