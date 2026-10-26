package gate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Round 6: a waiting exposure the gate cannot publish under the input lock
// refuses the consumption with exit 2.
func TestR6GatePublicationFailureRefuses(t *testing.T) {
	r := newRig(t)
	r.gojson("pubfail", 600, nil)
	p := filepath.Join(r.c.Dir, "state", "exposure-pending")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, "1.json"), []byte(`{"agents":[],"time":1}`+"\n"), 0o600)
	defer inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if filepath.Base(n) == "boundary-tainted" {
			return os.ErrPermission
		}
		return os.Rename(o, n)
	}})()
	out, code := r.gate("consume", r.goFile("pubfail"), "post", "pr-comment", "head1", r.draft)
	if code != 2 || exists(r.consumed("pubfail")) {
		t.Errorf("an unpublishable exposure did not refuse the consumption: exit %d %q", code, out)
	}
}
