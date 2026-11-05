package inbox_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

func unknownAgents(n, size int) []inbox.Unknown {
	var out []inbox.Unknown
	for i := range n {
		s, _ := json.Marshal(map[string]string{"agent": "claude", "kind": "id", "source": "hook", "value": fmt.Sprintf("s%d-%s", i, strings.Repeat("x", size))})
		out = append(out, inbox.Unknown{Pane: fmt.Sprintf("rogue-%d", i), Agent: "claude", Session: s})
	}
	return out
}

// XB8, XB9 (PR 24, a38ff72): a waiting record keeps every agent when it
// fits; otherwise its first twenty, marked truncated with the count; when
// even those exceed the strict bound, the minimal record (no agents,
// truncated). The fold publishes the agents it got and counts the rest.
func TestR9BoundedPendingTiers(t *testing.T) {
	for _, tc := range []struct {
		name            string
		unknown         []inbox.Unknown
		agents          int
		truncated       bool
		publishedAgents int
	}{
		{"fits", unknownAgents(3, 10), 3, false, 3},
		{"XB8 first twenty", unknownAgents(700, 300), 20, true, 20},
		{"XB9 minimal", unknownAgents(21, 7000), 0, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ch := exposureChannel(t)
			if err := ch.KeepPending(tc.unknown); err != nil {
				t.Fatal(err)
			}
			ents, _ := os.ReadDir(filepath.Join(c.Dir, "state", "exposure-pending"))
			if len(ents) != 1 {
				t.Fatalf("pending records %d", len(ents))
			}
			p := filepath.Join(c.Dir, "state", "exposure-pending", ents[0].Name())
			if _, ok := inbox.ReadStrict(c.Dir, p); !ok {
				t.Fatal("the pending record is not bounded strict JSON")
			}
			r := readRecord(t, p)
			if len(r.Agents) != tc.agents || r.Truncated != tc.truncated || (tc.truncated && r.Count != len(tc.unknown)) {
				t.Errorf("pending kept %d agents, truncated %v, count %d; want %d, %v, %d", len(r.Agents), r.Truncated, r.Count, tc.agents, tc.truncated, len(tc.unknown))
			}
			if err := ch.FoldExposure(); err != nil {
				t.Fatal(err)
			}
			pub := readRecord(t, filepath.Join(c.Dir, "state", "boundary-tainted"))
			if len(pub.Agents) != tc.publishedAgents || pub.Omitted != len(tc.unknown)-tc.publishedAgents {
				t.Errorf("published %d agents, omitted %d; want %d and %d", len(pub.Agents), pub.Omitted, tc.publishedAgents, len(tc.unknown)-tc.publishedAgents)
			}
			if ch.ExposurePending() {
				t.Error("the waiting record was not cleared")
			}
		})
	}
}

// A shell-written truncated record without a count still publishes its
// agents and counts nothing it cannot know.
func TestR9FoldShellTruncatedRecord(t *testing.T) {
	c, ch := exposureChannel(t)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	c.Write(filepath.Join(pending, "1.json"), `{"agents":[],"truncated":true,"time":1}`+"\n")
	c.Write(filepath.Join(pending, "2.json"), `{"agents":[{"pane":"p","agent":"claude","session":null}],"truncated":true,"count":5,"time":1}`+"\n")
	if err := ch.FoldExposure(); err != nil {
		t.Fatal(err)
	}
	if r := readRecord(t, filepath.Join(c.Dir, "state", "boundary-tainted")); len(r.Agents) != 1 || r.Omitted != 4 {
		t.Errorf("published %d agents, omitted %d; want 1 and 4", len(r.Agents), r.Omitted)
	}
}
