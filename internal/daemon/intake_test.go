package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// legacyEntry writes a file-mode send as the scripts did before adoption:
// the allocation copy and the new/ entry (body may differ in new/ to
// model a tampered entry).
func legacyEntry(t *testing.T, dir, id, from, to, body, newBody string) {
	t.Helper()
	root := &inbox.Root{Dir: dir}
	fp, ok1 := root.Peer(from)
	tp, ok2 := root.Peer(to)
	if !ok1 || !ok2 {
		t.Fatal("peers")
	}
	env := func(b string) []byte {
		return prettyJSON(obj("version", 1, "id", id, "from", from, "to", to,
			"from_identity", identityObj(fp.Identity), "to_identity", identityObj(tp.Identity),
			"time", 1, "kind", "brief", "reply_to", "", "body", b))
	}
	_ = os.MkdirAll(filepath.Join(dir, "ids", id), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, to, "new"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "ids", id, "envelope"), env(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, to, "new", id+".md"), env(newBody), 0o600); err != nil {
		t.Fatal(err)
	}
}

// intakeRecords counts the intake records in the journal files.
func intakeRecords(dir string) int {
	n := 0
	for _, f := range []string{"previous.ndjson", "current.ndjson"} {
		b, _ := os.ReadFile(filepath.Join(dir, "state", "events", f))
		n += strings.Count(string(b), `"op":"intake"`)
	}
	return n
}

// TestAdoptPromotesLegacyNew: file-mode sends left in new/ at adoption
// are verified as the courier did: a valid one becomes readable, a
// tampered one is quarantined with its terminal record and the sender's
// notice; repeated restarts neither lose nor duplicate either (12.2).
func TestAdoptPromotesLegacyNew(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	legacyEntry(t, dir, "m000000000001", adj.name, orch.name, "the brief", "the brief")
	legacyEntry(t, dir, "m000000000002", adj.name, orch.name, "the brief", "a changed brief")
	h.restart()
	out, _, code := h.msg(orch, "r", "read")
	if code != 0 || !strings.Contains(out, `"id":"m000000000001"`) || strings.Contains(out, "m000000000002") {
		t.Fatalf("read after adoption: %d %s", code, out)
	}
	for _, p := range []string{
		filepath.Join(dir, orch.name, "quarantine", "m000000000002.md"),
		filepath.Join(dir, "ids", "m000000000002", "terminal.json"),
		filepath.Join(dir, adj.name, "notices", "m000000000002.md.json"),
	} {
		if !exists(p) {
			t.Fatalf("missing %s", p)
		}
	}
	if exists(filepath.Join(dir, orch.name, "new", "m000000000001.md")) || exists(filepath.Join(dir, orch.name, "new", "m000000000002.md")) {
		t.Fatal("an entry stayed in new/")
	}
	if n := intakeRecords(dir); n != 2 {
		t.Fatalf("%d intake records", n)
	}
	for i := 0; i < 2; i++ {
		h.restart()
	}
	if n := intakeRecords(dir); n > 2 {
		t.Fatalf("restarts recorded the intake again: %d", n)
	}
	// A second copy of a delivered entry is held for the human, never
	// delivered twice.
	legacyEntry(t, dir, "m000000000001", adj.name, orch.name, "the brief", "the brief")
	before := intakeRecords(dir)
	h.restart()
	h.msg(orch, "r", "status") // after the actor's first turn, which takes in new/
	if !exists(filepath.Join(dir, orch.name, "new", "m000000000001.md")) || intakeRecords(dir) != before ||
		exists(filepath.Join(dir, orch.name, "ready", "m000000000001.md")) {
		t.Fatal("a duplicate entry was taken in again")
	}
	_ = os.Remove(filepath.Join(dir, orch.name, "new", "m000000000001.md"))
	// A new send after the intake takes the next id.
	body := writeFile(t, h.dir, "b.md", "next")
	if out, _, code := h.msg(adj, "r", "send", orch.name, body, "brief"); code != 0 || !strings.HasPrefix(out, "m000000000003 ") {
		t.Fatalf("next send: %d %q", code, out)
	}
}
