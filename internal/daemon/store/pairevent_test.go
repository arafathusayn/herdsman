package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// storedPair is the stored data of a pair event (5.3 type fields).
type storedPair struct {
	State           string `json:"state"`
	PendingChecksum string `json:"pending_checksum"`
	Peers           []struct {
		Name, Role, Pane, Session string
	} `json:"peers"`
}

func lastPair(t *testing.T, c Conn) storedPair {
	t.Helper()
	var data string
	if err := c.Query("SELECT data FROM events WHERE type = 'pair' ORDER BY seq DESC LIMIT 1", nil, func(r Row) error { data = r.Text(0); return nil }); err != nil {
		t.Fatal(err)
	}
	var d storedPair
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		t.Fatalf("pair event data %q: %v", data, err)
	}
	return d
}

func peerList(d storedPair) string {
	var out []string
	for _, p := range d.Peers {
		out = append(out, p.Name+"/"+p.Role+"/"+p.Pane+"/"+p.Session)
	}
	return strings.Join(out, ",")
}

// TestPairEventData (S4S6-1 P1-1): the stored pair event carries its 5.3
// fields: state, the new pending checksum (enroll and retire) and the
// peers with name, role, pane and the session's first 8 characters.
func TestPairEventData(t *testing.T) {
	c := registryRoute(t)
	long := Party{Name: "a1", Role: "adjutant", Pane: "p1", Session: `{"agent":"a","value":"0123456789abcdef"}`}
	if err := Enroll(c, long, 1); err != nil {
		t.Fatal(err)
	}
	sum, _ := PendingSHA256(c)
	d := lastPair(t, c)
	if d.State != "pending" || d.PendingChecksum != sum || peerList(d) != "a1/adjutant/p1/01234567" {
		t.Fatalf("enroll event %+v, want pending %s with a1", d, sum)
	}
	if err := Enroll(c, party("o1", "orchestrator", "p2"), 1); err != nil {
		t.Fatal(err)
	}
	sum, _ = PendingSHA256(c)
	q := ApproveRequest{SHA256: sum, RequestHash: "r1", ApprovedBy: "terminal",
		Credentials: map[string]Credential{"a1": {Option: "identity"}, "o1": {Option: "identity"}}}
	if _, err := Approve(c, q, 2); err != nil {
		t.Fatal(err)
	}
	if d := lastPair(t, c); d.State != "approved" || d.PendingChecksum != "" || peerList(d) != "a1/adjutant/p1/01234567,o1/orchestrator/p2/o1" {
		t.Fatalf("approve event %+v", d)
	}
	if err := Retire(c, "o1", 3); err != nil {
		t.Fatal(err)
	}
	sum, _ = PendingSHA256(c)
	if d := lastPair(t, c); d.State != "retired" || d.PendingChecksum != sum || peerList(d) != "a1/adjutant/p1/01234567" {
		t.Fatalf("retire event %+v, want retired %s with a1 left", d, sum)
	}
}

// TestEnrollVoidsOpenLaunches (S4S6-1 P1-2): every enroll voids the open
// launches ('registry changed'); a used or already void one stays.
func TestEnrollVoidsOpenLaunches(t *testing.T) {
	c := registryRoute(t)
	if _, err := c.Exec("INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at, void_reason) " +
		"VALUES (1, 'web', 'd', 'n1', 'adjutant', 'p', '{}', 0, 'g', 1, 1000, NULL), (2, 'web', 'd', 'n2', 'orchestrator', 'p', '{}', 0, 'g', 1, 1000, 'expired')"); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(c, party("a1", "adjutant", "p1"), 1); err != nil {
		t.Fatal(err)
	}
	if n := count(t, c, "SELECT count(*) FROM launches WHERE (id = 1 AND void_reason = 'registry changed') OR (id = 2 AND void_reason = 'expired')"); n != 2 {
		t.Fatalf("launches after an enroll: %d of 2 as expected", n)
	}
}

// TestApproveRefusesImport (D5): approved_by 'import' is the importer's
// alone.
func TestApproveRefusesImport(t *testing.T) {
	c := registryRoute(t)
	for _, p := range []Party{party("a1", "adjutant", "p1"), party("o1", "orchestrator", "p2")} {
		if err := Enroll(c, p, 1); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := PendingSHA256(c)
	q := ApproveRequest{SHA256: sum, RequestHash: "r1", ApprovedBy: "import",
		Credentials: map[string]Credential{"a1": {Option: "identity"}, "o1": {Option: "identity"}}}
	if _, err := Approve(c, q, 2); code(err) != "bad_request" {
		t.Fatalf("approved_by import: %v", err)
	}
	if n := count(t, c, "SELECT count(*) FROM approvals"); n != 0 {
		t.Fatalf("%d approvals", n)
	}
}

// TestAppendEventNeedsTransaction (D5): AppendEvent outside the
// operation's transaction is refused and writes nothing.
func TestAppendEventNeedsTransaction(t *testing.T) {
	c := registryRoute(t)
	if _, err := AppendEvent(c, Event{Type: "health", Data: "{}"}); err == nil {
		t.Fatal("AppendEvent ran outside a transaction")
	}
	if seq, _ := VisibleSeq(c); seq != 0 {
		t.Fatalf("visible seq %d", seq)
	}
}
