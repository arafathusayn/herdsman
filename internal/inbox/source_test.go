package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSourceDirChecksParentsOfMissing: a missing folder is reported as
// absent only when every existing folder above it passes; an unsafe or
// linked parent is reported as itself.
func TestSourceDirChecksParentsOfMissing(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.Chmod(root, 0o700)
	if err := os.Mkdir(filepath.Join(root, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SourceDir(root, filepath.Join(root, "a", "b", "c")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("safe parents: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, "a"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := SourceDir(root, filepath.Join(root, "a", "b")); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writable parent of a missing folder: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "l")); err != nil {
		t.Fatal(err)
	}
	if err := SourceDir(root, filepath.Join(root, "l", "b")); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linked parent of a missing folder: %v", err)
	}
}
