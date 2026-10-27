package daemon

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// withEveryKind adds every other file kind of the section 5 table that
// S14b covers: partial writes, files that stay in place, legacy
// evidence, the file channel's service state, an allocation with no
// message, an ack with no cur entry and a contract 2 approval.
func (x *importInbox) withEveryKind(t *testing.T) {
	t.Helper()
	x.withState(t)
	for rel, b := range map[string]string{
		"orch-1/tmp/m000000000008.md":      "half",
		"orch-1/recovery/m000000000007.md": "kept",
		"adj-1/ack/m000000000006.md.json":  "{}",
		"ids/m000000000009/envelope":       "{}",
		"state/route.lock/owner.json":      "{}",
		"state/boundary-tainted":           "x",
		"state/exposure-pending":           "x",
		"herdr-mutation.lock/owner.json":   "{}",
		"approvals/request.json":           "{}",
		"setup.json":                       "{}",
		"state/legacy/setup-v2.json":       "{}",
		"state/ring-abc":                   "1",
		"state/automation-held":            "1",
		"state/card-visible-0011":          "1",
		"state/heartbeat":                  "1",
		"state/watch-heartbeat":            "1",
		"state/diagnostic-1.log":           "x",
		"state/timing.json":                "{}",
		"courier.lock/owner":               "1",
		"watch.lock/owner":                 "1",
		"channel.lock/owner":               "1",
		"state/.tmp-sequence":              "5",
		"state/checkpoints/c1.json":        "{}",
	} {
		x.write(t, rel, []byte(b))
	}
	x.write(t, "state/approval.json", prettyJSON(obj("sha256", strings.Repeat("a", 64), "interaction", "first-pairing",
		"credentials", []any{obj("name", "adj-1", "option", "file")}, "revoked", []any{})))
}

// TestImportEveryKind: every file of the inbox has one import_log row,
// a contract 2 approval is an approvals evidence row, and a file with no
// target and no listed reason refuses the import.
func TestImportEveryKind(t *testing.T) {
	t.Skip("S14b work in progress: the census is not wired into importCore yet")
	x := newImportInbox(t)
	x.withEveryKind(t)
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	logged := map[string]string{}
	for _, r := range rowsOf(t, c, `SELECT path || '|' || disposition FROM import_log`) {
		p, d, _ := strings.Cut(r, "|")
		logged[p] = d
	}
	_ = filepath.WalkDir(x.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(x.dir, p)
			if logged[rel] == "" {
				t.Errorf("%s has no import_log row", rel)
			}
		}
		return nil
	})
	for p, want := range map[string]string{
		"orch-1/tmp/m000000000008.md":      "partial-write",
		"orch-1/recovery/m000000000007.md": "partial-write",
		"state/.tmp-sequence":              "partial-write",
		"state/boundary-tainted":           "stays in place: the boundary files (section 6)",
		"herdr-mutation.lock/owner.json":   "stays in place: the input lock",
		"state/route.lock/owner.json":      "stays in place: the route ownership lock",
		"state/ring-abc":                   "not imported: courier ring marker, file channel service state",
		"ids/m000000000009/envelope":       "legacy evidence: an allocation record of a message not imported",
		"adj-1/ack/m000000000006.md.json":  "legacy evidence: an ack without its cur entry",
		"state/approval.json":              "approvals (evidence)",
	} {
		if logged[p] != want {
			t.Errorf("%s: %q, want %q", p, logged[p], want)
		}
	}
	if got := strings.Join(rowsOf(t, c, `SELECT approved_by || ' ' || registry_sha256 || ' ' || interaction || ' ' || result FROM approvals`), "; "); got !=
		"import "+strings.Repeat("a", 64)+` first-pairing {"approved":true,"credentials":[{"name":"adj-1","option":"file"}],"revoked":[]}` {
		t.Fatalf("approvals: %s", got)
	}
	if n := rowsOf(t, c, `SELECT count(*) FROM credentials`); n[0] != "0" {
		t.Fatal("credentials imported; they come only at the first approval after adoption (D1)")
	}
	for _, rel := range []string{"state/mystery", "stray.txt", "orch-1/odd/file", "ids/m000000000001/surprise"} {
		x := newImportInbox(t)
		x.write(t, rel, []byte("x"))
		if _, e := x.run(t); e == nil || !strings.Contains(e.Message, "no import target") {
			t.Errorf("%s: %v", rel, e)
		}
	}
}
