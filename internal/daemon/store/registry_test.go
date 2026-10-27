package store

import (
	"errors"
	"strings"
	"testing"
)

// registryRoute is a fresh route database with its meta rows.
func registryRoute(t *testing.T) Conn {
	t.Helper()
	c := openKind(t, KindRoute)
	if err := InitRoute(c, epochA); err != nil {
		t.Fatal(err)
	}
	return c
}

func party(name, role, pane string) Party {
	return Party{Name: name, Role: role, Pane: pane, Session: `{"agent":"a","value":"` + name + `"}`}
}

func code(err error) string {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func count(t *testing.T, c Conn, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := c.Query(q, args, func(r Row) error { n = r.Int64(0); return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}

// pairUp enrolls adj and orch and approves them with file credentials.
func pairUp(t *testing.T, c Conn, adj, orch, req string) ApproveResult {
	t.Helper()
	for _, p := range []Party{party(adj, "adjutant", "p-"+adj), party(orch, "orchestrator", "p-"+orch)} {
		if err := Enroll(c, p, 1); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := PendingSHA256(c)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Approve(c, ApproveRequest{SHA256: sum, RequestHash: req, ApprovedBy: "terminal", Credentials: map[string]Credential{
		adj: {Option: "file", SHA256: strings.Repeat("1", 64), Secret: []byte("s-" + adj)}, orch: {Option: "identity"}}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestEnrollRulesOnStore (7.4): a retired name never returns, a name keeps
// its identity, a second pending party of a role, pane or session is
// role_taken, and enrolling again unchanged is a no-op.
func TestEnrollRulesOnStore(t *testing.T) {
	c := registryRoute(t)
	a := party("a1", "adjutant", "p1")
	if err := Enroll(c, a, 1); err != nil {
		t.Fatal(err)
	}
	rev, _ := metaInt(c, "registry_rev")
	if err := Enroll(c, a, 1); err != nil {
		t.Fatalf("same enrollment again: %v", err)
	}
	if again, _ := metaInt(c, "registry_rev"); again != rev {
		t.Fatalf("a repeated enrollment changed registry_rev %d -> %d", rev, again)
	}
	for name, tc := range map[string]struct {
		p    Party
		want string
	}{
		"second pending adjutant": {party("a2", "adjutant", "p2"), "role_taken"},
		"same pane":               {party("o1", "orchestrator", "p1"), "role_taken"},
		"same session":            {Party{Name: "o1", Role: "orchestrator", Pane: "p3", Session: a.Session}, "role_taken"},
		"name with a new pane":    {party("a1", "adjutant", "p9"), "name_retired"},
		"bad role":                {party("x", "reviewer", "p8"), "bad_request"},
	} {
		if err := Enroll(c, tc.p, 1); code(err) != tc.want {
			t.Errorf("%s: %v, want %s", name, err, tc.want)
		}
	}
	if err := Retire(c, "a1", 2); err != nil {
		t.Fatal(err)
	}
	if err := Enroll(c, a, 3); code(err) != "name_retired" {
		t.Fatalf("retired name enrolled again: %v", err)
	}
}

// TestApproveOnStore (7.5): the inspected pair is approved in one
// transaction: an approvals row, both parties approved, a credential per
// party (the file secret in its row, identity bound to the session),
// registry_rev raised, a pair event with the approval's seq.
func TestApproveOnStore(t *testing.T) {
	c := registryRoute(t)
	res := pairUp(t, c, "a1", "o1", "req1")
	if !res.Approved || len(res.Credentials) != 2 || len(res.Revoked) != 0 {
		t.Fatalf("result %+v", res)
	}
	if n := count(t, c, "SELECT count(*) FROM parties WHERE state = 'approved'"); n != 2 {
		t.Fatalf("%d approved parties", n)
	}
	cr, err := ActiveCredential(c, "a1")
	if err != nil || cr.Option != "file" || string(cr.Secret) != "s-a1" {
		t.Fatalf("a1 credential %+v %v", cr, err)
	}
	if cr, err := ActiveCredential(c, "o1"); err != nil || cr.Option != "identity" || len(cr.SHA256) != 64 || cr.Secret != nil {
		t.Fatalf("o1 credential %+v %v", cr, err)
	}
	if n := count(t, c, "SELECT count(*) FROM approvals a JOIN events e ON e.seq = a.seq WHERE e.type = 'pair' AND a.approved_by = 'terminal'"); n != 1 {
		t.Fatalf("approval without its pair event: %d", n)
	}
	if rev, _ := metaInt(c, "registry_rev"); rev != 3 {
		t.Fatalf("registry_rev %d, want 3 (two enrolls, one approval)", rev)
	}
}

// TestApproveRetrySameRequest (F5): the same request again answers the
// stored result and issues nothing: no second approval, no new
// credential generation, no event.
func TestApproveRetrySameRequest(t *testing.T) {
	c := registryRoute(t)
	first := pairUp(t, c, "a1", "o1", "req1")
	seq, _ := VisibleSeq(c)
	again, err := Approve(c, ApproveRequest{SHA256: "whatever", RequestHash: "req1"}, 9)
	if err != nil || again.Approved != first.Approved || len(again.Credentials) != len(first.Credentials) {
		t.Fatalf("retry %+v %v", again, err)
	}
	if n := count(t, c, "SELECT count(*) FROM approvals"); n != 1 {
		t.Fatalf("%d approvals after a retry", n)
	}
	if n := count(t, c, "SELECT count(*) FROM credentials"); n != 2 {
		t.Fatalf("%d credential rows after a retry", n)
	}
	if now, _ := VisibleSeq(c); now != seq {
		t.Fatalf("a retry wrote events %d -> %d", seq, now)
	}
}

// TestApproveRefusals: a checksum that is not the pending pair's, an
// incomplete pair and a credential for someone outside the pair are
// refused and write nothing.
func TestApproveRefusals(t *testing.T) {
	c := registryRoute(t)
	if err := Enroll(c, party("a1", "adjutant", "p1"), 1); err != nil {
		t.Fatal(err)
	}
	sum, _ := PendingSHA256(c)
	if _, err := Approve(c, ApproveRequest{SHA256: sum, RequestHash: "r1", ApprovedBy: "terminal"}, 2); code(err) != "incomplete_pair" {
		t.Fatalf("one party: %v", err)
	}
	if err := Enroll(c, party("o1", "orchestrator", "p2"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := Approve(c, ApproveRequest{SHA256: sum, RequestHash: "r2", ApprovedBy: "terminal"}, 2); code(err) != "checksum_mismatch" {
		t.Fatalf("stale checksum: %v", err)
	}
	sum, _ = PendingSHA256(c)
	q := ApproveRequest{SHA256: sum, RequestHash: "r3", ApprovedBy: "terminal", Credentials: map[string]Credential{
		"a1": {Option: "identity"}, "o1": {Option: "identity"}, "z": {Option: "identity"}}}
	if _, err := Approve(c, q, 2); code(err) != "bad_request" {
		t.Fatalf("credential outside the pair: %v", err)
	}
	if n := count(t, c, "SELECT count(*) FROM approvals"); n != 0 {
		t.Fatalf("%d approvals after refusals", n)
	}
}

// TestAskerRuleAndSupersede (PR 28, N-P1-1): with an approved adjutant,
// only its ask approves a re-enrolled pair; the orchestrator's ask and a
// stranger's are refused; a browser approval is not an ask. Approving
// supersedes the old pair: retired, credentials revoked, open launches
// voided, and both old names in revoked.
func TestAskerRuleAndSupersede(t *testing.T) {
	c := registryRoute(t)
	pairUp(t, c, "a1", "o1", "req1")
	if _, err := c.Exec("INSERT INTO launches(id, via, launcher_name, launcher_generation, name, role, pane, session, registry_rev, created_at, expires_at) " +
		"VALUES (1, 'adjutant', 'a1', 1, 'o1', 'orchestrator', 'p', '{}', 3, 1, 1000)"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Party{party("a2", "adjutant", "p-a2"), party("o2", "orchestrator", "p-o2")} {
		if err := Enroll(c, p, 3); err != nil {
			t.Fatal(err)
		}
	}
	sum, _ := PendingSHA256(c)
	creds := map[string]Credential{"a2": {Option: "identity"}, "o2": {Option: "identity"}}
	ask := func(by, interaction, req string) error {
		_, err := Approve(c, ApproveRequest{SHA256: sum, RequestHash: req, Interaction: interaction, AskedBy: by, ApprovedBy: "adjutant-ask", Credentials: creds}, 4)
		return err
	}
	if err := ask("o1", "ask:7", "r1"); err == nil || !strings.Contains(err.Error(), "orchestrator's own ask") {
		t.Fatalf("orchestrator's ask: %v", err)
	}
	if err := ask("stranger", "ask:7", "r2"); err == nil || !strings.Contains(err.Error(), "not asked by the approved adjutant") {
		t.Fatalf("stranger's ask: %v", err)
	}
	if err := ask("a1\x07", "ask:7", "r3"); code(err) != "bad_request" {
		t.Fatalf("control character in asked_by: %v", err)
	}
	if err := ask("a1", "ask:7", "r4"); err != nil {
		t.Fatalf("the adjutant's ask: %v", err)
	}
	for _, n := range []string{"a1", "o1"} {
		if st := count(t, c, "SELECT count(*) FROM parties WHERE name = ? AND state = 'retired'", n); st != 1 {
			t.Fatalf("%s not retired", n)
		}
		if _, err := ActiveCredential(c, n); !errors.Is(err, ErrNoCredential) {
			t.Fatalf("%s credential still active: %v", n, err)
		}
	}
	if n := count(t, c, "SELECT count(*) FROM launches WHERE id = 1 AND void_reason = 'superseded'"); n != 1 {
		t.Fatal("the superseded party's launch is not void")
	}
	var res string
	_ = c.Query("SELECT result FROM approvals WHERE request_sha256 = 'r4'", nil, func(r Row) error { res = r.Text(0); return nil })
	if !strings.Contains(res, `"revoked":["a1","o1"]`) {
		t.Fatalf("stored result %s", res)
	}
	// A paired browser's approval is the human's, not an ask.
	c2 := registryRoute(t)
	pairUp(t, c2, "a1", "o1", "req1")
	for _, p := range []Party{party("a2", "adjutant", "p-a2"), party("o2", "orchestrator", "p-o2")} {
		if err := Enroll(c2, p, 3); err != nil {
			t.Fatal(err)
		}
	}
	sum2, _ := PendingSHA256(c2)
	if _, err := Approve(c2, ApproveRequest{SHA256: sum2, RequestHash: "w1", Interaction: "web:dev1", ApprovedBy: "web", Credentials: creds}, 4); err != nil {
		t.Fatalf("browser approval: %v", err)
	}
}

// TestRetireRevokeOnStore: retire revokes the name's credentials, voids
// its launches and every other open launch, raises registry_rev; revoke
// ends an active credential once; an unknown name is refused.
func TestRetireRevokeOnStore(t *testing.T) {
	c := registryRoute(t)
	pairUp(t, c, "a1", "o1", "req1")
	if _, err := c.Exec("INSERT INTO launches(id, via, device, name, role, pane, session, registry_rev, launch_group, created_at, expires_at) " +
		"VALUES (1, 'web', 'd', 'o1', 'orchestrator', 'p', '{}', 3, 'g', 1, 1000), (2, 'web', 'd', 'n2', 'adjutant', 'p', '{}', 3, 'g', 1, 1000)"); err != nil {
		t.Fatal(err)
	}
	rev, _ := metaInt(c, "registry_rev")
	if err := Retire(c, "o1", 5); err != nil {
		t.Fatal(err)
	}
	if n := count(t, c, "SELECT count(*) FROM launches WHERE (id = 1 AND void_reason = 'retired name') OR (id = 2 AND void_reason = 'registry changed')"); n != 2 {
		t.Fatalf("launches voided: %d of 2", n)
	}
	if now, _ := metaInt(c, "registry_rev"); now != rev+1 {
		t.Fatalf("registry_rev %d -> %d", rev, now)
	}
	if _, err := ActiveCredential(c, "o1"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("retired credential: %v", err)
	}
	if err := Retire(c, "o1", 6); code(err) != "unknown_name" {
		t.Fatalf("retire twice: %v", err)
	}
	if err := Revoke(c, "a1"); err != nil {
		t.Fatal(err)
	}
	if err := Revoke(c, "a1"); code(err) != "unknown_name" {
		t.Fatalf("revoke twice: %v", err)
	}
}
