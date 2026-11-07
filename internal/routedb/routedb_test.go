package routedb

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// TestClassifyTable: each owner record is read as files, as the store, or
// refused with its reason.
func TestClassifyTable(t *testing.T) {
	for _, tc := range []struct {
		name, owner, route string
		mode               Mode
		why                string
	}{
		{"contract 3", "", "r", Store, ""},
		{"contract 3, any route", "", "", Store, ""},
		{"contract 2", `{"owner":"daemon","contract":2}`, "r", Files, ""},
		{"no record", "-", "r", Files, ""},
		{"another route's store", "", "b", Refused, "names store"},
		{"contract 4", `{"owner":"daemon","contract":4}`, "r", Refused, "contract 4"},
		{"not the daemon", `{"owner":"me","contract":3}`, "r", Refused, "not a daemon owner record"},
		{"string contract", `{"owner":"daemon","contract":"3"}`, "r", Refused, "not a daemon owner record"},
		{"not JSON", `{`, "r", Refused, "strict JSON"},
		{"mode 644", "644", "r", Refused, "mode 644"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ph, dir, db := fixture(t, "r")
			owner := filepath.Join(dir, "state", "owner.json")
			switch tc.owner {
			case "":
			case "-":
				_ = os.Remove(owner)
			case "644":
				_ = os.Chmod(owner, 0o644)
			default:
				if err := os.WriteFile(owner, []byte(tc.owner), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mode, p, err := Classify(dir, ph, tc.route)
			if mode != tc.mode || (tc.why == "" && err != nil) || (tc.why != "" && (err == nil || !strings.Contains(err.Error(), tc.why))) {
				t.Fatalf("Classify = %v %q %v, want %v %q", mode, p, err, tc.mode, tc.why)
			}
			if mode == Store && p != db {
				t.Errorf("store %q, want %q", p, db)
			}
		})
	}
}

func writeRoutes(t *testing.T, ph, v string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ph, "daemon", "routes.json"), []byte(v), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestClassifyInbox: the route comes from routes.json by inbox; a record
// naming another route's database, an inbox no route lists, a missing or
// invalid routes.json are refused; files mode needs no routes.json.
func TestClassifyInbox(t *testing.T) {
	ph, dir, db := fixture(t, "r")
	if mode, _, err := ClassifyInbox(dir, ph); mode != Refused || err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("no routes.json: %v %v", mode, err)
	}
	writeRoutes(t, ph, `{`)
	if mode, _, err := ClassifyInbox(dir, ph); mode != Refused || err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Errorf("invalid routes.json: %v %v", mode, err)
	}
	writeRoutes(t, ph, `{"version":1,"routes":{"r":{"inbox":"/elsewhere"}}}`)
	if mode, _, err := ClassifyInbox(dir, ph); mode != Refused || err == nil || !strings.Contains(err.Error(), "no route") {
		t.Errorf("unlisted inbox: %v %v", mode, err)
	}
	writeRoutes(t, ph, `{"version":1,"routes":{"b":{"inbox":"`+dir+`"}}}`)
	if mode, _, err := ClassifyInbox(dir, ph); mode != Refused || err == nil || !strings.Contains(err.Error(), "names store") {
		t.Errorf("another route's database: %v %v", mode, err)
	}
	writeRoutes(t, ph, `{"version":1,"routes":{"r":{"inbox":"`+dir+`","repo":"x"}}}`)
	if mode, p, err := ClassifyInbox(dir, ph); mode != Store || p != db || err != nil {
		t.Errorf("listed inbox: %v %q %v", mode, p, err)
	}
	_ = os.Remove(filepath.Join(dir, "state", "owner.json"))
	_ = os.Remove(filepath.Join(ph, "daemon", "routes.json"))
	if mode, _, err := ClassifyInbox(dir, ph); mode != Files || err != nil {
		t.Errorf("file mode: %v %v", mode, err)
	}
}

// TestPrivateHome: the option, then HERDSMAN_PRIVATE_HOME, then the home
// folder's .herdsman-private.
func TestPrivateHome(t *testing.T) {
	env := map[string]string{"HERDSMAN_PRIVATE_HOME": "/env"}
	if p, err := PrivateHome("/opt", func(k string) string { return env[k] }); p != "/opt" || err != nil {
		t.Errorf("option: %q %v", p, err)
	}
	if p, err := PrivateHome("", func(k string) string { return env[k] }); p != "/env" || err != nil {
		t.Errorf("environment: %q %v", p, err)
	}
	home, _ := os.UserHomeDir()
	if p, err := PrivateHome("", func(string) string { return "" }); p != filepath.Join(home, ".herdsman-private") || err != nil {
		t.Errorf("default: %q %v", p, err)
	}
	t.Setenv("HOME", "")
	if _, err := PrivateHome("", func(string) string { return "" }); err == nil {
		t.Error("no home folder: no error")
	}
}

// TestRead: the status of a route database, with holds grouped by target
// and owners in order; a missing file wraps os.ErrNotExist and nothing is
// created.
func TestRead(t *testing.T) {
	_, _, db := fixture(t, "r")
	if _, err := Read(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database: %v", err)
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Read created the database")
	}
	c, err := store.Open(db, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitRoute(c, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('adj', 'adjutant', 'p1', '{}', 1, 'approved', 1, 2)`,
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at) VALUES ('orch', 'orchestrator', 'p2', '{}', 1, 'approved', 1, 2)`,
		`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at) VALUES ('adj2', 'adjutant', 'p3', '{}', 2, 'pending', 3)`,
		`INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (1, 'adj', 'orch', 'task', '{}', X'78', 'x', 'cur', 1, 1, 'daemon')`,
		`INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (2, 'adj', 'ghost', 'task', '{}', X'78', 'x', 'quarantined', 2, 1, 'daemon')`,
		`INSERT INTO messages(id, sender, recipient, kind, envelope, body, body_sha256, state, created_seq, created_at, origin) VALUES (3, 'orch', 'adj', 'task', '{}', X'78', 'x', 'new', 3, 1, 'daemon')`,
		`INSERT INTO holds(target, owner, reason, time) VALUES ('orch', 'adj', 'busy', 1)`,
		`INSERT INTO holds(target, owner, reason, time) VALUES ('orch', 'zed', 'later', 2)`,
		`INSERT INTO holds(target, owner, reason, time) VALUES ('adj', 'orch', 'x', 3)`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = c.Close()
	st, err := Read(db)
	if err != nil {
		t.Fatal(err)
	}
	if st.Approved || st.PendingSHA256 == "" || len(st.Peers) != 2 {
		t.Errorf("registry: approved %v pending %q peers %+v", st.Approved, st.PendingSHA256, st.Peers)
	}
	boxes := map[string]Box{}
	for _, b := range st.Boxes {
		boxes[b.Name] = b
	}
	if b := boxes["orch"]; b.Cur != 1 || b.Unacked != 1 || b.Orphan {
		t.Errorf("orch box %+v", b)
	}
	if b := boxes["ghost"]; b.Quarantine != 1 || !b.Orphan {
		t.Errorf("ghost box %+v", b)
	}
	if b := boxes["adj"]; b.New != 1 {
		t.Errorf("adj box %+v", b)
	}
	if len(st.Holds) != 2 || st.Holds[0].Target != "adj" || st.Holds[1].Target != "orch" ||
		len(st.Holds[1].Owners) != 2 || st.Holds[1].Owners[0] != (HoldOwner{Owner: "adj", Reason: "busy", Time: 1}) || st.Holds[1].Owners[1].Owner != "zed" {
		t.Errorf("holds %+v", st.Holds)
	}
}
