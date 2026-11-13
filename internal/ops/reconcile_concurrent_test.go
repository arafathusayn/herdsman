package ops_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
)

type heldEffect struct {
	apply func()
	probe func()
	calls atomic.Int32
}

func (f *heldEffect) Apply(context.Context, ops.Step) error {
	f.calls.Add(1)
	if f.apply != nil {
		f.apply()
	}
	return nil
}
func (f *heldEffect) Probe(context.Context, ops.Step) (ops.Verdict, string, error) {
	if f.probe != nil {
		f.probe()
	}
	return ops.Absent, "", nil
}

func TestRecoveryLeavesAnExecutingEffectAlone(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	fx := &heldEffect{apply: func() { close(entered); <-release }, probe: func() { probes.Add(1) }}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
	op, err := e.Submit(t.Context(), oneStep(ops.RetryNever))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.Drive(t.Context(), op.ID); done <- err }()
	<-entered
	recoveryErr := e.Recover(t.Context())
	close(release)
	driveErr := <-done
	if recoveryErr != nil || driveErr != nil {
		t.Fatalf("recover %v, drive %v", recoveryErr, driveErr)
	}
	if probes.Load() != 0 || fx.calls.Load() != 1 {
		t.Fatalf("probes %d, effects %d", probes.Load(), fx.calls.Load())
	}
	got, err := e.Get(op.ID)
	if err != nil || got.State != ops.OpDone {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestDriveCannotRaceARecoveryProbe(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fx := &heldEffect{probe: func() { close(entered); <-release }}
	e, _ := openEngine(t, ops.Options{Effects: map[string]ops.Effect{"fx": fx}})
	op := intendedOp(t, e, oneStep(ops.RetryNever))
	done := make(chan error, 1)
	go func() { done <- e.Reconcile(t.Context()) }()
	<-entered
	_, driveErr := e.Drive(t.Context(), op.ID)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(driveErr, ops.ErrBusy) {
		t.Fatalf("drive during probe: %v", driveErr)
	}
	got, err := e.Drive(t.Context(), op.ID)
	if err != nil || got.State != ops.OpDone || fx.calls.Load() != 1 {
		t.Fatalf("%+v %v; effects %d", got, err, fx.calls.Load())
	}
}
