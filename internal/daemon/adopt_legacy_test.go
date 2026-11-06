package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMoveLegacy: a file mode file goes to state/legacy/<base>-v1.json,
// or the first free numbered name; a missing file is no move, and a
// state/legacy that is not a folder is an error.
func TestMoveLegacy(t *testing.T) {
	dir := t.TempDir()
	if moved, err := moveLegacy(dir, "setup.json", "setup", ".json"); moved || err != nil {
		t.Fatalf("no file: %v %v", moved, err)
	}
	if err := mkdirs(dir, "state/legacy"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"state/legacy/approval-v1.json", "state/approval.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if moved, err := moveLegacy(dir, "state/approval.json", "approval", ".json"); !moved || err != nil {
		t.Fatalf("second copy: %v %v", moved, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "state", "legacy", "approval-v1.2.json")); err != nil || string(b) != "state/approval.json" {
		t.Errorf("second copy not at approval-v1.2.json: %q %v", b, err)
	}
	other := t.TempDir()
	_ = mkdirs(other, "state")
	_ = os.WriteFile(filepath.Join(other, "state", "legacy"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(other, "setup.json"), nil, 0o600)
	if _, err := moveLegacy(other, "setup.json", "setup", ".json"); err == nil {
		t.Error("state/legacy as a file: no error")
	}
}

// TestAdoptLegacyMoveFails: a failed move of setup.json or of file mode's
// approval.json is an internal error, and the adoption stays pending.
func TestAdoptLegacyMoveFails(t *testing.T) {
	for _, c := range []struct{ name, want string }{{"setup", "old proof"}, {"approval", "approval record"}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.stop()
			dir, sum := fileInbox(t, h)
			// After the import and the marker, just before the move.
			h.cfg.Crash = func(p string) {
				if p != "adopt.marker" {
					return
				}
				if c.name == "approval" {
					_ = os.Remove(filepath.Join(dir, "setup.json"))
					_ = os.WriteFile(filepath.Join(dir, "state", "approval.json"), []byte("{}\n"), 0o600)
				}
				_ = os.WriteFile(filepath.Join(dir, "state", "legacy"), nil, 0o600)
			}
			h.start()
			err := adoptErr(h, "old", dir, sum)
			if code(err) != "internal" || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an internal error on the %s: %v", c.want, err)
			}
			if !exists(filepath.Join(dir, "state", "adopting.json")) {
				t.Error("the adoption is not pending")
			}
		})
	}
}

// TestAdoptPartialWriteRefused: a file-mode write in flight refuses a
// fresh adoption and releases its locks, so a later run adopts.
func TestAdoptPartialWriteRefused(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	partial := filepath.Join(dir, "orch-1", "tmp", "m000000000003.md")
	if err := os.WriteFile(partial, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptErr(h, "old", dir, sum); code(err) != "adopt_locked" {
		t.Fatalf("want adopt_locked: %v", err)
	}
	if err := os.Remove(partial); err != nil {
		t.Fatal(err)
	}
	if err := adoptErr(h, "old", dir, sum); err != nil {
		t.Fatalf("adopt after the write ended: %v", err)
	}
}
