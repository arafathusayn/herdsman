package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round 7 (shell 160c070 pending_state): a pending folder that this process
// can list but not search (0400), or search but not list (0300), empty or
// not, is uninspectable: the gate publishes a generic exposure and answers
// HUMAN 3, and a retry after chmod 0700 still answers HUMAN 3 with no
// consumption.
func TestR7UnsearchablePendingSurvivesRetry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  os.FileMode
		entry bool
	}{
		{"0400-empty", 0o400, false},
		{"0400-entry", 0o400, true},
		{"0300-empty", 0o300, false},
		{"0300-entry", 0o300, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.gojson("pending", 600, nil)
			p := filepath.Join(r.c.Dir, "state", "exposure-pending")
			os.MkdirAll(p, 0o700)
			if tc.entry {
				os.WriteFile(filepath.Join(p, "1.json"), []byte(`{"agents":[{"pane":"rogue","agent":"claude","session":null}],"time":1}`+"\n"), 0o600)
			}
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(p, 0o700)
			first, code := r.gate("consume", r.goFile("pending"), "post", "pr-comment", "head1", r.draft)
			if code != 3 || !strings.Contains(first, "HUMAN") || exists(r.consumed("pending")) {
				t.Fatalf("first go: exit %d %q", code, first)
			}
			if !exists(filepath.Join(r.c.Dir, "state", "boundary-tainted")) {
				t.Fatal("no durable exposure was published")
			}
			os.Chmod(p, 0o700)
			if tc.entry {
				if _, err := os.Stat(filepath.Join(p, "1.json")); err != nil {
					t.Error("the uninspected entry was removed")
				}
			}
			again, code := r.gate("consume", r.goFile("pending"), "post", "pr-comment", "head1", r.draft)
			if code != 3 || strings.Contains(again, "CONSUME") || exists(r.consumed("pending")) {
				t.Errorf("retry after chmod 0700: exit %d %q", code, again)
			}
		})
	}
}
