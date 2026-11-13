package notice

import (
	"context"
	"errors"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// QueueLine carries no task content or identifiers. A delayed delivery can
// only ask the authenticated execution to recheck its own notice gates.
const QueueLine = "A notice is waiting for you. Run `herdsman notice read` and do what it prints."
const EventWakeUnanswered = "wake_unanswered"

// ErrQueueNotReady proves the queue was not written. A later tick may retry
// preparation, unlike an uncertain error from an already-started command.
var ErrQueueNotReady = errors.New("harness queue is not ready")

// ObserveCall retires a queued wake only when its own execution calls back.
// An interrupted queue is also released by that evidence, never by a timer.
func (s *Service) ObserveCall(e runtime.Execution) error {
	return s.quiet(func(tx Tx) error {
		list, err := tx.Notices(e.ID)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.Generation != e.Generation || n.Endpoint != e.EndpointID {
				continue
			}
			if n.WakePending || n.Interrupted {
				n.WakePending, n.Interrupted = false, false
				if err := tx.PutNotice(n); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// QueueWake reserves a wake durably before calling the harness queue. A
// lost queue reply or a daemon crash leaves that reservation outstanding;
// neither can cause an automatic duplicate. Ordinary notice calls continue
// while the external queue command runs. queueMu serializes concurrent ticks.
func (s *Service) QueueWake(ctx context.Context, execution string) error {
	if s.cfg.Queue == nil {
		return nil
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	e, err := s.execution(ctx, execution)
	if err != nil {
		return err
	}
	if s.cfg.Queued == nil || !s.cfg.Queued(e.HarnessKind) {
		return nil
	}
	var reserved Notice
	var events []Event
	err = s.quiet(func(tx Tx) error {
		if s.closed {
			return errors.New("notice service is closed")
		}
		list, err := tx.Notices(e.ID)
		if err != nil {
			return err
		}
		var target *Notice
		open := false
		for i := range list {
			n := &list[i]
			if n.Open() && n.Ordinary() {
				open = true
			}
			if n.Interrupted {
				return nil
			}
			if n.Kind == KindFinalReport && n.State == StateIssued {
				target = n
			}
		}
		for _, n := range list {
			if !n.WakePending {
				continue
			}
			// The unanswered clock does not run during an open claim.
			if open {
				n.WakeAt = s.now()
				return tx.PutNotice(n)
			}
			if !n.WakeReported && s.now().Sub(n.WakeAt) >= s.cfg.WaitGrace {
				n.WakeReported = true
				if err := tx.PutNotice(n); err != nil {
					return err
				}
				events = append(events, Event{Kind: EventWakeUnanswered, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: n.ID,
					Detail: "the queued wake received no notice call; the reservation is retained until this execution calls back"})
			}
			return nil
		}
		if target == nil {
			for i := range list {
				if list[i].Open() && list[i].RecoveryDue {
					target = &list[i]
					break
				}
			}
		}
		if target == nil {
			for i := range list {
				if list[i].State == StateIssued && list[i].Ordinary() {
					target = &list[i]
					break
				}
			}
		}
		if target == nil || s.gate(tx, e, *target, true) != nil {
			return nil
		}
		// Idle after a session has started means interrupt/control-line state on
		// Codex. Its queue is held there; no automatic line can resume it.
		if e.Observation.Program == runtime.ProgramIdle && e.Conversation != nil {
			target.Interrupted = true
			events = append(events, Event{Kind: EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: target.ID,
				Reason: AttentionInterrupted, Detail: "the harness queue is held after an interrupt; submit a line in its pane", Line: AttendedLine})
			return tx.PutNotice(*target)
		}
		target.WakePending, target.WakeReported, target.WakeAt = true, false, s.now()
		reserved = *target
		return tx.PutNotice(reserved)
	})
	for _, ev := range events {
		s.cfg.Emit(ev)
	}
	if err != nil || reserved.ID == "" {
		return err
	}
	// Bound a misbehaving queue process independently of the tick's context.
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = s.cfg.Queue(bounded, e, QueueLine)
	if errors.Is(err, ErrQueueNotReady) {
		// No write occurred. Keep diagnostics once, but do not strand work
		// whose session evidence has not appeared on disk yet.
		var attention *Event
		saveErr := s.quiet(func(tx Tx) error {
			n, ok, readErr := tx.NoticeByID(reserved.ID)
			if readErr != nil || !ok {
				return readErr
			}
			n.WakePending = false
			if !n.Attention {
				n.Attention = true
				attention = &Event{Kind: EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Notice: n.ID,
					Reason: "queue_unavailable", Detail: err.Error(), Line: AttendedLine}
			}
			return tx.PutNotice(n)
		})
		if attention != nil {
			s.cfg.Emit(*attention)
		}
		return saveErr
	}
	return err
}

// QueueAllowed rechecks the reserved notice immediately before the external
// queue write, after any version and native-session probes have completed.
func (s *Service) QueueAllowed(e runtime.Execution) bool {
	allowed := false
	err := s.quiet(func(tx Tx) error {
		list, err := tx.Notices(e.ID)
		if err != nil {
			return err
		}
		for _, n := range list {
			if n.WakePending && !n.Interrupted && (n.State == StateIssued || n.Open() && n.RecoveryDue) && s.gate(tx, e, n, true) == nil {
				allowed = true
			}
		}
		return nil
	})
	return err == nil && allowed
}
