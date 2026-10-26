package inbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Round 6: a failed listing is recorded in the diagnostic log as an
// uninspectable path, not folded as an empty one.
func TestR6UnlistablePathIsLogged(t *testing.T) {
	c, ch := exposureChannel(t)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	c.Write(filepath.Join(pending, "x.json"), `{"agents":[],"time":1}`+"\n")
	if err := os.Chmod(pending, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(pending, 0o700)
	if err := ch.FoldExposure(); err != nil {
		t.Fatal(err)
	}
	logs, _ := filepath.Glob(filepath.Join(c.Dir, "state", "diagnostic-*.log"))
	found := false
	for _, l := range logs {
		b, _ := os.ReadFile(l)
		found = found || strings.Contains(string(b), "cannot be inspected")
	}
	if !found {
		t.Error("the uninspectable pending path was not logged")
	}
}

// Round 6: waiting records folded while an exposure is already published
// leave the published record as it is and are cleared.
func TestR6FoldIntoAnExistingRecord(t *testing.T) {
	c, ch := exposureChannel(t)
	tainted := filepath.Join(c.Dir, "state", "boundary-tainted")
	before := `{"agents":[{"pane":"first","agent":"claude","session":null}],"time":1}` + "\n"
	c.Write(tainted, before)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	c.Write(filepath.Join(pending, "1.json"), `{"agents":[{"pane":"later","agent":"claude","session":null}],"time":2}`+"\n")
	if err := ch.FoldExposure(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tainted); string(b) != before {
		t.Errorf("the published record changed: %q", b)
	}
	if ch.ExposurePending() {
		t.Error("the waiting record was not cleared")
	}
}

// Round 6: a published record that cannot be written refuses the fold and
// keeps the waiting record.
func TestR6FoldPublicationFailure(t *testing.T) {
	c, ch := exposureChannel(t)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	c.Write(filepath.Join(pending, "1.json"), `{"agents":[],"time":1}`+"\n")
	defer inbox.SetFileOps(inbox.FileOps{Rename: func(o, n string) error {
		if filepath.Base(n) == "boundary-tainted" {
			return os.ErrPermission
		}
		return os.Rename(o, n)
	}})()
	if err := ch.FoldExposure(); err == nil {
		t.Fatal("a publication failure was not refused")
	}
	if !ch.ExposurePending() {
		t.Error("the waiting record was lost")
	}
}
