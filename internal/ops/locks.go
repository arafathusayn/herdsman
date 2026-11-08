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

// LockedError says which operation holds a lock.
type LockedError struct{ Lock, Holder string }

func (e *LockedError) Error() string {
	return fmt.Sprintf("ops: lock %s is held by operation %s", e.Lock, e.Holder)
}

func checkLocks(c store.Conn, locks []string) error {
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
