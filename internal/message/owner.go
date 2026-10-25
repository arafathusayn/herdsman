package message

import (
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// owner is one process's lock ownership: every queue, input, alert and
// adopted lock that the command or a nested command (a sub-Env) holds, in
// one list under one mutex, so the signal path frees them all (review 3).
// A lock is taken under the mutex, one attempt at a time, so no lock is
// ever held without being listed; once drained, nothing is taken again.
type owner struct {
	mu      sync.Mutex
	held    []heldLock
	drained bool
}

type heldLock struct {
	by       *Env
	l        *inbox.Lock
	mutation bool
}

// owner is the run's ownership state, shared with its sub-runs.
func (e *Env) owner() *owner {
	e.ownOnce.Do(func() {
		if e.own == nil {
			e.own = &owner{}
		}
	})
	return e.own
}

// take takes the lock folder dir (tries attempts, a second apart). As the
// input lock (mutation), fold runs as its new owner before the lock is
// listed; its error frees the lock and refuses. nil without an error: the
// lock stayed busy, or the run is draining.
func (e *Env) take(dir string, tries int, mutation bool, fold func() error) (*inbox.Lock, error) {
	o := e.owner()
	for attempt := 1; ; attempt++ {
		o.mu.Lock()
		if o.drained {
			o.mu.Unlock()
			return nil, nil
		}
		if l := inbox.TryOnce(dir); l != nil {
			if fold != nil {
				if err := fold(); err != nil {
					_ = l.Release()
					o.mu.Unlock()
					return nil, err
				}
			}
			o.held = append(o.held, heldLock{by: e, l: l, mutation: mutation})
			o.mu.Unlock()
			return l, nil
		}
		o.mu.Unlock()
		if attempt >= max(tries, 1) {
			return nil, nil
		}
		e.sleep(time.Second)
	}
}

// hold lists a lock taken outside take (an adopted lock folder).
func (e *Env) hold(l *inbox.Lock) *inbox.Lock {
	if l == nil {
		return nil
	}
	o := e.owner()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.drained {
		_ = l.Release()
		return l
	}
	o.held = append(o.held, heldLock{by: e, l: l})
	return l
}

// release frees one held lock.
func (e *Env) release(l *inbox.Lock) error {
	o := e.owner()
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, h := range o.held {
		if h.l == l {
			o.held = append(o.held[:i], o.held[i+1:]...)
			return l.Release()
		}
	}
	if o.drained {
		return nil // a signal already freed it; another process may own it now
	}
	return l.Release()
}

// releaseAll frees the locks this Env took (a sub-Env leaves its parent's).
func (e *Env) releaseAll() {
	o := e.owner()
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.held[:0]
	for i := len(o.held) - 1; i >= 0; i-- {
		if o.held[i].by == e {
			_ = o.held[i].l.Release()
		}
	}
	for _, h := range o.held {
		if h.by != e {
			kept = append(kept, h)
		}
	}
	o.held = kept
}

// Release is the signal path: it waits for any owner-only publication in
// progress (AsOwner), marks the run drained so nothing is taken or
// published as owner again, then frees every lock of the process. A signal
// handler calls it from its own goroutine while the command runs.
func (e *Env) Release() {
	o := e.owner()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drained = true
	for i := len(o.held) - 1; i >= 0; i-- {
		_ = o.held[i].l.Release()
	}
	o.held = nil
}

// ownedBy is the channel's Owner: whether the run holds the input lock of
// lockDir, fixed while fn runs.
func (o *owner) ownedBy(lockDir string, fn func(held bool) error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	held := false
	if !o.drained {
		for _, h := range o.held {
			if h.mutation && h.l.Dir() == lockDir {
				held = true
			}
		}
	}
	return fn(held)
}

// recordExposure is record_exposure with the run's own lock list: as the
// input lock's owner it publishes; otherwise it takes the lock (which
// publishes what waited) or, when busy, leaves the record pending.
func (e *Env) recordExposure(fault string, unknown []inbox.Unknown) error {
	switch fault {
	case inbox.FaultInventory, inbox.FaultPending:
	default:
		return nil
	}
	if fault == inbox.FaultInventory && inbox.Exists(e.path("state", "boundary-tainted")) {
		return nil
	}
	publish := func() (bool, error) {
		done := false
		err := e.ch.AsOwner(func(h bool) error {
			if !h {
				return nil
			}
			done = true
			return e.ch.RecordExposure(fault, unknown, true)
		})
		return done, err
	}
	if done, err := publish(); done || err != nil {
		return err
	}
	l, err := e.takeMutation(e.ch.LockWait)
	if err != nil {
		return err
	}
	if l == nil {
		if fault == inbox.FaultInventory {
			return e.ch.KeepPending(unknown)
		}
		return nil
	}
	defer e.release(l)
	_, err = publish()
	return err
}
