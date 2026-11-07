package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// sourceTree is a safe root with a/f ("12345").
func sourceTree(t *testing.T) string {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.Chmod(root, 0o700)
	if err := os.Mkdir(filepath.Join(root, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "f"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestSourceRefusals: each unsafe source is refused with its reason.
func TestSourceRefusals(t *testing.T) {
	root := sourceTree(t)
	a, f := filepath.Join(root, "a"), filepath.Join(root, "a", "f")
	mk := func(fn func() error) {
		t.Helper()
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	}
	mk(func() error { return os.Symlink(f, filepath.Join(a, "l")) })
	mk(func() error { return syscall.Mkfifo(filepath.Join(a, "p"), 0o600) })
	mk(func() error { return os.WriteFile(filepath.Join(a, "w"), nil, 0o600) })
	mk(func() error { return os.Chmod(filepath.Join(a, "w"), 0o622) })
	mk(func() error { return os.WriteFile(filepath.Join(a, "z"), nil, 0) })
	mk(func() error { return os.Mkdir(filepath.Join(a, "shut"), 0o700) })
	mk(func() error { return os.Mkdir(filepath.Join(a, "shut", "in"), 0o700) })
	mk(func() error { return os.Chmod(filepath.Join(a, "shut"), 0) })
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(a, "shut"), 0o700) })
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"dir not under root", SourceDir(root, "/elsewhere"), "is not under"},
		{"dir sharing a prefix", SourceDir(root, root+"x"), "is not under"},
		{"unclean dir", SourceDir(root, root+"/a/../a"), "not a clean path"},
		{"file as a folder", SourceDir(root, f), "is not a folder"},
		{"unsearchable folder", SourceDir(root, filepath.Join(a, "shut", "in")), "permission denied"},
		{"root is a link", SourceDir(filepath.Join(a, "l"), filepath.Join(a, "l")), "is a link"},
		{"file link", errOf(ReadSource(root, filepath.Join(a, "l"), 10)), "is a link"},
		{"FIFO", errOf(ReadSource(root, filepath.Join(a, "p"), 10)), "not a regular file"},
		{"writable file", errOf(ReadSource(root, filepath.Join(a, "w"), 10)), "writable by group or others"},
		{"unreadable file", errOf(ReadSource(root, filepath.Join(a, "z"), 10)), "permission denied"},
		{"too large", errOf(ReadSource(root, f, 4)), "larger than 4 bytes"},
		{"read under a bad folder", errOf(ReadSource(root, filepath.Join(f, "x"), 10)), "is not a folder"},
		{"list a bad folder", errOf(ListSource(root, f)), "is not a folder"},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, tc.err, tc.want)
		}
	}
	if _, err := ReadSource(root, filepath.Join(a, "missing"), 10); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v, want ErrNotExist", err)
	}
	if b, err := ReadSource(root, f, 5); err != nil || string(b) != "12345" {
		t.Errorf("ReadSource = %q %v", b, err)
	}
	if err := SourceDir(root, root); err != nil {
		t.Errorf("SourceDir(root, root) = %v", err)
	}
}

func errOf[T any](_ T, err error) error { return err }
