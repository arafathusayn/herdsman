package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// importInbox is a file-mode inbox for the importer: an approved pair
// (adj-1, orch-1), m1 ready for orch-1, m2 in cur and acked, m3 in cur
// and not acked, m4 quarantined for adj-1, ids/ up to m9 allocated and
// state/sequence at 5.
type importInbox struct {
	ph, dir string
	token   string
}

func newImportInbox(t *testing.T) *importInbox {
	t.Helper()
	ph := shortDir(t)
	x := &importInbox{ph: ph, dir: filepath.Join(ph, "repo-r"), token: strings.Repeat("c", 32)}
	_ = os.Mkdir(x.dir, 0o700)
	_ = mkdirs(x.dir, "state", "ids", "enrolled", "alerts")
	adj := inbox.Identity{Pane: "p1", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s1"}}
	orch := inbox.Identity{Pane: "p2", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s2"}}
	reg := prettyJSON(obj("adj-1", obj("role", "adjutant", "identity", identityObj(adj)), "orch-1", obj("role", "orchestrator", "identity", identityObj(orch))))
	x.write(t, "peers", reg)
	x.write(t, "peers.approved", reg)
	x.write(t, "enrolled/adj-1", []byte("p1\n"))
	for _, b := range []string{"adj-1", "orch-1"} {
		_ = mkdirs(x.dir, b+"/tmp", b+"/new", b+"/ready", b+"/cur", b+"/quarantine", b+"/ack", b+"/notices")
	}
	msg := func(id, from, to, body string) []byte {
		fi, ti := adj, orch
		if from == "orch-1" {
			fi, ti = orch, adj
		}
		return prettyJSON(obj("version", 1, "id", id, "from", from, "to", to, "from_identity", identityObj(fi), "to_identity", identityObj(ti),
			"time", 1700000000, "kind", "brief", "reply_to", "", "body", body))
	}
	for _, m := range []struct{ id, from, to, queue string }{
		{"m000000000001", "adj-1", "orch-1", "ready"}, {"m000000000002", "adj-1", "orch-1", "cur"},
		{"m000000000003", "adj-1", "orch-1", "cur"}, {"m000000000004", "orch-1", "adj-1", "quarantine"},
	} {
		env := msg(m.id, m.from, m.to, "body of "+m.id)
		x.write(t, "ids/"+m.id+"/envelope", env)
		x.write(t, m.to+"/"+m.queue+"/"+m.id+".md", env)
	}
	// m2: claimed and acked under an earlier claim.
	sum, _ := inbox.DigestFile(filepath.Join(x.dir, "orch-1", "cur", "m000000000002.md"))
	x.write(t, "ids/m000000000002/claim", []byte(".claim.123\n"))
	x.write(t, "orch-1/ack/m000000000002.md.json", prettyJSON(obj("id", "m000000000002", "claim", ".claim.123", "time", 1700000100, "processing_record", "done", "envelope_checksum", sum)))
	x.write(t, "ids/m000000000002/terminal.json", prettyJSON(obj("id", "m000000000002", "state", "handled", "envelope_checksum", sum)))
	x.write(t, "ids/m000000000004/terminal.json", prettyJSON(obj("id", "m000000000004", "state", "quarantined")))
	_ = mkdirs(x.dir, "ids/m000000000009")
	x.write(t, "state/sequence", []byte("5\n"))
	return x
}

func (x *importInbox) write(t *testing.T, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(x.dir, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// run imports into a fresh staging database and returns it open.
func (x *importInbox) run(t *testing.T) (store.Conn, *APIError) {
	t.Helper()
	dbDir := filepath.Join(x.ph, "daemon")
	_ = os.Mkdir(dbDir, 0o700)
	c, err := store.Open(filepath.Join(dbDir, "staging-"+randomHex(4)+".db"), store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var ae *APIError
	_ = c.Tx(func(tx store.Conn) error {
		if ae = importCore(tx, x.dir, 1800000000); ae != nil {
			return ae
		}
		return nil
	})
	return c, ae
}

func rowsOf(t *testing.T, c store.Conn, q string, args ...any) []string {
	t.Helper()
	var out []string
	if err := c.Query(q, args, func(r store.Row) error { out = append(out, r.Text(0)); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestImportCore: the approved pair, every message with its state, the
// ack byte for byte, the quarantine terminal, the id high-water mark and
// one import_log row per source file read.
func TestImportCore(t *testing.T) {
	x := newImportInbox(t)
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT name || ' ' || role || ' ' || pane || ' ' || state || ' ' || session FROM parties ORDER BY name`), "\n"); got !=
		`adj-1 adjutant p1 approved {"agent":"claude","kind":"id","source":"hook","value":"s1"}`+"\n"+
			`orch-1 orchestrator p2 approved {"agent":"claude","kind":"id","source":"hook","value":"s2"}` {
		t.Fatalf("parties:\n%s", got)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT id || ' ' || sender || ' ' || recipient || ' ' || state || ' ' || origin || ' ' || coalesce(claim_token, '-') FROM messages ORDER BY id`), "\n"); got !=
		"1 adj-1 orch-1 ready imported -\n2 adj-1 orch-1 handled imported -\n3 adj-1 orch-1 cur imported -\n4 orch-1 adj-1 quarantined imported -" {
		t.Fatalf("messages:\n%s", got)
	}
	body := rowsOf(t, c, `SELECT body FROM messages WHERE id = 1`)
	sum := sha256.Sum256([]byte("body of m000000000001"))
	if body[0] != "body of m000000000001" || rowsOf(t, c, `SELECT body_sha256 FROM messages WHERE id = 1`)[0] != hex.EncodeToString(sum[:]) {
		t.Fatalf("body %q", body)
	}
	if env := rowsOf(t, c, `SELECT envelope FROM messages WHERE id = 1`)[0]; strings.Contains(env, "body") || !strings.Contains(env, `"id":"m000000000001"`) {
		t.Fatalf("envelope %s", env)
	}
	ackFile, _ := os.ReadFile(filepath.Join(x.dir, "orch-1", "ack", "m000000000002.md.json"))
	var record []byte
	_ = c.Query(`SELECT record FROM acks WHERE id = 2`, nil, func(r store.Row) error { record = r.Bytes(0); return nil })
	if string(record) != string(ackFile) || rowsOf(t, c, `SELECT claim FROM acks WHERE id = 2`)[0] != ".claim.123" {
		t.Fatalf("ack %q", record)
	}
	if got := rowsOf(t, c, `SELECT id || ' ' || state FROM terminals`); len(got) != 1 || got[0] != "4 quarantined" {
		t.Fatalf("terminals %v", got)
	}
	if got := rowsOf(t, c, `SELECT v FROM meta WHERE k = 'last_message_id'`); len(got) != 1 || got[0] != "m000000000009" {
		t.Fatalf("high water %v", got)
	}
	logged := strings.Join(rowsOf(t, c, `SELECT path || ' ' || disposition FROM import_log ORDER BY path`), "\n")
	for _, want := range []string{"peers parties", "peers.approved parties", "enrolled/adj-1 parties", "orch-1/ready/m000000000001.md messages ready",
		"orch-1/cur/m000000000002.md messages handled", "orch-1/ack/m000000000002.md.json acks", "ids/m000000000002/terminal.json acks",
		"ids/m000000000002/claim acks", "orch-1/cur/m000000000003.md messages cur", "adj-1/quarantine/m000000000004.md messages quarantined",
		"ids/m000000000004/terminal.json terminals", "ids/m000000000001/envelope messages ready", "state/sequence meta last_message_id"} {
		if !strings.Contains(logged+"\n", want+"\n") {
			t.Fatalf("import_log misses %q:\n%s", want, logged)
		}
	}
	peers, _ := os.ReadFile(filepath.Join(x.dir, "peers"))
	ps := sha256.Sum256(peers)
	if rowsOf(t, c, `SELECT sha256 FROM import_log WHERE path = 'peers'`)[0] != hex.EncodeToString(ps[:]) {
		t.Fatal("import_log sha256")
	}
}

// TestImportActiveClaim: an active reader claim is imported with its
// token, and the unacked cur message it bound carries it; a file-mode
// token the schema cannot hold refuses the adoption, never renamed.
func TestImportActiveClaim(t *testing.T) {
	x := newImportInbox(t)
	x.write(t, "orch-1/claim/token", []byte(x.token+"\n"))
	x.write(t, "orch-1/claim/owner", []byte("p2:4242\n"))
	x.write(t, "ids/m000000000003/claim", []byte(x.token+"\n"))
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	if got := rowsOf(t, c, `SELECT token || ' ' || box || ' ' || owner || ' ' || state FROM claims`); len(got) != 1 || got[0] != x.token+" orch-1 p2:4242 active" {
		t.Fatalf("claims %v", got)
	}
	if got := rowsOf(t, c, `SELECT coalesce(claim_token, '-') FROM messages WHERE id = 3`); got[0] != x.token {
		t.Fatalf("m3 claim %v", got)
	}
	x.write(t, "orch-1/claim/token", []byte(".claim.987654\n"))
	if _, e := x.run(t); e == nil || e.Code != "adopt_locked" || !strings.Contains(e.Message, "--release") {
		t.Fatalf("a file-mode token: %v", e)
	}
}

// TestImportRefusals: a pending registry, an id in two queues, an
// envelope that does not match its file, a linked queue folder and a
// linked message file each refuse, and nothing is committed.
func TestImportRefusals(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, x *importInbox)
		code string
		text string
	}{
		"pending registry": {func(t *testing.T, x *importInbox) { x.write(t, "peers", []byte("{}\n")) }, "checksum_mismatch", "approved pair"},
		"two queues": {func(t *testing.T, x *importInbox) {
			b, _ := os.ReadFile(filepath.Join(x.dir, "orch-1", "ready", "m000000000001.md"))
			x.write(t, "orch-1/cur/m000000000001.md", b)
		}, "internal", "two queues"},
		"envelope mismatch": {func(t *testing.T, x *importInbox) {
			b, _ := os.ReadFile(filepath.Join(x.dir, "orch-1", "ready", "m000000000001.md"))
			x.write(t, "orch-1/ready/m000000000007.md", b)
		}, "internal", "does not match its file"},
		"linked queue": {func(t *testing.T, x *importInbox) {
			_ = os.RemoveAll(filepath.Join(x.dir, "adj-1", "ready"))
			_ = os.Symlink(t.TempDir(), filepath.Join(x.dir, "adj-1", "ready"))
		}, "internal", "is a link"},
		"linked message": {func(t *testing.T, x *importInbox) {
			_ = os.Symlink(filepath.Join(x.dir, "ids", "m000000000001", "envelope"), filepath.Join(x.dir, "orch-1", "ready", "m000000000005.md"))
		}, "internal", "not a strict JSON file"},
		"bad name":     {func(t *testing.T, x *importInbox) { x.write(t, "orch-1/ready/notes.txt", []byte("x")) }, "internal", "not a message file"},
		"bad sequence": {func(t *testing.T, x *importInbox) { x.write(t, "state/sequence", []byte("x\n")) }, "internal", "state/sequence"},
		"bad terminal": {func(t *testing.T, x *importInbox) {
			x.write(t, "ids/m000000000004/terminal.json", prettyJSON(obj("id", "m000000000004", "state", "handled")))
		}, "internal", "as quarantined"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			c.edit(t, x)
			db, e := x.run(t)
			if e == nil || e.Code != c.code || !strings.Contains(e.Message, c.text) {
				t.Fatalf("got %v", e)
			}
			if n := rowsOf(t, db, `SELECT count(*) FROM parties`); n[0] != "0" {
				t.Fatal("a refused import committed rows")
			}
		})
	}
}
