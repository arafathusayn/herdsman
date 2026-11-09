package wtree

import (
	"os"
	"path/filepath"
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
