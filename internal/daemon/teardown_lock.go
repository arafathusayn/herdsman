package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/ledger"
)

// lockTeardown holds one task's teardown for this process. The kernel drops
// the lock if the process dies, so a crash never leaves a task locked.
func lockTeardown(commonDir, task string) (func(), error) {
	folder, err := ledger.LogFolder(commonDir, task)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(folder, "teardown.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("teardown lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("a teardown of task %q is already running", task)
		}
		return nil, fmt.Errorf("teardown lock: %w", err)
	}
	return func() { file.Close() }, nil
}
