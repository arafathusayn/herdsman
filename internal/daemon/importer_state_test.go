package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// withState adds the route state of S14 to the fixture inbox: a hold with
// two owners, alerts in every state, a relayed card, both target files
// and a contract 2 inbox's daemon files.
func (x *importInbox) withState(t *testing.T) {
	t.Helper()
	x.write(t, "state/hold-orch-1", prettyJSON(obj("holds", obj("adj-1", obj("reason", "wait", "time", 1700000200), "human", obj("reason", "review", "time", 1700000300)))))
	pay := func(title string) []byte { return prettyJSON(obj("title", title, "body", "b")) }
	x.write(t, "alerts/a-pend.pending", pay("pending one"))
	x.write(t, "alerts/a-pend.failures", []byte("2\n"))
	x.write(t, "alerts/a-pend.attempt", nil)
	x.write(t, "alerts/a-pend.resolved", pay("raised before"))
	x.write(t, "alerts/a-shown.pending", pay("shown one"))
	x.write(t, "alerts/a-shown.sent", nil)
	x.write(t, "alerts/a-res.resolved", pay("resolved one"))
	x.write(t, "alerts/a-queued.queued-00ff", pay("queued one"))
	x.write(t, "alerts/a-retry.failures", []byte("1\n"))
	x.write(t, "alerts/.tmp-1", []byte("half"))
	_ = mkdirs(x.dir, "alerts/a-pend.lock")
	src := obj("pane", "p9", "agent", "claude")
	x.write(t, "card-events/ck.123.json", prettyJSON(obj("version", 1, "type", "side_agent_data", "id", "ck.123", "source", src, "text", "card text")))
	x.write(t, "card-events/ck.456", nil)
	x.write(t, "state/card-ck.123.queued", []byte("m000000000003 1700000400\n"))
	x.write(t, "watch-targets.json", prettyJSON([]any{obj("pane", "p7", "session", obj("agent", "claude"))}))
	x.write(t, "resume-targets.json", prettyJSON(obj("task-1", obj("role", "task-agent", "pane", "p8"))))
	x.write(t, "state/owner.json", []byte(`{"contract":2}`))
	x.write(t, "state/events/current.ndjson", []byte("{}\n"))
	x.write(t, "state/approval.json", []byte(`{"sha256":"x"}`))
	x.write(t, "state/credentials.json", []byte(`{}`))
	x.write(t, "state/credentials/adj-1", []byte("secret"))
	x.write(t, "state/proof-stale", []byte("stale\n"))
}

// TestImportState: holds by owner, alerts with their states, the card
// with its relay, the targets, and every state file read in import_log.
func TestImportState(t *testing.T) {
	x := newImportInbox(t)
	x.withState(t)
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT target||' '||owner||' '||reason||' '||time FROM holds ORDER BY owner`), "; "); got != "orch-1 adj-1 wait 1700000200; orch-1 human review 1700000300" {
		t.Fatalf("holds: %s", got)
	}
	want := "a-pend pending pending one; a-queued pending queued one; a-res resolved resolved one; a-shown shown shown one"
	if got := strings.Join(rowsOf(t, c, `SELECT key||' '||state||' '||title FROM alerts ORDER BY key`), "; "); got != want {
		t.Fatalf("alerts: %s, want %s", got, want)
	}
	if got := rowsOf(t, c, `SELECT payload FROM alerts WHERE key = 'a-pend'`); len(got) != 1 || !strings.Contains(got[0], `"pending one"`) {
		t.Fatalf("alert payload: %v", got)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT episode||' '||relayed_id||' '||headline||' '||source FROM cards`), "; "); got != `ck.123 3 card text {"agent":"claude","pane":"p9"}` {
		t.Fatalf("cards: %s", got)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT kind||' '||name||' '||identity FROM targets ORDER BY kind`), "; "); got != `resume task-1 {"pane":"p8","role":"task-agent"}; watch p7 {"pane":"p7","session":{"agent":"claude"}}` {
		t.Fatalf("targets: %s", got)
	}
	logged := map[string]string{}
	_ = c.Query(`SELECT path, disposition FROM import_log`, nil, func(r store.Row) error { logged[r.Text(0)] = r.Text(1); return nil })
	for p, d := range map[string]string{
		"state/hold-orch-1":           "holds",
		"alerts/a-shown.sent":         "alerts shown",
		"alerts/a-pend.failures":      "not imported: alert retry state of the file channel",
		"alerts/a-pend.lock":          "not imported: alert retry state of the file channel",
		"alerts/a-retry.failures":     "not imported: alert retry state of the file channel",
		"alerts/a-queued.queued-00ff": "alerts",
		"alerts/.tmp-1":               "partial-write",
		"card-events/ck.123.json":     "cards",
		"card-events/ck.456":          "partial-write",
		"state/card-ck.123.queued":    "cards relayed",
		"watch-targets.json":          "targets",
		"resume-targets.json":         "targets",
		"state/owner.json":            "not imported: owner.json is written again at publish",
		"state/events/current.ndjson": "not imported: a new epoch starts",
		"state/approval.json":         "legacy evidence: the contract 2 approval",
		"state/credentials/adj-1":     "not imported: secrets are reissued at the next approval",
		"state/proof-stale":           "legacy evidence: the route is HUMAN until setup writes version 3",
	} {
		if logged[p] != d {
			t.Errorf("import_log %s: %q, want %q", p, logged[p], d)
		}
	}
}

// TestImportStateRefusals: a malformed state file refuses the import,
// never imports a guess.
func TestImportStateRefusals(t *testing.T) {
	for name, bad := range map[string]func(*testing.T, *importInbox){
		"hold not owner format": func(t *testing.T, x *importInbox) {
			x.write(t, "state/hold-orch-1", prettyJSON(obj("holds", obj("adj-1", obj("reason", "wait")))))
		},
		"hold extra key": func(t *testing.T, x *importInbox) {
			x.write(t, "state/hold-orch-1", prettyJSON(obj("holds", obj(), "x", 1)))
		},
		"alert payload": func(t *testing.T, x *importInbox) {
			x.write(t, "alerts/a.pending", prettyJSON(obj("title", "t")))
		},
		"alert link": func(t *testing.T, x *importInbox) {
			x.write(t, "outside", prettyJSON(obj("title", "t", "body", "b")))
			if err := os.Symlink(filepath.Join(x.dir, "outside"), filepath.Join(x.dir, "alerts", "a.pending")); err != nil {
				t.Fatal(err)
			}
		},
		"alerts folder link": func(t *testing.T, x *importInbox) {
			// Empty: only the folder check sees it, as no file under it is read.
			_ = mkdirs(x.dir, "elsewhere")
			_ = os.Remove(filepath.Join(x.dir, "alerts"))
			if err := os.Symlink(filepath.Join(x.dir, "elsewhere"), filepath.Join(x.dir, "alerts")); err != nil {
				t.Fatal(err)
			}
		},
		"card id": func(t *testing.T, x *importInbox) {
			x.write(t, "card-events/ck.1.json", prettyJSON(obj("version", 1, "type", "side_agent_data", "id", "ck.2", "source", obj(), "text", "t")))
		},
		"watch pane twice": func(t *testing.T, x *importInbox) {
			x.write(t, "watch-targets.json", prettyJSON([]any{obj("pane", "p7"), obj("pane", "p7")}))
		},
		"resume not object": func(t *testing.T, x *importInbox) {
			x.write(t, "resume-targets.json", prettyJSON([]any{}))
		},
		"credentials link": func(t *testing.T, x *importInbox) {
			_ = mkdirs(x.dir, "state/credentials")
			if err := os.Symlink("/etc/hosts", filepath.Join(x.dir, "state", "credentials", "adj-1")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			bad(t, x)
			c, e := x.run(t)
			if e == nil {
				t.Fatal("imported")
			}
			if n := rowsOf(t, c, `SELECT count(*) FROM import_log`); n[0] != "0" {
				t.Fatalf("a refused import kept %s import_log rows", n[0])
			}
		})
	}
}
