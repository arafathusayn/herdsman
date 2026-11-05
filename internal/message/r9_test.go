package message

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// logs it, and frees the input lock at once: while the watcher still runs
// (its event wait), another writer takes the lock and completes.
func TestR9ReadonlyPendingWatcher(t *testing.T) {
	c := watchedCase(t)
	p := c.in("state", "exposure-pending")
	os.MkdirAll(p, 0o700)
	c.write(filepath.Join(p, "1-1-1.json"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n")
	chmodT(t, p, 0o500)
	s := &whileRunning{c: c, t: t}
	c.events = s
	defer func() { c.events = nil }()
	if out, code := c.run("courier-pane", map[string]string{"RUN_FOR": "60", "INTERVAL": "1"}, "card-watch"); code != 0 {
		t.Fatalf("watcher: %d %q", code, out)
	}
	if !s.held {
		t.Fatal("XB11b the other writer did not complete while the watcher ran")
	}
	if b, _ := os.ReadFile(c.in("state", "boundary-tainted")); !strings.Contains(string(b), "rogue-pane") {
		t.Errorf("XB11 the watcher did not publish the exposure: %q", b)
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
}

// whileRunning is the watcher's event stream: at its first wait, while the
// watcher holds its own lock, a hold writer must take the input lock.
type whileRunning struct {
	c    *tc
	t    *testing.T
	held bool
	done bool
}

func (s *whileRunning) Open(context.Context, []map[string]any) (Stream, error) { return s, nil }
func (s *whileRunning) Close()                                                 {}
func (s *whileRunning) Next(d time.Duration) (string, error) {
	if !s.done {
		s.done = true
		if !inbox.Exists(s.c.in("watch.lock")) {
			s.t.Error("the watcher is not running")
		}
		out, code := s.c.run("receiver-pane", map[string]string{"LOCK_WAIT": "1"}, "hold", O, "hold", A, "held while the watcher runs")
		if code != 0 {
			s.t.Errorf("XB11b another writer could not take the input lock while the watcher ran: %d %q", code, out)
		}
		s.held = code == 0
	}
	s.c.now = s.c.now.Add(d)
	return "", errTimeout
}

// A large inventory exposure recorded with the input lock free is
// published at once as a bounded record: the agents that fit, the rest
// counted (round 4's bound, kept for the published record).
func TestR9LargeInventoryPublishedBounded(t *testing.T) {
	c := newCase(t, true)
	e := openEnv(t, c, nil)
	var unknown []inbox.Unknown
	for i := range 700 {
		s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": fmt.Sprintf("s%d-%s", i, strings.Repeat("x", 300))})
		unknown = append(unknown, inbox.Unknown{Pane: fmt.Sprintf("rogue-%d", i), Agent: "claude", Session: s})
	}
	if err := e.recordExposure(inbox.FaultInventory, unknown); err != nil {
		t.Fatal(err)
	}
	b, ok := inbox.ReadStrict(c.ic.Dir, c.in("state", "boundary-tainted"))
	var r struct {
		Agents  []any `json:"agents"`
		Omitted int   `json:"omitted"`
	}
	if !ok || json.Unmarshal(b, &r) != nil || len(r.Agents) == 0 || len(r.Agents)+r.Omitted != 700 {
		t.Errorf("published %d agents, omitted %d (strict %v)", len(r.Agents), r.Omitted, ok)
	}
}
