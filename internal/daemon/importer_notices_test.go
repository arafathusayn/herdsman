package daemon

import (
	"strings"
	"testing"
)

// TestImportNoticesAndResume: a sender notice for the quarantined m4,
// and every resume form, with .sent winning over its pending form.
func TestImportNoticesAndResume(t *testing.T) {
	x := newImportInbox(t)
	x.write(t, "orch-1/notices/m000000000004.md.json", prettyJSON(obj("id", "m000000000004", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
	x.write(t, "ids/m000000000004/notice-published", nil)
	x.write(t, "state/resume-k1.sent", []byte("1700000500 intent /goal resume; restore tab t1\n"))
	x.write(t, "state/resume-k1", []byte("intent\n"))
	x.write(t, "state/resume-k1.readback", []byte("screen\n"))
	x.write(t, "state/resume-k2", []byte("intent\n"))
	x.write(t, "state/resume-held-task-1", []byte("HELD turn is not idle or is blocked\n"))
	c, e := x.run(t)
	if e != nil {
		t.Fatal(e)
	}
	if got := strings.Join(rowsOf(t, c, `SELECT id||' '||recipient||' '||state||' '||authority FROM notices`), "; "); got != "4 orch-1 quarantined notice-only" {
		t.Fatalf("notices: %s", got)
	}
	want := `k1 {"state":"sent","text":"1700000500 intent /goal resume; restore tab t1"} 1700000500; ` +
		`k2 {"state":"pending","text":"intent"}; task-1 {"state":"held","text":"HELD turn is not idle or is blocked"}`
	got := strings.Join(rowsOf(t, c, `SELECT target||' '||intent||CASE WHEN target = 'k1' THEN ' '||time ELSE '' END FROM resume_intents ORDER BY target`), "; ")
	if got != want {
		t.Fatalf("resume_intents:\n%s\nwant\n%s", got, want)
	}
	for p, d := range map[string]string{
		"state/resume-k1":                      "superseded by .sent",
		"state/resume-k1.readback":             "legacy evidence: resume read-back",
		"ids/m000000000004/notice-published":   "notices",
		"orch-1/notices/m000000000004.md.json": "notices",
	} {
		if got := rowsOf(t, c, `SELECT disposition FROM import_log WHERE path = ?`, p); len(got) != 1 || got[0] != d {
			t.Errorf("import_log %s: %v, want %q", p, got, d)
		}
	}
}

// TestImportNoticeRefusals: a notice of a message not imported, and a
// .sent form without its time, refuse the import.
func TestImportNoticeRefusals(t *testing.T) {
	for name, bad := range map[string]func(*testing.T, *importInbox){
		"notice of no message": func(t *testing.T, x *importInbox) {
			x.write(t, "orch-1/notices/m000000000008.md.json", prettyJSON(obj("id", "m000000000008", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
		},
		"notice id": func(t *testing.T, x *importInbox) {
			x.write(t, "orch-1/notices/m000000000004.md.json", prettyJSON(obj("id", "m000000000001", "state", "quarantined")))
		},
		"sent without time": func(t *testing.T, x *importInbox) {
			x.write(t, "state/resume-k1.sent", []byte("intent /goal resume\n"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newImportInbox(t)
			bad(t, x)
			if _, e := x.run(t); e == nil {
				t.Fatal("imported")
			}
		})
	}
}
