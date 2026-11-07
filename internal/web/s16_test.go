package web

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

const s16Epoch = "0123456789abcdef0123456789abcdef"

// s16Owner is the contract 3 owner record with these fields in place of
// the valid ones (raw JSON values); an empty value drops the field.
func s16Owner(path string, fields map[string]string) string {
	f := map[string]string{"owner": `"daemon"`, "epoch": `"` + s16Epoch + `"`, "contract": "3", "protocol": "1",
		"adopted_from": "null", "store": `"` + path + `"`}
	for k, v := range fields {
		f[k] = v
	}
	parts := []string{}
	for _, k := range []string{"owner", "epoch", "contract", "protocol", "adopted_from", "store"} {
		if f[k] != "" {
			parts = append(parts, `"`+k+`":`+f[k])
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// s16Route makes a private home (HERDSMAN_PRIVATE_HOME for the test) with
// route r's database (an approved pair, one ready message for orch, a
// hold) and its inbox of contract 3.
func s16Route(t *testing.T) (base, dir, path string) {
	t.Helper()
	base, err := os.MkdirTemp("", "s16w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	t.Setenv("HERDSMAN_PRIVATE_HOME", filepath.Join(base, "ph"))
	dir, path = filepath.Join(base, "inbox"), filepath.Join(base, "ph", "daemon", "routes", "r.db")
	for _, d := range []string{filepath.Dir(path), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(s16Owner(path, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	s16Routes(t, base, `{"r":{"inbox":"`+dir+`"}}`)
	c, err := store.Open(path, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitRoute(c, s16Epoch); err != nil {
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
	return base, dir, path
}

// s16Routes writes the private home's routes.json with these routes.
func s16Routes(t *testing.T, base, routes string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, "ph", "daemon", "routes.json"), []byte(`{"version":1,"routes":`+routes+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// S16: the web reader shows a contract 3 inbox's registry, mailboxes and
// holds from its route database, read-only, and creates nothing.
func TestReadChannelReadsTheRouteDB(t *testing.T) {
	base, dir, _ := s16Route(t)
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

// S16 fix 1: a refused owner record that names the route database is an
// error with nothing read, never the file reader. The store path must be
// <private home>/daemon/routes/<route>.db, as `message status` requires.
func TestReadChannelRefusedOwnerRecord(t *testing.T) {
	cases := map[string]func(t *testing.T, base, owner, path string){}
	write := func(s string) func(*testing.T, string, string, string) {
		return func(t *testing.T, _, owner, _ string) {
			if err := os.WriteFile(owner, []byte(s), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fields := map[string]map[string]string{
		"relative store":  {"store": `"daemon/routes/r.db"`},
		"no store":        {"store": ""},
		"string contract": {"contract": `"3"`},
		"contract 4":      {"contract": "4"},
	}
	for name, f := range fields {
		cases[name] = func(t *testing.T, base, owner, path string) { write(s16Owner(path, f))(t, base, owner, path) }
	}
	cases["unclean store"] = func(t *testing.T, base, owner, path string) {
		write(s16Owner(path, map[string]string{"store": `"` + filepath.Dir(path) + `/./r.db"`}))(t, base, owner, path)
	}
	cases["not a .db"] = func(t *testing.T, base, owner, path string) {
		write(s16Owner(path, map[string]string{"store": `"` + filepath.Join(filepath.Dir(path), "r.sqlite") + `"`}))(t, base, owner, path)
	}
	cases["another home"] = func(t *testing.T, base, owner, path string) {
		write(s16Owner(path, map[string]string{"store": `"` + filepath.Join(base, "other", "daemon", "routes", "r.db") + `"`}))(t, base, owner, path)
	}
	cases["duplicate key"] = func(t *testing.T, base, owner, path string) {
		write(strings.Replace(s16Owner(path, nil), `"contract":3`, `"contract":3,"contract":3`, 1))(t, base, owner, path)
	}
	cases["not json"] = write(`{"owner":"daemon",`)
	cases["mode 644"] = func(t *testing.T, _, owner, _ string) {
		if err := os.Chmod(owner, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases["symlink"] = func(t *testing.T, base, owner, path string) {
		real := filepath.Join(base, "owner-real.json")
		if err := os.WriteFile(real, []byte(s16Owner(path, nil)), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(owner)
		if err := os.Symlink(real, owner); err != nil {
			t.Fatal(err)
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			base, dir, path := s16Route(t)
			setup(t, base, filepath.Join(dir, "state", "owner.json"), path)
			// A legacy mailbox the file reader would show.
			if err := os.MkdirAll(filepath.Join(dir, "orch", "new"), 0o700); err != nil {
				t.Fatal(err)
			}
			ch := readChannel(dir, time.Now())
			if !strings.HasPrefix(ch.Error, "owner record: ") || len(ch.Peers) != 0 || len(ch.Boxes) != 0 || ch.Approved {
				t.Fatalf("error %q peers %+v boxes %+v approved %v", ch.Error, ch.Peers, ch.Boxes, ch.Approved)
			}
		})
	}
}
