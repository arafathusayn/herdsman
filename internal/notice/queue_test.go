package notice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

func queuedService(t *testing.T, w *world, db Store, send func(context.Context, runtime.Execution, string) error) *Service {
	t.Helper()
	s := newService(t, w, db)
	s.cfg.Queued = func(k string) bool { return k == "codex" }
	s.cfg.Queue = send
	return s
}

func TestQueueWakeHoldRestartAndLostReply(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	db := NewMemStore()
	calls := 0
	send := func(_ context.Context, e runtime.Execution, line string) error {
		calls++
		if e.ID != "e" || line != QueueLine {
			t.Fatal("queue leaked task data or targeted another execution")
		}
		return errors.New("reply lost")
	}
	s := queuedService(t, w, db, send)
	e := w.add("e", "codex")
	approved(t, s, e)
	n := issue(t, s, e.ID, "secret task")
	issue(t, s, e.ID, "second task")
	if err := s.Hold(e.Repo, e.Route, true, "pause"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("queued under hold")
	}
	if err := s.Hold(e.Repo, e.Route, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err == nil || calls != 1 {
		t.Fatalf("queue: calls=%d err=%v", calls, err)
	}
	s.Close()
	s = queuedService(t, w, db, send)
	w.advance(2 * time.Minute)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(w.kinds(EventWakeUnanswered)) != 1 {
		t.Fatal("restart retried an uncertain write or hid unanswered wake")
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.kinds(EventWakeUnanswered)) != 1 {
		t.Fatal("unanswered event repeats")
	}
	other := e
	other.Generation++
	if err := s.ObserveCall(other); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil || calls != 1 {
		t.Fatal("another generation cleared reservation")
	}
	if err := s.ObserveCall(e); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, e, n.ID)
	if err != nil || got.Token == "" {
		t.Fatal("claim failed")
	}
	if _, err := s.Ack(ctx, e, n.ID, got.Token, OutcomeCompleted, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err == nil || calls != 2 {
		t.Fatal("next notice did not wake")
	}
}

func TestQueuePreparationRetriesButConcurrentTicksDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	calls := 0
	ready := false
	entered, release := make(chan struct{}), make(chan struct{})
	s := queuedService(t, w, NewMemStore(), func(context.Context, runtime.Execution, string) error {
		calls++
		if !ready {
			return ErrQueueNotReady
		}
		close(entered)
		<-release
		return nil
	})
	e := w.add("e", "codex")
	approved(t, s, e)
	issue(t, s, e.ID, "task")
	if err := s.QueueWake(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	ready = true
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := s.QueueWake(ctx, e.ID); err != nil {
				t.Error(err)
			}
		})
	}
	<-entered
	// The request path is not locked behind an external harness command.
	if err := s.Hold(e.Repo, e.Route, true, "pause"); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if calls != 2 {
		t.Fatalf("duplicate queue command: %d", calls)
	}
}

func TestQueueInterruptedAndFinalReport(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	calls := 0
	s := queuedService(t, w, NewMemStore(), func(context.Context, runtime.Execution, string) error { calls++; return nil })
	e := w.add("e", "codex")
	approved(t, s, e)
	n := issue(t, s, e.ID, "task")
	claim, _ := s.Read(ctx, e, n.ID)
	if err := s.Stopping(e.Repo, e.Route, "stop", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueFinalReport(ctx, FinalRequest{Key: "final", Execution: e.ID, StopOp: "stop", Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueWake(ctx, e.ID); err != nil || calls != 1 {
		t.Fatal("open claim hid final report from queue")
	}
	if err := s.ObserveCall(e); err != nil {
		t.Fatal(err)
	}
	w.edit(e.ID, func(e *runtime.Execution) {
		e.Observation.Program = runtime.ProgramIdle
		e.Conversation = &runtime.Conversation{Value: "session"}
	})
	if err := s.QueueWake(ctx, e.ID); err != nil || calls != 1 {
		t.Fatal("queued after interrupt")
	}
	if len(w.kinds(EventNeedsAttention)) != 1 {
		t.Fatal("interrupt was not announced")
	}
	_ = claim
}

func TestQueueRecoversUnacknowledgedClaimAfterTurnEnds(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	calls := 0
	s := queuedService(t, w, NewMemStore(), func(context.Context, runtime.Execution, string) error { calls++; return nil })
	e := w.add("e", "codex")
	approved(t, s, e)
	n := issue(t, s, e.ID, "brief")
	if _, err := s.Read(ctx, e, n.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("woke a claim before recovery was due")
	}
	w.advance(2 * time.Minute)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ended turn with open claim got %d wakes", calls)
	}
}
