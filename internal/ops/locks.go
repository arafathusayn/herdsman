package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// TaskLock names the lock of a task.
func TaskLock(task string) string { return "task:" + task }

// RefLock names the lock of a ref of a repository.
func RefLock(repo, ref string) string { return "ref:" + repo + ":" + ref }

// RemoteLock names the lock of a remote.
func RemoteLock(remote string) string { return "remote:" + remote }

// AccountLock is the export barrier. While an operation holds it no other
// operation starts, and it is granted only when none is running.
const AccountLock = "account"

// SnapshotLock names the lock of a snapshot, held while it is read or pruned.
func SnapshotLock(id string) string { return "snapshot:" + id }

// LockedError says which operation holds a lock.
type LockedError struct{ Lock, Holder string }

func (e *LockedError) Error() string {
	return fmt.Sprintf("ops: lock %s is held by operation %s", e.Lock, e.Holder)
}

// barrierWaiter is the holder a LockedError names for an export still waiting.
const barrierWaiter = "(an export is waiting)"

// checkBarrier refuses work while the account lock is held, or requested.
func checkBarrier(c store.Conn, draining bool) error {
	var holder string
	if err := c.Query("SELECT op_id FROM operation_locks WHERE name = ?", []any{AccountLock}, func(r store.Row) error { holder = r.Text(0); return nil }); err != nil {
		return err
	}
	if holder != "" {
		return &LockedError{Lock: AccountLock, Holder: holder}
	}
	if draining {
		return &LockedError{Lock: AccountLock, Holder: barrierWaiter}
	}
	return nil
}

// checkNoneRunning grants the account lock only once nothing is in flight.
// Parked operations do not count: no effect of theirs runs until a person
// resolves them, and Resolve waits for the barrier.
func checkNoneRunning(c store.Conn) error {
	var running string
	if err := c.Query("SELECT id FROM operations WHERE state = 'running' LIMIT 1", nil, func(r store.Row) error { running = r.Text(0); return nil }); err != nil {
		return err
	}
	if running != "" {
		return &LockedError{Lock: AccountLock, Holder: running}
	}
	return nil
}

func checkLocks(c store.Conn, locks []string, draining bool) error {
	wantsBarrier := slices.Contains(locks, AccountLock)
	if err := checkBarrier(c, draining && !wantsBarrier); err != nil {
		return err
	}
	if wantsBarrier {
		if err := checkNoneRunning(c); err != nil {
			return err
		}
	}
	for _, l := range locks {
		var holder string
		if err := c.Query("SELECT op_id FROM operation_locks WHERE name = ?", []any{l}, func(r store.Row) error { holder = r.Text(0); return nil }); err != nil {
			return err
		}
		if holder != "" {
			return &LockedError{Lock: l, Holder: holder}
		}
	}
	return nil
}

func takeLocks(c store.Conn, id string, locks []string) error {
	sorted := slices.Clone(locks)
	slices.Sort(sorted)
	for _, l := range slices.Compact(sorted) {
		if _, err := c.Exec("INSERT INTO operation_locks(name, op_id) VALUES (?, ?)", l, id); err != nil {
			return err
		}
	}
	return nil
}

func releaseLocks(c store.Conn, id string) error {
	_, err := c.Exec("DELETE FROM operation_locks WHERE op_id = ?", id)
	return err
}

// HeldLocks lists the locks held now.
func (e *Engine) HeldLocks() ([]string, error) {
	var out []string
	err := e.Read(func(c store.Conn) error {
		var err error
		out, err = queryIDs(c, "SELECT name FROM operation_locks ORDER BY name")
		return err
	})
	return out, err
}

// submitBarrier is SubmitWaiting for the account lock. New work is held back
// while it waits, or a steady stream of operations would starve the export.
func (e *Engine) submitBarrier(ctx context.Context, s Spec) (*Operation, error) {
	e.mu.Lock()
	e.draining++
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.draining--
		e.mu.Unlock()
	}()
	return e.SubmitWaiting(ctx, s)
}

// lockPoll is how often SubmitWaiting tries again.
const lockPoll = 20 * time.Millisecond

// SubmitWaiting is Submit that waits its turn on a held lock.
func (e *Engine) SubmitWaiting(ctx context.Context, s Spec) (*Operation, error) {
	for {
		op, err := e.Submit(ctx, s)
		var le *LockedError
		if !errors.As(err, &le) {
			return op, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
