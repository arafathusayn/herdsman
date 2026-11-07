//go:build darwin || linux

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckHeldSkipsParked: a descriptor the checked open parked holds
// the inode but is no SQLite connection, so checkHeld must not count it
// as one. Open's kept descriptor holds the database; the checked open
// refuses the database at mode 644 and parks that descriptor (closing it
// would drop the kept inode's locks). checkHeld refuses before the park
// and must still refuse after it (S1-1 F1: a file swapped in and out
// again while SQLite opened it).
func TestCheckHeldSkipsParked(t *testing.T) {
	p := filepath.Join(dir(t), "h.db")
	w, err := Open(p, Writer, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	openMu.Lock()
	f, release, err := keepOpen(p, false)
	if err != nil {
		openMu.Unlock()
		t.Fatal(err)
	}
	before := checkHeld(p, f)
	openMu.Unlock()
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if tryCheckedOpen(p) {
		t.Fatal("the checked open accepted a mode 644 database")
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if n := closeParked(); n != 1 {
		t.Fatalf("%d descriptors parked after the refused open of a kept inode, want 1", n)
	}
	openMu.Lock()
	after := checkHeld(p, f)
	openMu.Unlock()
	// The -wal and -shm a writer leaves are held by nothing either; the
	// refusal must name the database itself, as it did before the park.
	if want := p + ": SQLite does not hold"; before == nil || !strings.Contains(before.Error(), want) {
		t.Fatalf("before the park: %v, want %q", before, want)
	} else if after == nil || !strings.Contains(after.Error(), want) {
		t.Errorf("after the park: %v, want %q (the parked descriptor counted as SQLite holding the database)", after, want)
	}
	release()
	if n := closeParked(); n != 0 {
		t.Errorf("%d refused descriptors still parked after the release", n)
	}
}
