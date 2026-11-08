package wtree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// TrashDir is where worktrees wait to be deleted.
func TrashDir(common string) string {
	return filepath.Join(common, StateFolder, "trash")
}

// MoveToTrash renames a worktree into the trash, which is instant whatever the
// tree's size. Across file systems the rename fails and the tree stays put.
func MoveToTrash(common, path, name string) (string, error) {
	trash := TrashDir(common)
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(trash, name)
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}

// StartDrain deletes a trashed tree in a detached process at low priority, so
// the deletion neither blocks the caller nor stops with it.
func StartDrain(common, trashed string) error {
	if !strings.HasPrefix(trashed, TrashDir(common)+string(filepath.Separator)) {
		return fmt.Errorf("drain: %s is not in the trash", trashed)
	}
	cmd := exec.Command("nice", "-n", "19", "rm", "-rf", "--", trashed)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
