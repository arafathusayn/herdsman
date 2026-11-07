package inbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// swapRoot is an inbox root with root/a/f ("inside") and a folder outside
// it with a/f ("outside-secret"); swap replaces root/a with a link to the
// outside folder's a, as an agent racing the importer would.
func swapRoot(t *testing.T) (root string, swap func()) {
	t.Helper()
	root, _ = filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	for _, d := range []string{root, outside} {
		_ = os.Chmod(d, 0o700)
	}
	for d, body := range map[string]string{root: "inside", outside: "outside-secret"} {
		if err := os.Mkdir(filepath.Join(d, "a"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a", "f"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, func() {
		if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a.orig")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(filepath.Join(outside, "a"), filepath.Join(root, "a")); err != nil {
			t.Error(err)
		}
	}
}

// TestReadSourceAncestorSwap: a checked folder replaced by a link to an
// outside folder after the checks and before the file open is never
// followed: the file is opened under the checked folder's descriptor.
func TestReadSourceAncestorSwap(t *testing.T) {
	root, swap := swapRoot(t)
	sourceBeforeOpen = swap
	defer func() { sourceBeforeOpen = nil }()
	b, err := ReadSource(root, filepath.Join(root, "a", "f"), 100)
	if string(b) == "outside-secret" {
		t.Fatal("ReadSource followed a folder swapped for a link after its check")
	}
	if err != nil || string(b) != "inside" {
		t.Errorf("ReadSource = %q %v, want the checked folder's file", b, err)
	}
}

// TestListSourceAncestorSwap: the listing reads the checked folder, not a
// link swapped in after its check.
func TestListSourceAncestorSwap(t *testing.T) {
	root, swap := swapRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a", "g"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceBeforeOpen = swap
	defer func() { sourceBeforeOpen = nil }()
	names, err := ListSource(root, filepath.Join(root, "a"))
	if err != nil || !slices.Equal(names, []string{"f", "g"}) {
		t.Errorf("ListSource = %v %v, want [f g] of the checked folder", names, err)
	}
}
