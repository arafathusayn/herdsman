package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestSourceDirRootFirst: the walk goes from root down, so a linked
// parent is refused before anything below it is looked up: a writable
// folder outside the inbox behind the link is never what is reported.
func TestSourceDirRootFirst(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.Chmod(root, 0o700)
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(outside, "child"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	err := SourceDir(root, filepath.Join(root, "link", "child"))
	if err == nil || !strings.Contains(err.Error(), filepath.Join(root, "link")+" is a link") {
		t.Fatalf("got %v, want the link refused before its child is looked up", err)
	}
	if _, err := ReadSource(root, filepath.Join(root, "link", "child", "f"), 10); err == nil || !strings.Contains(err.Error(), " is a link") {
		t.Fatalf("ReadSource: %v", err)
	}
}
