package message

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// XB8, XB9 (PR 24, a38ff72): a busy writer keeps a large exposure as a
// truncated pending record (first twenty agents, or none when even those
// exceed the bound); after those agents exit the authority is HUMAN.
func TestR9BusyWriterKeepsTruncatedRecord(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n, size   int
		agents    int
		wantCount int
	}{
		{"XB8 large", 700, 160, 20, 700},
		{"XB9 long sessions", 21, 7000, 0, 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCase(t, true)
			e := openEnv(t, c, nil)
			var unknown []inbox.Unknown
			for i := range tc.n {
				s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": fmt.Sprintf("s%d-%s", i, strings.Repeat("x", tc.size))})
				unknown = append(unknown, inbox.Unknown{Pane: fmt.Sprintf("rogue-%d", i), Agent: "claude", Session: s})
			}
			os.Mkdir(c.in(inbox.MutationLock), 0o700) // a busy writer
			if err := e.recordExposure(inbox.FaultInventory, unknown); err != nil {
				t.Fatalf("the exposure was not kept: %v", err)
			}
			ents, _ := os.ReadDir(c.in("state", "exposure-pending"))
			if len(ents) != 1 {
				t.Fatalf("pending records %d", len(ents))
			}
			b, ok := inbox.ReadStrict(c.ic.Dir, c.in("state", "exposure-pending", ents[0].Name()))
			var r struct {
				Agents    []any `json:"agents"`
				Truncated bool  `json:"truncated"`
				Count     int   `json:"count"`
			}
			if !ok || json.Unmarshal(b, &r) != nil || len(r.Agents) != tc.agents || !r.Truncated || r.Count != tc.wantCount {
				t.Errorf("pending record: %d agents, truncated %v, count %d", len(r.Agents), r.Truncated, r.Count)
			}
			os.Remove(c.in(inbox.MutationLock))
			if out, code := c.run("receiver-pane", nil, "authority"); code != 3 {
				t.Errorf("the authority after those agents exit: %d %q", code, out)
			}
		})
	}
}

// XB11 (PR 24, a38ff72): a pending folder that lists but cannot be written.
// The watcher publishes the exposure, keeps the entry it could not remove,
// logs it, and frees the input lock: another writer takes it afterwards.
func TestR9ReadonlyPendingWatcher(t *testing.T) {
	c := watchedCase(t)
	p := c.in("state", "exposure-pending")
	os.MkdirAll(p, 0o700)
	c.write(filepath.Join(p, "1-1-1.json"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n")
	chmodT(t, p, 0o500)
	held := false
	base := c.fx.Handler("agent.list")
	c.srv.Handle("agent.list", func(m map[string]any) (any, string) {
		if inbox.Exists(c.in(inbox.MutationLock)) {
			held = true
		}
		return base(m)
	})
	c.watch()
	if b, _ := os.ReadFile(c.in("state", "boundary-tainted")); !strings.Contains(string(b), "rogue-pane") {
		t.Errorf("XB11 the watcher did not publish the exposure: %q", b)
	}
	if held || inbox.Exists(c.in(inbox.MutationLock)) {
		t.Error("XB11b the input lock was kept after the fold")
	}
	os.Chmod(p, 0o700)
	if b, _ := os.ReadFile(filepath.Join(p, "1-1-1.json")); !strings.Contains(string(b), "rogue-pane") {
		t.Error("XB11c the entry it could not remove was not kept")
	}
	logs, _ := filepath.Glob(c.in("state", "diagnostic-*.log"))
	found := false
	for _, l := range logs {
		b, _ := os.ReadFile(l)
		found = found || strings.Contains(string(b), "EXPOSURE a pending entry could not be removed; kept for the human")
	}
	if !found {
		t.Error("the kept entry was not logged")
	}
	if out, code := c.run("receiver-pane", nil, "hold", O, "hold", A, "held for the test"); code != 0 {
		t.Errorf("XB11b another writer could not take the input lock: %d %q", code, out)
	}
}
