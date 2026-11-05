package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// TestImportRefusesEachUnsafeSource: every file and folder the importer
// reads, made writable by others in turn, refuses the import (a hostile
// source is never read, whichever file it is).
func TestImportRefusesEachUnsafeSource(t *testing.T) {
	var rels []string
	walked := fullInbox(t).dir
	_ = filepath.WalkDir(walked, func(p string, d fs.DirEntry, err error) error {
		if rel, rerr := filepath.Rel(walked, p); err == nil && rerr == nil && rel != "." {
			rels = append(rels, rel)
		}
		return nil
	})
	for _, rel := range rels {
		x := fullInbox(t)
		target := filepath.Join(x.dir, rel)
		st, err := os.Lstat(target)
		if err != nil {
			continue // a name of the walk that differs per fixture
		}
		if err := os.Chmod(target, st.Mode().Perm()|0o022); err != nil {
			t.Fatal(err)
		}
		if _, e := x.run(t); e == nil {
			t.Errorf("%s writable by others: imported", rel)
		}
	}
}

// TestImportMalformedCases: each malformed source refuses the import or
// is kept as the evidence it is.
func TestImportMalformedCases(t *testing.T) {
	adj := inbox.Identity{Pane: "p1", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s1"}}
	orch := inbox.Identity{Pane: "p2", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s2"}}
	env := func(id, to, reply, body string) []byte {
		fi, ti := adj, orch
		if to == "adj-1" {
			fi, ti = orch, adj
		}
		from := map[string]string{"orch-1": "adj-1", "adj-1": "orch-1"}[to]
		return prettyJSON(obj("version", 1, "id", id, "from", from, "to", to, "from_identity", identityObj(fi), "to_identity", identityObj(ti),
			"time", 1700000000, "kind", "brief", "reply_to", reply, "body", body))
	}
	refused := map[string]func(*testing.T, *importInbox){
		"peers missing": func(t *testing.T, x *importInbox) { _ = os.Remove(filepath.Join(x.dir, "peers")) },
		"quarantine terminal gone": func(t *testing.T, x *importInbox) {
			_ = os.Remove(filepath.Join(x.dir, "ids", "m000000000004", "terminal.json"))
		},
		"quarantine terminal a list": func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000004/terminal.json", []byte("[]"))
		},
		"claim without owner": func(t *testing.T, x *importInbox) { x.write(t, "orch-1/claim/token", []byte(x.token+"\n")) },
		"envelope to another box": func(t *testing.T, x *importInbox) {
			b := env("m000000000001", "adj-1", "", "body")
			x.write(t, "ids/m000000000001/envelope", b)
			x.write(t, "orch-1/ready/m000000000001.md", b)
		},
		"handled terminal not JSON": func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000002/terminal.json", []byte("{"))
		},
		"handled terminal of another state": func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000002/terminal.json", prettyJSON(obj("id", "m000000000002", "state", "quarantined")))
		},
		"resume names no target":  func(t *testing.T, x *importInbox) { x.write(t, "state/resume-held-", []byte("HELD\n")) },
		"watch targets not JSON":  func(t *testing.T, x *importInbox) { x.write(t, "watch-targets.json", []byte("[")) },
		"watch targets an object": func(t *testing.T, x *importInbox) { x.write(t, "watch-targets.json", prettyJSON(obj())) },
		"resume target a string": func(t *testing.T, x *importInbox) {
			x.write(t, "resume-targets.json", prettyJSON(obj("task-1", "p8")))
		},
		"approval not JSON": func(t *testing.T, x *importInbox) { x.write(t, "state/approval.json", []byte("{")) },
	}
	for name, bad := range refused {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			bad(t, x)
			if _, e := x.run(t); e == nil {
				t.Fatal("imported")
			}
		})
	}
	t.Run("ids writable with no message", func(t *testing.T) {
		x := newImportInbox(t)
		for _, q := range []string{"orch-1/ready", "orch-1/cur", "adj-1/quarantine", "orch-1/ack"} {
			_ = os.RemoveAll(filepath.Join(x.dir, q))
		}
		if err := os.Chmod(filepath.Join(x.dir, "ids"), 0o777); err != nil {
			t.Fatal(err)
		}
		if _, e := x.run(t); e == nil || !strings.Contains(e.Message, "writable") {
			t.Fatalf("ids writable by others: %v", e)
		}
	})
	t.Run("reply and differing quarantine allocation", func(t *testing.T) {
		x := newImportInbox(t)
		b := env("m000000000001", "orch-1", "m000000000004", "a reply")
		x.write(t, "ids/m000000000001/envelope", b)
		x.write(t, "orch-1/ready/m000000000001.md", b)
		x.write(t, "ids/m000000000004/envelope", env("m000000000004", "adj-1", "", "the allocated body"))
		x.write(t, "alerts/a-only.sent", nil)
		c, e := x.run(t)
		if e != nil {
			t.Fatal(e)
		}
		if got := rowsOf(t, c, `SELECT reply_to FROM messages WHERE id = 1`); len(got) != 1 || got[0] != "4" {
			t.Errorf("reply_to %v, want 4", got)
		}
		if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = 'ids/m000000000004/envelope'`); len(got) != 1 || !strings.Contains(got[0], "differs") {
			t.Errorf("differing allocation: %v", got)
		}
		if got := rowsOf(t, c, `SELECT title||' '||payload||' '||state FROM alerts WHERE key = 'a-only'`); len(got) != 1 || got[0] != "a-only {} shown" {
			t.Errorf("alert with no payload: %v", got)
		}
	})
}
