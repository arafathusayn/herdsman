package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

func TestHelloAndVersionRefusal(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	hello, err := c.Hello(context.Background())
	if err != nil || hello.DaemonVersion != testVersion || hello.Protocol != Protocol {
		t.Fatalf("hello %+v %v", hello, err)
	}
	cases := []struct {
		version, protocol string
		code, update      string
		status            int
	}{
		{"", "", "version_missing", "client", 400},
		{"x.y", "1", "version_malformed", "client", 400},
		{"0.9.0", "1", "version_mismatch", "client", 426},
		{"2.0.0", "1", "version_mismatch", "daemon", 426},
		{testVersion, "9", "version_mismatch", "daemon", 426},
		{"devel+abcdef0", "1", "version_mismatch", "client", 426},
	}
	for _, k := range cases {
		req, _ := http.NewRequest("GET", "http://herdsman/v1/health", nil)
		if k.version != "" {
			req.Header.Set(HeaderVersion, k.version)
			req.Header.Set(HeaderProtocol, k.protocol)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var eb errorBody
		_ = json.NewDecoder(resp.Body).Decode(&eb)
		resp.Body.Close()
		if resp.StatusCode != k.status || eb.Error == nil || eb.Error.Code != k.code || eb.Error.Update != k.update {
			t.Fatalf("%s/%s: %d %+v", k.version, k.protocol, resp.StatusCode, eb.Error)
		}
	}
}

func TestHelloReportsDefaultCredential(t *testing.T) {
	h := newHarness(t)
	hello, err := h.client().Hello(context.Background())
	if err != nil || hello.DefaultCredential != "file" || len(hello.CredentialOptions) < 2 {
		t.Fatalf("%+v %v", hello, err)
	}
}

func TestClientRefusesOlderDaemon(t *testing.T) {
	h := newHarness(t)
	c := NewClient(SocketPath(h.ph), "2.0.0")
	defer c.Close()
	_, err := c.Do(context.Background(), "GET", "/v1/health", nil, nil)
	e, ok := AsAPIError(err)
	if !ok || e.Update != "daemon" || !strings.Contains(e.Message, "herdsman daemon restart") {
		t.Fatalf("got %v", err)
	}
}

func TestClientRefusesNewerDaemon(t *testing.T) {
	h := newHarness(t)
	c := NewClient(SocketPath(h.ph), "0.1.0")
	defer c.Close()
	_, err := c.Do(context.Background(), "GET", "/v1/health", nil, nil)
	e, ok := AsAPIError(err)
	if !ok || e.Update != "client" {
		t.Fatalf("got %v", err)
	}
}

func TestRouteCreate(t *testing.T) {
	h := newHarness(t)
	var cr CreateResult
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r1"}, &cr)
	if cr.Inbox != filepath.Join(h.ph, "repo-r1") || len(cr.Epoch) != 32 {
		t.Fatalf("%+v", cr)
	}
	st, err := os.Stat(cr.Inbox)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("inbox mode: %v %v", st, err)
	}
	if o, ok := ReadOwner(cr.Inbox); !ok || o.Epoch != cr.Epoch || o.Contract != 2 {
		t.Fatalf("owner %+v", o)
	}
	_, err = h.client().Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r1"}, nil)
	if code(err) != "route_exists" {
		t.Fatalf("second create: %v", err)
	}
	_, err = h.client().Do(context.Background(), "POST", "/v1/routes", CreateRequest{Repo: "../x", Route: "r2"}, nil)
	if code(err) != "bad_request" {
		t.Fatalf("bad name: %v", err)
	}
	for _, l := range retainedLocks {
		if o, ok := daemonLockOwner(filepath.Join(cr.Inbox, l)); !ok || o.Epoch != cr.Epoch {
			t.Fatalf("retained lock %s: %+v", l, o)
		}
	}
	if inbox.TryOnce(filepath.Join(cr.Inbox, inbox.ChannelLock)) != nil {
		t.Fatal("a file-mode writer got the queue lock of a daemon route")
	}
}

func TestRouteList(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "b"}, nil)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "a"}, nil)
	var out struct{ Routes []RouteInfo }
	h.do("GET", "/v1/routes", nil, &out)
	if len(out.Routes) != 2 || out.Routes[0].Route != "a" || out.Routes[0].Owner != "daemon" || out.Routes[0].Status == "" {
		t.Fatalf("%+v", out)
	}
}

func TestRouteRemoveKeepsInbox(t *testing.T) {
	h := newHarness(t)
	_, orch, dir := h.route("r")
	ev, err := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	if f, _ := ev.Next(); f.Event != "snapshot" {
		t.Fatalf("first frame %+v", f)
	}
	h.do("DELETE", "/v1/routes/r", nil, nil)
	f, err := ev.Next()
	if err != nil || f.Event != "route_removed" {
		t.Fatalf("frame %+v %v", f, err)
	}
	if _, err := ev.Next(); err == nil {
		t.Fatal("party stream stayed open after route_removed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("inbox removed: %v", err)
	}
	_, err = h.client().Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil)
	if code(err) != "unknown_route" {
		t.Fatalf("after remove: %v", err)
	}
	if inbox.TryOnce(filepath.Join(dir, inbox.ChannelLock)) == nil {
		t.Fatal("removal kept the queue lock")
	}
}

func TestEnrollRules(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r"}, nil)
	h.fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
	h.fx.SetAgent("p2", herdrtest.Agent("p2", "s2"))
	enroll := func(pane, name, role string) (string, error) {
		c := h.client()
		c.Pane = pane
		var res map[string]string
		_, err := c.Do(context.Background(), "POST", "/v1/routes/r/enroll", EnrollRequest{Name: name, Role: role}, &res)
		return res["pending_sha256"], err
	}
	s1, err := enroll("p1", "adj-1", "adjutant")
	if err != nil || len(s1) != 64 {
		t.Fatalf("enroll: %q %v", s1, err)
	}
	if s, err := enroll("p1", "adj-1", "adjutant"); err != nil || s != s1 {
		t.Fatalf("repeat changed the registry: %q %v", s, err)
	}
	if _, err := enroll("p2", "orch-1", "adjutant"); code(err) != "role_taken" {
		t.Fatalf("role taken: %v", err)
	}
	if _, err := enroll("p1", "adj-2", "orchestrator"); code(err) != "role_taken" {
		t.Fatalf("session taken: %v", err)
	}
	if _, err := enroll("p2", "adj-1", "adjutant"); code(err) != "name_retired" {
		t.Fatalf("immutable name: %v", err)
	}
	if _, err := enroll("", "orch-1", "orchestrator"); code(err) != "unauthorized" {
		t.Fatalf("no pane: %v", err)
	}
	h.fx.Edit("p2", "interactive_ready", nil)
	if _, err := enroll("p2", "orch-1", "orchestrator"); code(err) != "identity_unavailable" {
		t.Fatalf("not ready: %v", err)
	}
	h.fx.Edit("p2", "interactive_ready", true)
	if _, err := enroll("p9", "orch-1", "orchestrator"); code(err) != "identity_unavailable" {
		t.Fatalf("gone pane: %v", err)
	}
	s2, err := enroll("p2", "orch-1", "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: s1}, nil); code(err) != "checksum_mismatch" {
		t.Fatalf("old checksum: %v", err)
	}
	var res ApproveResult
	h.do("POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: s2}, &res)
	h.do("POST", "/v1/routes/r/pair/retire", RetireRequest{Name: "adj-1", SHA256: s2}, nil)
	if _, err := enroll("p1", "adj-1", "adjutant"); code(err) != "name_retired" {
		t.Fatalf("retired name: %v", err)
	}
}

func TestPairApprove(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	var st StatusReport
	h.do("GET", "/v1/routes/r/status", nil, &st)
	if st.Registry.State != "approved" || len(st.Peers) != 2 {
		t.Fatalf("%+v", st)
	}
	for _, p := range []*party{adj, orch} {
		b, err := os.ReadFile(filepath.Join(dir, "state", "credentials", p.name))
		fi, _ := os.Stat(filepath.Join(dir, "state", "credentials", p.name))
		if err != nil || len(strings.TrimSpace(string(b))) != 64 || fi.Mode().Perm() != 0o600 {
			t.Fatalf("credential of %s: %q %v", p.name, b, err)
		}
	}
}

func TestIncompletePairRefused(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r"}, nil)
	h.fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
	c := h.client()
	c.Pane = "p1"
	var res map[string]string
	if _, err := c.Do(context.Background(), "POST", "/v1/routes/r/enroll", EnrollRequest{Name: "adj-1", Role: "adjutant"}, &res); err != nil {
		t.Fatal(err)
	}
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: res["pending_sha256"]}, nil)
	if code(err) != "incomplete_pair" {
		t.Fatalf("got %v", err)
	}
}

func TestApproveCredentialSelection(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r"}, nil)
	var sum string
	for i, p := range []struct{ pane, name, role string }{{"p1", "adj-1", "adjutant"}, {"p2", "orch-1", "orchestrator"}} {
		h.fx.SetAgent(p.pane, herdrtest.Agent(p.pane, "s"+string(rune('1'+i))))
		c := h.client()
		c.Pane = p.pane
		var res map[string]string
		if _, err := c.Do(context.Background(), "POST", "/v1/routes/r/enroll", EnrollRequest{Name: p.name, Role: p.role}, &res); err != nil {
			t.Fatal(err)
		}
		sum = res["pending_sha256"]
	}
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: sum, Credentials: map[string]string{"adj-1": "bogus"}}, nil)
	if code(err) != "bad_request" {
		t.Fatalf("bogus option: %v", err)
	}
	var res ApproveResult
	h.do("POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: sum, Credentials: map[string]string{"adj-1": "identity"}}, &res)
	got := map[string]string{}
	for _, c := range res.Credentials {
		got[c.Name] = c.Option
	}
	if got["adj-1"] != "identity" || got["orch-1"] != "file" {
		t.Fatalf("%+v", res)
	}
	dir := filepath.Join(h.ph, "repo-r")
	if _, err := os.Stat(filepath.Join(dir, "state", "credentials", "adj-1")); err == nil {
		t.Fatal("identity option wrote a credential file")
	}
	// The identity party works with its name and pane only.
	h.setup("r")
	c := h.client()
	c.Pane, c.Name = "p1", "adj-1"
	if _, err := c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); err != nil {
		t.Fatalf("identity party: %v", err)
	}
	// The file party needs its credential.
	c2 := h.client()
	c2.Pane, c2.Name = "p2", "orch-1"
	if _, err := c2.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{ReadKey: key()}, nil); code(err) != "unauthorized" {
		t.Fatalf("file party without credential: %v", err)
	}
}

func TestApproveRevokesOldCredentials(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	old := orch.c.Credential
	// A new orchestrator generation enrolls; the old one retires.
	var st StatusReport
	h.do("GET", "/v1/routes/r/status", nil, &st)
	pb, _ := os.ReadFile(filepath.Join(dir, "peers"))
	h.do("POST", "/v1/routes/r/pair/retire", RetireRequest{Name: orch.name, SHA256: sha256Hex(pb)}, nil)
	if _, err := orch.c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); code(err) != "unauthorized" {
		t.Fatalf("retired credential still works: %v", err)
	}
	h.fx.SetAgent("p3", herdrtest.Agent("p3", "s3"))
	c := h.client()
	c.Pane = "p3"
	var res map[string]string
	if _, err := c.Do(context.Background(), "POST", "/v1/routes/r/enroll", EnrollRequest{Name: "orch-2", Role: "orchestrator"}, &res); err != nil {
		t.Fatal(err)
	}
	var ar ApproveResult
	h.do("POST", "/v1/routes/r/pair/approve", ApproveRequest{SHA256: res["pending_sha256"]}, &ar)
	b, _ := os.ReadFile(filepath.Join(dir, "state", "credentials", adj.name))
	if strings.TrimSpace(string(b)) == adj.c.Credential {
		t.Fatal("re-approval kept the old credential")
	}
	if _, err := adj.c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); code(err) != "unauthorized" {
		t.Fatalf("old credential after re-approval: %v", err)
	}
	if old == "" {
		t.Fatal("no old credential")
	}
}

func TestCredentialRevoke(t *testing.T) {
	h := newHarness(t)
	adj, _, _ := h.route("r")
	h.do("POST", "/v1/routes/r/credentials/"+adj.name+"/revoke", nil, nil)
	if _, err := adj.c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); code(err) != "unauthorized" {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes/r/credentials/nobody/revoke", nil, nil); code(err) != "unknown_name" {
		t.Fatalf("unknown: %v", err)
	}
}

func TestRouteSeparation(t *testing.T) {
	h := newHarness(t)
	adjA, _, _ := h.route("a")
	_, _, _ = h.route("b")
	if _, err := adjA.c.Do(context.Background(), "GET", "/v1/routes/b/status", nil, nil); code(err) != "wrong_route" {
		t.Fatalf("cross-route credential: %v", err)
	}
	if _, err := adjA.c.Do(context.Background(), "GET", "/v1/routes/zz/status", nil, nil); code(err) != "unknown_route" {
		t.Fatalf("unknown route: %v", err)
	}
	// A send can only name a party of its own route.
	_, err := adjA.c.Do(context.Background(), "POST", "/v1/routes/a/messages", SendRequest{To: "orch-b", Kind: "brief", Body: "x", Key: key()}, nil)
	if code(err) != "bad_request" {
		t.Fatalf("send to another route's party: %v", err)
	}
}

func TestIdentityMismatchRefused(t *testing.T) {
	h := newHarness(t)
	adj, _, _ := h.route("r")
	h.fx.SetAgent(adj.pane, herdrtest.Agent(adj.pane, "a-new-session"))
	if _, err := adj.c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); code(err) != "identity_mismatch" {
		t.Fatalf("got %v", err)
	}
	h.fx.Fail("agent.get", "server_error")
	if _, err := adj.c.Do(context.Background(), "GET", "/v1/routes/r/status", nil, nil); code(err) != "herdr_unreachable" {
		t.Fatalf("herdr down: %v", err)
	}
}

func TestSetupWritesProofV2(t *testing.T) {
	h := newHarness(t)
	_, _, dir := h.route("r")
	b, ok := inbox.ReadStrict(dir, filepath.Join(dir, "setup.json"))
	var doc SetupV2
	if !ok || json.Unmarshal(b, &doc) != nil || doc.Version != 2 || doc.Inbox != dir || len(doc.Rules) != 1 || doc.Binary.Path != h.cfg.Executable {
		t.Fatalf("setup %s", b)
	}
	rb, _ := os.ReadFile(h.rules)
	if doc.Rules[0].SHA256 != sha256Hex(rb) {
		t.Fatal("rule checksum is not SHA-256")
	}
	_, err := h.client().Do(context.Background(), "PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{"relative"}, Boundary: json.RawMessage(`{}`)}, nil)
	if code(err) != "bad_request" {
		t.Fatalf("relative rule: %v", err)
	}
}

func TestProofStaleHolds(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "before")
	if err := os.WriteFile(h.rules, []byte("(changed)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "after", Key: key()}, nil)
	if code(err) != "proof_stale" {
		t.Fatalf("stale proof: %v", err)
	}
	var ar AuthorityResult
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" {
		t.Fatalf("%+v", ar)
	}
}

func TestAuthorityRelayOrHuman(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.route("r")
	var ar AuthorityResult
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" || len(ar.Reasons) == 0 {
		t.Fatalf("a boundary without its fields must be HUMAN: %+v", ar)
	}
	boundary := `{"version":1,"inventory_current":true,"private_home":true,"human_checked":true,"protected_domain":true,"launch_admission":true,"courier_panes":[],"lower_trust":[]}`
	h.do("PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{h.rules}, PolicyVerified: true, Boundary: json.RawMessage(boundary)}, nil)
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "RELAY" {
		t.Fatalf("protected setup: %+v", ar)
	}
	h.fx.SetAgent("p9", herdrtest.Agent("p9", "stranger"))
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" || ar.Reasons[0] != "boundary-inventory" {
		t.Fatalf("unknown agent: %+v", ar)
	}
}

func TestSendCommits(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	res := adj.send(t, "r", orch.name, "hello")
	if res.ID != "m000000000001" || res.Seq == 0 {
		t.Fatalf("%+v", res)
	}
	ready := filepath.Join(dir, orch.name, "ready", res.ID+".md")
	env, ok := inbox.Envelope(dir, ready)
	if !ok || env["body"] != "hello" || env["from"] != adj.name {
		t.Fatalf("envelope %v", env)
	}
	a, _ := os.ReadFile(ready)
	b, _ := os.ReadFile(filepath.Join(dir, "ids", res.ID, "envelope"))
	if !bytes.Equal(a, b) {
		t.Fatal("queue file differs from the allocation copy")
	}
	if exists(filepath.Join(dir, orch.name, "new", res.ID+".md")) {
		t.Fatal("a daemon send went through new/")
	}
}

func TestSendCaps(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	// Fill the backlog with ready files directly (1000 pending).
	box := filepath.Join(dir, orch.name, "ready")
	for i := range backlogMax {
		_ = os.WriteFile(filepath.Join(box, "m9"+strings.Repeat("0", 7)+string(rune('0'+i/100%10))+string(rune('0'+i/10%10))+string(rune('0'+i%10))+".md"), []byte("{}"), 0o600)
	}
	_, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "x", Key: key()}, nil)
	if code(err) != "backlog_full" {
		t.Fatalf("got %v", err)
	}
	_, err = adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "side_agent_data", Body: "x", Key: key()}, nil)
	if code(err) != "human_boundary" {
		t.Fatalf("side agent data under HUMAN: %v", err)
	}
}

func TestSendKeyConflict(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	k := key()
	var a, b SendResult
	if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "x", Key: k}, &a); err != nil {
		t.Fatal(err)
	}
	st, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", SendRequest{To: orch.name, Kind: "brief", Body: "x", Key: k}, &b)
	if err != nil || st != 200 || !b.Duplicate || b.ID != a.ID {
		t.Fatalf("retry: %d %+v %v", st, b, err)
	}
	for _, q := range []SendRequest{
		{To: orch.name, Kind: "brief", Body: "y", Key: k},
		{To: orch.name, Kind: "result", Body: "x", Key: k},
		{To: orch.name, Kind: "brief", Body: "x", Key: k, ReplyTo: a.ID},
	} {
		if _, err := adj.c.Do(context.Background(), "POST", "/v1/routes/r/messages", q, nil); code(err) != "key_conflict" {
			t.Fatalf("%+v: %v", q, err)
		}
	}
}

func TestReadClaim(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	m1 := adj.send(t, "r", orch.name, "one")
	m2 := adj.send(t, "r", orch.name, "two")
	cl := orch.read(t, "r")
	if len(cl.Manifest) != 2 || cl.Manifest[0].ID != m1.ID || cl.Manifest[1].ID != m2.ID || len(cl.Claim) != 32 {
		t.Fatalf("%+v", cl)
	}
	if cl.Manifest[0].Path != filepath.Join(dir, orch.name, "cur", m1.ID+".md") {
		t.Fatalf("path %s", cl.Manifest[0].Path)
	}
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{ReadKey: key()}, nil); code(err) != "claim_held" {
		t.Fatalf("second reader: %v", err)
	}
	if _, err := orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil); code(err) != "unacked_work" {
		t.Fatalf("release with work: %v", err)
	}
	orch.ack(t, "r", cl.Claim, m1.ID)
	orch.ack(t, "r", cl.Claim, m2.ID)
	if !(&inbox.Root{Dir: dir}).AckValid(filepath.Join(dir, orch.name), m1.ID) {
		t.Fatal("the daemon's ack does not validate as a file-mode ack")
	}
	if _, err := orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, orch.name, "claim")) {
		t.Fatal("claim folder left after release")
	}
	if _, err := orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil); code(err) != "no_claim" {
		t.Fatalf("second release: %v", err)
	}
}

func TestReadLostResponse(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "one")
	k := key()
	var a, b ClaimResult
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{ReadKey: k}, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{ReadKey: k}, &b); err != nil {
		t.Fatal(err)
	}
	if a.Claim != b.Claim || len(b.Manifest) != 1 {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestReadDuringActiveClaim(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "one")
	cl := orch.read(t, "r")
	m2 := adj.send(t, "r", orch.name, "two")
	orch.ack(t, "r", cl.Claim, cl.Manifest[0].ID)
	if _, err := orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/claims/"+cl.Claim, nil, nil); code(err) != "unacked_work" {
		t.Fatalf("release with a new arrival in ready: %v", err)
	}
	ek := key()
	var ex ClaimResult
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{Token: cl.Claim, ExtendKey: ek}, &ex); err != nil {
		t.Fatal(err)
	}
	if len(ex.Manifest) != 1 || ex.Manifest[0].ID != m2.ID {
		t.Fatalf("%+v", ex)
	}
	// A lost extension response retried with its key binds nothing twice.
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{Token: cl.Claim, ExtendKey: ek}, &ex); err != nil || len(ex.Manifest) != 1 {
		t.Fatalf("retry %+v %v", ex, err)
	}
	orch.ack(t, "r", cl.Claim, m2.ID)
	// An empty extension, then an arrival: the retry with the same key
	// returns the same empty result and leaves the arrival in ready/.
	ek2 := key()
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{Token: cl.Claim, ExtendKey: ek2}, &ex); err != nil || len(ex.Manifest) != 0 {
		t.Fatalf("empty %+v %v", ex, err)
	}
	m3 := adj.send(t, "r", orch.name, "three")
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{Token: cl.Claim, ExtendKey: ek2}, &ex); err != nil || len(ex.Manifest) != 0 {
		t.Fatalf("empty retry %+v %v", ex, err)
	}
	if !exists(filepath.Join(h.ph, "repo-r", orch.name, "ready", m3.ID+".md")) {
		t.Fatal("the retry bound a new arrival")
	}
	if _, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims", ClaimRequest{Token: "0123456789abcdef0123456789abcdef", ExtendKey: key()}, nil); code(err) != "no_claim" {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestAckIdempotent(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	m := adj.send(t, "r", orch.name, "one")
	cl := orch.read(t, "r")
	q := AckRequest{ID: m.ID, Record: "intake note"}
	var a AckResult
	st, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims/"+cl.Claim+"/acks", q, &a)
	if err != nil || st != 201 || a.Already {
		t.Fatalf("%d %+v %v", st, a, err)
	}
	st, err = orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims/"+cl.Claim+"/acks", q, &a)
	if err != nil || st != 200 || !a.Already {
		t.Fatalf("repeat %d %+v %v", st, a, err)
	}
}

func TestAckTerminalConflict(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	m := adj.send(t, "r", orch.name, "one")
	cl := orch.read(t, "r")
	orch.ack(t, "r", cl.Claim, m.ID)
	_, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims/"+cl.Claim+"/acks", AckRequest{ID: m.ID, Record: "another record"}, nil)
	if code(err) != "terminal_conflict" {
		t.Fatalf("got %v", err)
	}
}

func TestAckTooLarge(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	m := adj.send(t, "r", orch.name, "one")
	cl := orch.read(t, "r")
	// Quotes double when encoded: under the bound raw, over it encoded.
	rec := strings.Repeat(`"`, inbox.MaxSize/2+10)
	_, err := orch.c.Do(context.Background(), "POST", "/v1/routes/r/claims/"+cl.Claim+"/acks", AckRequest{ID: m.ID, Record: rec}, nil)
	if code(err) != "ack_too_large" {
		t.Fatalf("got %v", err)
	}
	if exists(filepath.Join(dir, orch.name, "ack", m.ID+".md.json")) || exists(filepath.Join(dir, "ids", m.ID, "terminal.json")) || exists(filepath.Join(dir, "ids", m.ID, "ack.json")) {
		t.Fatal("a refused ack wrote a file")
	}
}

func TestHoldOwners(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	var res HoldResult
	if _, err := adj.c.Do(context.Background(), "PUT", "/v1/routes/r/holds/"+orch.name, HoldRequest{Reason: "wait"}, &res); err != nil || len(res.Holds) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := orch.c.Do(context.Background(), "PUT", "/v1/routes/r/holds/"+orch.name, HoldRequest{Reason: "me too"}, &res); err != nil || len(res.Holds) != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := adj.c.Do(context.Background(), "DELETE", "/v1/routes/r/holds/"+orch.name, nil, &res); err != nil || len(res.Holds) != 1 || res.Holds[0] != orch.name {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := adj.c.Do(context.Background(), "DELETE", "/v1/routes/r/holds/"+orch.name, nil, nil); code(err) != "no_hold" {
		t.Fatalf("no hold: %v", err)
	}
	if _, err := adj.c.Do(context.Background(), "PUT", "/v1/routes/r/holds/nobody", HoldRequest{Reason: "x"}, nil); code(err) != "unknown_target" {
		t.Fatalf("unknown target: %v", err)
	}
	if _, err := adj.c.Do(context.Background(), "PUT", "/v1/routes/r/holds/"+orch.name, HoldRequest{Reason: strings.Repeat("x", 301)}, nil); code(err) != "bad_request" {
		t.Fatalf("long reason: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "state", "hold-"+orch.name))
	if !strings.Contains(string(b), `"holds"`) || !strings.Contains(string(b), orch.name) {
		t.Fatalf("hold file %s", b)
	}
	_, _ = orch.c.Do(context.Background(), "DELETE", "/v1/routes/r/holds/"+orch.name, nil, nil)
	if exists(filepath.Join(dir, "state", "hold-"+orch.name)) {
		t.Fatal("hold file left with no owner")
	}
}

// waitFrame reads frames until one of type typ.
func waitFrame(t testing.TB, ev *EventReader, typ string) SSEFrame {
	t.Helper()
	for {
		f, err := ev.Next()
		if err != nil {
			t.Fatalf("waiting for %s: %v", typ, err)
		}
		if f.Event == typ {
			return f
		}
	}
}

func TestEventShapes(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	evO, err := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer evO.Close()
	evA, err := adj.c.Events(context.Background(), "/v1/routes/r/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer evA.Close()
	snap := waitFrame(t, evO, "snapshot")
	var s Event
	if json.Unmarshal(snap.Data, &s) != nil || s.Snapshot == nil || s.LastSeq == nil || s.Unread == nil || snap.ID == "" {
		t.Fatalf("snapshot %s", snap.Data)
	}
	waitFrame(t, evA, "snapshot")
	m := adj.send(t, "r", orch.name, "one")
	f := waitFrame(t, evO, "message")
	var got map[string]any
	_ = json.Unmarshal(f.Data, &got)
	for _, k := range []string{"v", "route", "seq", "epoch", "time", "type", "id", "from", "to", "kind", "reply_to"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("message event lacks %s: %s", k, f.Data)
		}
	}
	if got["id"] != m.ID || f.ID != got["epoch"].(string)+"-"+jsonNum(got["seq"]) {
		t.Fatalf("message %s id %s", f.Data, f.ID)
	}
	if strings.Contains(string(f.Data), "one") {
		t.Fatal("an event carries the body")
	}
	cl := orch.read(t, "r")
	orch.ack(t, "r", cl.Claim, m.ID)
	f = waitFrame(t, evA, "receipt")
	_ = json.Unmarshal(f.Data, &got)
	if got["id"] != m.ID || got["by"] != orch.name || got["state"] != "handled" {
		t.Fatalf("receipt %s", f.Data)
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestResumeAfterCursor(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	ev, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	snap := waitFrame(t, ev, "snapshot")
	ev.Close()
	m1 := adj.send(t, "r", orch.name, "one")
	m2 := adj.send(t, "r", orch.name, "two")
	ev, err := orch.c.Events(context.Background(), "/v1/routes/r/events", snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	f1 := waitFrame(t, ev, "message")
	f2 := waitFrame(t, ev, "message")
	if !strings.Contains(string(f1.Data), m1.ID) || !strings.Contains(string(f2.Data), m2.ID) {
		t.Fatalf("%s %s", f1.Data, f2.Data)
	}
	// From the first message's cursor, only the second is replayed.
	ev2, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", f1.ID)
	defer ev2.Close()
	if f := waitFrame(t, ev2, "message"); f.ID != f2.ID {
		t.Fatalf("replayed %s, want %s", f.ID, f2.ID)
	}
	// After a restart, the replay comes from the journal files.
	h.restart()
	orch.c = h.partyClient(filepath.Join(h.ph, "repo-r"), orch)
	ev3, err := orch.c.Events(context.Background(), "/v1/routes/r/events", snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer ev3.Close()
	if f := waitFrame(t, ev3, "message"); f.ID != f1.ID {
		t.Fatalf("after restart: %s, want %s", f.ID, f1.ID)
	}
}

func TestResetOnEpoch(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	ev, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	snap := waitFrame(t, ev, "snapshot")
	var res struct{ Epoch string }
	h.do("POST", "/v1/routes/r/reset-events", ResetRequest{ConfirmRoute: "r"}, &res)
	// The connected stream gets reset, then a snapshot of the new epoch.
	f := waitFrame(t, ev, "reset")
	if !strings.Contains(string(f.Data), "epoch_changed") || !strings.Contains(string(f.Data), res.Epoch) {
		t.Fatalf("reset %s", f.Data)
	}
	if f := waitFrame(t, ev, "snapshot"); !strings.HasPrefix(f.ID, res.Epoch) {
		t.Fatalf("snapshot %s", f.ID)
	}
	ev.Close()
	// A stream with the old cursor gets reset then snapshot first.
	ev2, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", snap.ID)
	defer ev2.Close()
	if f, _ := ev2.Next(); f.Event != "reset" {
		t.Fatalf("first %+v", f)
	}
	if f, _ := ev2.Next(); f.Event != "snapshot" {
		t.Fatalf("second %+v", f)
	}
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes/r/reset-events", ResetRequest{ConfirmRoute: "x"}, nil); code(err) != "bad_request" {
		t.Fatalf("confirm: %v", err)
	}
}

func TestResetOnExpiredCursor(t *testing.T) {
	old := journalRotate
	journalRotate = 4096
	t.Cleanup(func() { journalRotate = old })
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	ev, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	snap := waitFrame(t, ev, "snapshot")
	ev.Close()
	for range 40 {
		adj.send(t, "r", orch.name, "x")
	}
	ev2, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", snap.ID)
	defer ev2.Close()
	f, _ := ev2.Next()
	if f.Event != "reset" || !strings.Contains(string(f.Data), "cursor_expired") {
		t.Fatalf("got %+v %s", f, f.Data)
	}
	if f, _ := ev2.Next(); f.Event != "snapshot" {
		t.Fatalf("second %+v", f)
	}
}

func TestSnapshotStreamHandoff(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	done := make(chan []string)
	go func() {
		var ids []string
		for range 30 {
			ids = append(ids, adj.send(t, "r", orch.name, "x").ID)
		}
		done <- ids
	}()
	time.Sleep(time.Millisecond)
	ev, err := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	snap := waitFrame(t, ev, "snapshot")
	var s Event
	_ = json.Unmarshal(snap.Data, &s)
	seen := map[string]int{}
	for _, id := range s.Unread {
		seen[id]++
	}
	ids := <-done
	for len(seen) < len(ids) {
		f := waitFrame(t, ev, "message")
		var m Event
		_ = json.Unmarshal(f.Data, &m)
		seen[m.ID]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("%s seen %d times", id, seen[id])
		}
	}
}

func TestViewReconnectMultipleRoutes(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.route("a")
	_, _, _ = h.route("b")
	view := h.client()
	ev, err := view.Events(context.Background(), "/v1/events", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := waitFrame(t, ev, "registry")
	var r Event
	_ = json.Unmarshal(reg.Data, &r)
	if len(r.Routes) != 2 || r.Routes[0].Route != "a" {
		t.Fatalf("registry %s", reg.Data)
	}
	got := map[string]bool{}
	for range 2 {
		f := waitFrame(t, ev, "snapshot")
		var s Event
		_ = json.Unmarshal(f.Data, &s)
		got[s.Route] = true
		if s.Unread != nil {
			t.Fatal("the view's snapshot carries unread ids")
		}
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("%v", got)
	}
	// A route created while the view is connected sends its snapshot.
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "c"}, nil)
	f := waitFrame(t, ev, "snapshot")
	if !strings.Contains(string(f.Data), `"route":"c"`) {
		t.Fatalf("%s", f.Data)
	}
	ev.Close()
	// After a reconnect, the registry omits a removed route.
	h.do("DELETE", "/v1/routes/a", nil, nil)
	ev2, _ := view.Events(context.Background(), "/v1/events", "")
	defer ev2.Close()
	reg = waitFrame(t, ev2, "registry")
	_ = json.Unmarshal(reg.Data, &r)
	if len(r.Routes) != 2 || r.Routes[0].Route != "b" {
		t.Fatalf("registry after removal %s", reg.Data)
	}
}

func TestHealthDegradesOneRoute(t *testing.T) {
	h := newHarness(t)
	_, _, _ = h.route("a")
	_, _, _ = h.route("b")
	rt, _ := h.d.route("a")
	for range 3 {
		_ = rt.do(context.Background(), func() *APIError { panic("boom") })
	}
	var hl Health
	h.do("GET", "/v1/health", nil, &hl)
	st := map[string]string{}
	for _, r := range hl.Routes {
		st[r.Route] = r.Status
	}
	if st["a"] != "degraded" || st["b"] != "ok" || hl.Status != "degraded" {
		t.Fatalf("%+v", hl)
	}
}

func TestBodyBound(t *testing.T) {
	h := newHarness(t)
	big := map[string]string{"repo": strings.Repeat("x", maxBody), "route": "r"}
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes", big, nil); code(err) != "bad_request" {
		t.Fatalf("big body: %v", err)
	}
	dup := []byte(`{"repo":"a","repo":"b","route":"r"}`)
	req, _ := http.NewRequest("POST", "http://herdsman/v1/routes", bytes.NewReader(dup))
	req.Header.Set(HeaderVersion, testVersion)
	req.Header.Set(HeaderProtocol, "1")
	resp, err := h.client().hc.Do(req)
	if err != nil || resp.StatusCode != 400 {
		t.Fatalf("duplicate key: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestSingletonPerUser(t *testing.T) {
	h := newHarness(t)
	// Same account, same private home.
	d2 := New(h.cfg)
	err := d2.Start()
	var running *ErrRunning
	if !errors.As(err, &running) || running.Holder.PID != os.Getpid() || !strings.Contains(err.Error(), "another daemon runs for this user") {
		t.Fatalf("second daemon: %v", err)
	}
	// Same account, another private home.
	cfg := h.cfg
	cfg.PrivateHome = filepath.Join(h.dir, "q")
	if err := New(cfg).Start(); !errors.As(err, &running) {
		t.Fatalf("another private home: %v", err)
	}
	// After the first stops (the kernel frees the lock), a start succeeds.
	h.stop()
	if !AccountLockHeld(h.home) {
		h.start()
	} else {
		t.Fatal("the account lock outlived its daemon")
	}
}

func TestDiscoveryOtherPrivateHome(t *testing.T) {
	h := newHarness(t)
	l, ok := ReadAccountLock(h.home)
	if !ok || l.PrivateHome != h.ph || l.Socket != SocketPath(h.ph) || l.Version != testVersion {
		t.Fatalf("%+v", l)
	}
}

func TestShutdownDrains(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	ev, _ := orch.c.Events(context.Background(), "/v1/routes/r/events", "")
	waitFrame(t, ev, "snapshot")
	h.do("POST", "/v1/shutdown", ShutdownRequest{Reason: "stop"}, nil)
	f := waitFrame(t, ev, "shutdown")
	if !strings.Contains(string(f.Data), "stop") {
		t.Fatalf("%s", f.Data)
	}
	select {
	case <-h.d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestDaemonListensOnlyOnSocket(t *testing.T) {
	h := newHarness(t)
	if h.d.ln.Addr().Network() != "unix" {
		t.Fatalf("listener on %s", h.d.ln.Addr().Network())
	}
	st, err := os.Stat(SocketPath(h.ph))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", st, err)
	}
}

func TestErrorCodesAndExits(t *testing.T) {
	for code, spec := range errorTable {
		e := Errf(code, "x")
		if e.Code != code || e.Status != spec.status || e.Retryable != spec.retryable || ExitCode(code) != spec.exit {
			t.Fatalf("%s: %+v", code, e)
		}
	}
	if Errf("no_such_code", "x").Code != "internal" || ExitCode("no_such_code") != 1 {
		t.Fatal("an unknown code is internal")
	}
	if ExitCode("human_boundary") != 3 || ExitCode("busy") != 75 {
		t.Fatal("special exits")
	}
}
