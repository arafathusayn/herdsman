package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/routedb"
)

// TestImportStagingRefusals: a route database path that cannot be
// checked, a staging folder that cannot be made and a staging file that
// cannot be opened each refuse the import with an internal error, and
// leave no staging database.
func TestImportStagingRefusals(t *testing.T) {
	const epoch = "0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		name, want string
		fault      func(t *testing.T, h *harness, routes string)
	}{
		{"route path unchecked", "route database ", func(t *testing.T, h *harness, routes string) {
			if err := os.RemoveAll(routes); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(routes, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"staging folder", "route database folder", func(t *testing.T, h *harness, routes string) {
			if err := os.MkdirAll(routes, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(routes, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(routes, 0o700) })
		}},
		{"staging file", "staging route database", func(t *testing.T, h *harness, routes string) {
			// A non-empty folder where the staging file goes: the leftover
			// cleanup cannot remove it, and the store cannot open it.
			if err := os.MkdirAll(filepath.Join(h.d.stagingPath("old", epoch), "x"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			dir, _ := fileInbox(t, h)
			routes := filepath.Dir(routedb.Path(h.ph, "old"))
			c.fault(t, h, routes)
			e := h.d.importStaging("old", dir, epoch)
			if e == nil || e.Code != "internal" || !strings.Contains(e.Message, c.want) {
				t.Fatalf("want internal %q: %v", c.want, e)
			}
			if st, err := os.Lstat(h.d.stagingPath("old", epoch)); err == nil && !st.IsDir() {
				t.Error("a staging database was left")
			}
		})
	}
}
