package inbox_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Round 7 (shell 160c070 pending_state): none only when the path is absent
// or a real folder this process can read and search with no entry; entries
// when such a folder holds any entry; uninspectable (pending, published
// generically and kept) for everything else.
func TestR7PendingStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, p string)
		pending bool
	}{
		{"absent", func(t *testing.T, p string) {}, false},
		{"empty-0700", func(t *testing.T, p string) { os.Mkdir(p, 0o700) }, false},
		{"empty-0500", func(t *testing.T, p string) { os.Mkdir(p, 0o500) }, false},
		{"empty-0400", func(t *testing.T, p string) { os.Mkdir(p, 0o400) }, true},
		{"empty-0300", func(t *testing.T, p string) { os.Mkdir(p, 0o300) }, true},
		{"empty-0100", func(t *testing.T, p string) { os.Mkdir(p, 0o100) }, true},
		{"file", func(t *testing.T, p string) { os.WriteFile(p, []byte("x\n"), 0o600) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ch := exposureChannel(t)
			p := filepath.Join(c.Dir, "state", "exposure-pending")
			tc.setup(t, p)
			defer os.Chmod(p, 0o700)
			if got := ch.ExposurePending(); got != tc.pending {
				t.Fatalf("ExposurePending = %v, want %v", got, tc.pending)
			}
			if err := ch.FoldExposure(); err != nil {
				t.Fatal(err)
			}
			if got := exists(filepath.Join(c.Dir, "state", "boundary-tainted")); got != tc.pending {
				t.Errorf("published %v, want %v", got, tc.pending)
			}
			if tc.pending {
				if _, err := os.Lstat(p); err != nil {
					t.Error("the uninspectable path was not kept")
				}
			}
		})
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }
