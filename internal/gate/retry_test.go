package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round 6 (shell 160c070): an uninspectable pending exposure path (a link
// to an empty folder, a folder whose listing fails) is published under the
// input lock as a generic exposure and the gate answers HUMAN 3; when the
// path is clean again, a retry of the same go still consumes nothing.
func TestR6UninspectablePendingSurvivesRetry(t *testing.T) {
	for _, mode := range []string{"link", "unlistable"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t)
			r.gojson("pending", 600, nil)
			p := filepath.Join(r.c.Dir, "state", "exposure-pending")
			if mode == "link" {
				empty := filepath.Join(r.c.Base, "empty")
				os.Mkdir(empty, 0o700)
				if err := os.Symlink(empty, p); err != nil {
					t.Fatal(err)
				}
			} else {
				os.Mkdir(p, 0o700)
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(p, 0o700)
			}
			first, code := r.gate("consume", r.goFile("pending"), "post", "pr-comment", "head1", r.draft)
			if code != 3 || !strings.Contains(first, "HUMAN") || exists(r.consumed("pending")) {
				t.Fatalf("first go: exit %d %q", code, first)
			}
			if !exists(filepath.Join(r.c.Dir, "state", "boundary-tainted")) {
				t.Fatal("no durable exposure was published")
			}
			// The path becomes clean without new human setup.
			if mode == "link" {
				os.Remove(p)
				os.Mkdir(p, 0o700)
			} else {
				os.Chmod(p, 0o700)
			}
			again, code := r.gate("consume", r.goFile("pending"), "post", "pr-comment", "head1", r.draft)
			if code != 3 || strings.Contains(again, "CONSUME") || exists(r.consumed("pending")) {
				t.Errorf("retry after the path was cleaned: exit %d %q", code, again)
			}
		})
	}
}
