package web

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// S16: the web reader shows a contract 3 inbox's registry, mailboxes and
// holds from its route database, read-only, and creates nothing.
func TestReadChannelReadsTheRouteDB(t *testing.T) {
	const epoch = "0123456789abcdef0123456789abcdef"
	base, err := os.MkdirTemp("", "s16w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	dir, path := filepath.Join(base, "inbox"), filepath.Join(base, "ph", "daemon", "routes", "r.db")
	for _, d := range []string{filepath.Dir(path), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	owner := `{"owner":"daemon","epoch":"` + epoch + `","contract":3,"protocol":1,"adopted_from":null,"store":"` + path + `"}`
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := store.Open(path, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitRoute(c, epoch); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('adj', 'adjutant', 'p1', '{}', 1, 'approved', 1, 2)`,
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('orch', 'orchestrator', 'p2', '{}', 1, 'approved', 1, 2)`,
		`INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (1, 'adj', 'orch', 'task', '{}', X'78', 'x', 'ready', 1, 1, 'daemon')`,
		`INSERT INTO holds(target, owner, reason, time) VALUES ('orch', 'adj', 'busy', 1)`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = c.Close()
	list := func() []string {
		var out []string
		_ = filepath.Walk(base, func(p string, _ os.FileInfo, err error) error {
			if err == nil {
				out = append(out, p)
			}
			return nil
		})
		slices.Sort(out)
		return out
	}
	before := list()
	ch := readChannel(dir, time.Now())
	if ch.Error != "" || !ch.Approved || ch.Pending {
		t.Fatalf("error %q approved %v pending %v", ch.Error, ch.Approved, ch.Pending)
	}
	if len(ch.Peers) != 2 || ch.Peers[0] != (ChannelPeer{Name: "adj", Role: "adjutant", Pane: "p1"}) || ch.Peers[1] != (ChannelPeer{Name: "orch", Role: "orchestrator", Pane: "p2"}) {
		t.Fatalf("peers %+v", ch.Peers)
	}
	var orch *ChannelBox
	for i := range ch.Boxes {
		if ch.Boxes[i].Name == "orch" {
			orch = &ch.Boxes[i]
		}
	}
	if orch == nil || orch.Counts["ready"] != 1 || orch.Unacked != 1 || orch.Claim != "none" || orch.Orphan {
		t.Fatalf("boxes %+v", ch.Boxes)
	}
	if len(ch.Holds) != 1 || ch.Holds[0].Name != "hold-orch" || len(ch.Holds[0].Owners) != 1 || ch.Holds[0].Owners[0] != (ChannelHolding{By: "adj", Reason: "busy"}) {
		t.Fatalf("holds %+v", ch.Holds)
	}
	if after := list(); !slices.Equal(before, after) {
		t.Fatalf("the reader created files: %v -> %v", before, after)
	}
}
