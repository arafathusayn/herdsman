package daemon

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

// startDrain deletes a trashed tree in a detached process at low priority, so
// the deletion neither blocks the caller nor stops with it.
func startDrain(common, trashed string) error {
	if !strings.HasPrefix(trashed, wtree.TrashDir(common)+string(filepath.Separator)) {
		return fmt.Errorf("drain: %s is not in the trash", trashed)
	}
	cmd := exec.Command("nice", "-n", "19", "rm", "-rf", "--", trashed)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
