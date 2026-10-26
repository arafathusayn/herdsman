package inbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

type foldedRecord struct {
	Agents  []inbox.Unknown `json:"agents"`
	Omitted int             `json:"omitted"`
}

func readRecord(t *testing.T, p string) foldedRecord {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var r foldedRecord
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Review 5: the omitted counts of several waiting records are summed into
// the published record, beside every kept agent.
func TestR5FoldKeepsOmittedCounts(t *testing.T) {
	c, ch := exposureChannel(t)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	os.MkdirAll(pending, 0o700)
	c.Write(filepath.Join(pending, "1.json"), `{"agents":[{"pane":"rogue-1","agent":"claude","session":null}],"omitted":3,"time":1}`+"\n")
	c.Write(filepath.Join(pending, "2.json"), `{"agents":[{"pane":"rogue-2","agent":"claude","session":null}],"omitted":4,"time":2}`+"\n")
	if err := ch.FoldExposure(); err != nil {
		t.Fatal(err)
	}
	r := readRecord(t, filepath.Join(c.Dir, "state", "boundary-tainted"))
	if len(r.Agents) != 2 || r.Omitted != 7 {
		t.Errorf("folded %d agents, omitted %d; want 2 and 7", len(r.Agents), r.Omitted)
	}
	if ch.ExposurePending() {
		t.Error("the waiting records were not cleared")
	}
}

// Review 5: one agent too large for the bound is kept as a count, waiting
// and then published, never lost.
func TestR5OversizedAgentCounted(t *testing.T) {
	c, ch := exposureChannel(t)
	s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": strings.Repeat("v", inbox.MaxSize)})
	if err := ch.KeepPending([]inbox.Unknown{{Pane: "huge-pane", Agent: "claude", Session: s}}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(c.Dir, "state", "exposure-pending"))
	if len(ents) != 1 {
		t.Fatalf("pending records %d", len(ents))
	}
	if r := readRecord(t, filepath.Join(c.Dir, "state", "exposure-pending", ents[0].Name())); len(r.Agents) != 0 || r.Omitted != 1 {
		t.Errorf("pending kept %d agents, omitted %d; want 0 and 1", len(r.Agents), r.Omitted)
	}
	if err := ch.FoldExposure(); err != nil {
		t.Fatal(err)
	}
	if r := readRecord(t, filepath.Join(c.Dir, "state", "boundary-tainted")); len(r.Agents) != 0 || r.Omitted != 1 {
		t.Errorf("published %d agents, omitted %d; want 0 and 1", len(r.Agents), r.Omitted)
	}
}
