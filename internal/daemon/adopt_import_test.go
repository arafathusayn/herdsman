package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/routedb"
)

// Real adoption runs the SQLite importer (Must prove 5): POST
// /v1/routes/{route}/adopt imports the inbox into the route database
// <private home>/daemon/routes/<route>.db before the route is published,
// and a refused import leaves the route unadopted and the inbox as it was.

// adoptDB opens the adopted route's database read-only.
func adoptDB(t *testing.T, h *harness, route string) store.Conn {
	t.Helper()
	c, err := store.Open(routedb.Path(h.ph, route), store.Reader, store.KindRoute)
	if err != nil {
		t.Fatalf("route database: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestAdoptionImportsEveryKind: one adoption through the API brings each
// imported kind into the route database: the parties, the messages, the
// state (a hold), the approval evidence, a sender notice, a resume
// intent and the delivery hold, each with its import_log row.
func TestAdoptionImportsEveryKind(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	write := func(rel string, b []byte) {
		t.Helper()
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env, _ := os.ReadFile(filepath.Join(dir, "orch-1", "ready", "m000000000002.md"))
	write("orch-1/quarantine/m000000000002.md", env)
	_ = os.Remove(filepath.Join(dir, "orch-1", "ready", "m000000000002.md"))
	write("ids/m000000000002/terminal.json", prettyJSON(obj("id", "m000000000002", "state", "quarantined")))
	write("orch-1/notices/m000000000002.md.json", prettyJSON(obj("id", "m000000000002", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
	write("state/hold-orch-1", prettyJSON(obj("holds", obj("adj-1", obj("reason", "wait", "time", 1700000000)))))
	write("state/resume-k1", []byte("intent\n"))
	write("state/route-paused", []byte("by the human\n"))
	// An earlier approval's evidence (another registry checksum).
	write("state/approval.json", prettyJSON(obj("sha256", strings.Repeat("a", 64), "interaction", "first-pairing",
		"credentials", []any{obj("name", "adj-1", "option", "file")}, "revoked", []any{})))
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	c := adoptDB(t, h, "old")
	for _, k := range []struct{ kind, q, want string }{
		{"parties", `SELECT group_concat(name||':'||role||':'||state, ' ') FROM (SELECT * FROM parties ORDER BY name)`, "adj-1:adjutant:approved orch-1:orchestrator:approved"},
		{"messages", `SELECT group_concat(id||':'||recipient||':'||state, ' ') FROM (SELECT * FROM messages ORDER BY id)`, "1:orch-1:ready 2:orch-1:quarantined"},
		{"state", `SELECT target||':'||owner||':'||reason FROM holds`, "orch-1:adj-1:wait"},
		{"approval evidence", `SELECT approved_by||' '||interaction||' '||result FROM approvals`, `import first-pairing {"approved":true,"credentials":[{"name":"adj-1","option":"file"}],"revoked":[]}`},
		{"sender notices", `SELECT id||':'||recipient||':'||state FROM notices`, "2:orch-1:quarantined"},
		{"resume intents", `SELECT target||' '||intent FROM resume_intents`, `k1 {"state":"pending","text":"intent"}`},
		{"delivery hold", `SELECT v FROM meta WHERE k = 'delivery_hold'`, "paused"},
		{"id high-water mark", `SELECT v FROM meta WHERE k = 'last_message_id'`, "m000000000002"},
		{"epoch length", `SELECT length(v) FROM meta WHERE k = 'epoch'`, "32"},
	} {
		if got := strings.Join(rowsOf(t, c, k.q), "; "); got != k.want {
			t.Errorf("%s: %q, want %q", k.kind, got, k.want)
		}
	}
	for _, p := range []string{"peers.approved", "orch-1/ready/m000000000001.md", "state/hold-orch-1", "state/approval.json",
		"orch-1/notices/m000000000002.md.json", "state/resume-k1", "state/route-paused", "setup.json"} {
		if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = ?`, p); len(got) != 1 {
			t.Errorf("import_log %s: %v, want one row", p, got)
		}
	}
	if _, ok := ReadOwner(dir); !ok {
		t.Error("the adopted inbox has no owner record")
	}
	if ents, _ := filepath.Glob(filepath.Join(h.ph, "daemon", "routes", "*staging*")); len(ents) != 0 {
		t.Errorf("staging files left: %v", ents)
	}
}

// refusedUntouched checks a refused adoption: no owner record, marker,
// pending entry, route entry or route database, the old proof in place
// and the adoption's locks released.
func refusedUntouched(t *testing.T, h *harness, dir string) {
	t.Helper()
	if _, ok := ReadOwner(dir); ok {
		t.Error("a refused adoption published owner.json")
	}
	for _, p := range []string{filepath.Join(dir, "state", "adopting.json"), filepath.Join(dir, "state", "legacy"), routedb.Path(h.ph, "old")} {
		if exists(p) {
			t.Errorf("a refused adoption left %s", p)
		}
	}
	if !exists(filepath.Join(dir, "setup.json")) {
		t.Error("a refused adoption moved the old proof")
	}
	if ents, _ := filepath.Glob(filepath.Join(h.ph, "daemon", "routes", "*")); len(ents) != 0 {
		t.Errorf("a refused adoption left route files: %v", ents)
	}
	if pl := h.d.readPending(); len(pl.Inboxes) != 0 {
		t.Errorf("a refused adoption is pending: %+v", pl)
	}
	if rf, _ := ReadRoutes(h.ph); rf.Routes["old"].Inbox != "" {
		t.Error("a refused adoption registered the route")
	}
	for _, l := range adoptLocks {
		if _, held := daemonLockOwner(filepath.Join(dir, l)); held {
			t.Errorf("a refused adoption kept %s", l)
		}
	}
}

// TestAdoptionRunsCensus: a file of the inbox with no import target and
// no listed reason refuses the adoption, and nothing changes.
func TestAdoptionRunsCensus(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("unaccounted source"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if err == nil || !strings.Contains(err.Error(), "no import target") {
		t.Fatalf("adoption with stray.txt: %v, want the census refusal", err)
	}
	refusedUntouched(t, h, dir)
	// Once the file is gone, the same adoption goes through.
	_ = os.Remove(filepath.Join(dir, "stray.txt"))
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	if res.Route != "old" {
		t.Fatalf("%+v", res)
	}
}

// TestAdoptionRefusesMalformedSource: a malformed source file refuses
// the adoption, and nothing changes.
func TestAdoptionRefusesMalformedSource(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	if err := os.WriteFile(filepath.Join(dir, "state", "hold-orch-1"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("adoption with a malformed hold: %v, want an import refusal", err)
	}
	refusedUntouched(t, h, dir)
}

// TestAdoptionRefusesExistingDatabase: a route database already at the
// route's path is never overwritten: the adoption is refused untouched.
func TestAdoptionRefusesExistingDatabase(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	p := routedb.Path(h.ph, "old")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("adoption over an existing database: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "keep" {
		t.Errorf("the existing database was changed: %q", b)
	}
	if _, ok := ReadOwner(dir); ok || !exists(filepath.Join(dir, "setup.json")) {
		t.Error("a refused adoption changed the inbox")
	}
}

// TestAdoptionLeavesNewToIntake: a message queued in new/ is not in the
// route database; its import_log row names the daemon's intake, which
// delivers it at the route's first open.
func TestAdoptionLeavesNewToIntake(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	id := "m000000000001.md"
	if err := os.Rename(filepath.Join(dir, "orch-1", "ready", id), filepath.Join(dir, "orch-1", "new", id)); err != nil {
		t.Fatal(err)
	}
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	c := adoptDB(t, h, "old")
	if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = ?`, "orch-1/new/"+id); len(got) != 1 || !strings.Contains(got[0], "intake") {
		t.Errorf("import_log of the new/ entry: %v", got)
	}
	if got := rowsOf(t, c, `SELECT count(*) FROM messages WHERE id = 1`); got[0] != "0" {
		t.Errorf("the new/ entry was imported: %v", got)
	}
}

// TestPublishStagingStates: publish renames the staging database (and
// its sides) to the route's path; a resume after the rename finds it
// done; a missing staging file, or one beside an existing database,
// keeps the adoption pending; a staging import that cannot start is
// refused with nothing left.
func TestPublishStagingStates(t *testing.T) {
	h := newHarness(t)
	const epoch = "0123456789abcdef0123456789abcdef"
	final, staged := routedb.Path(h.ph, "r"), h.d.stagingPath("r", epoch)
	if e := h.d.publishStaging("r", epoch); e == nil || !strings.Contains(e.Message, "missing") {
		t.Errorf("no staging file: %v", e)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"", "-wal"} {
		if err := os.WriteFile(staged+s, []byte("db"+s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if e := h.d.publishStaging("r", epoch); e != nil {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(final + "-wal"); string(b) != "db-wal" || exists(staged) || exists(final+"-shm") {
		t.Errorf("publish moved %q; staging left %v", b, exists(staged))
	}
	if e := h.d.publishStaging("r", epoch); e != nil {
		t.Errorf("a resume after the rename: %v", e)
	}
	if err := os.WriteFile(staged, []byte("again"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := h.d.publishStaging("r", epoch); e == nil || e.Code != "route_exists" {
		t.Errorf("staging beside a database: %v", e)
	}
	// The routes folder is not this user's private folder: the staging
	// database cannot open, and nothing is left.
	other := "s"
	routes := filepath.Dir(routedb.Path(h.ph, other))
	if err := os.Chmod(routes, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(routes, 0o700) }()
	if e := h.d.importStaging(other, h.ph, epoch); e == nil || !strings.Contains(e.Message, "staging route database") {
		t.Errorf("an unsafe routes folder: %v", e)
	}
	if exists(h.d.stagingPath(other, epoch)) {
		t.Error("a staging file was left")
	}
}

// TestStagingFileErrors: a routes folder that is a file refuses the
// import; a leftover staging file of an earlier adoption is removed; a
// rename that fails keeps the adoption pending.
func TestStagingFileErrors(t *testing.T) {
	h := newHarness(t)
	const epoch = "0123456789abcdef0123456789abcdef"
	routes := filepath.Dir(routedb.Path(h.ph, "r"))
	if err := os.WriteFile(routes, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if e := h.d.importStaging("r", h.ph, epoch); e == nil || !strings.Contains(e.Message, "not a directory") {
		t.Errorf("routes folder a file: %v", e)
	}
	_ = os.Remove(routes)
	daemonDir := filepath.Dir(routes)
	if err := os.Chmod(daemonDir, 0o500); err != nil {
		t.Fatal(err)
	}
	e := h.d.importStaging("r", h.ph, epoch)
	_ = os.Chmod(daemonDir, 0o700)
	if e == nil || !strings.Contains(e.Message, "route database folder") {
		t.Errorf("routes folder cannot be made: %v", e)
	}
	if err := os.Mkdir(routes, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := h.d.stagingPath("r", "ffffffffffffffffffffffffffffffff")
	if err := os.WriteFile(leftover, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if e := h.d.importStaging("r", h.ph, epoch); e == nil {
		t.Error("the private home imported as an inbox")
	}
	if exists(leftover) || exists(h.d.stagingPath("r", epoch)) {
		t.Error("a staging file was left")
	}
	staged, final := h.d.stagingPath("r", epoch), routedb.Path(h.ph, "r")
	if err := os.WriteFile(staged, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged+"-shm", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(final+"-shm", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if e := h.d.publishStaging("r", epoch); e == nil || !strings.Contains(e.Message, "stays pending") {
		t.Errorf("a failed rename: %v", e)
	}
}
