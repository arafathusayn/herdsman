package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// fileInbox makes a slice 3 file-mode inbox under the private home with an
// approved pair (adj-1 in p1, orch-1 in p2), a contract 1 proof, one ready
// message, one claimed and acked message and the sequence at 2.
func fileInbox(t testing.TB, h *harness) (dir, sum string) {
	t.Helper()
	dir = filepath.Join(h.ph, "repo-old")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = mkdirs(dir, "state", "ids", "enrolled", "alerts")
	h.fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
	h.fx.SetAgent("p2", herdrtest.Agent("p2", "s2"))
	adjID := inbox.Identity{Pane: "p1", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s1"}}
	orchID := inbox.Identity{Pane: "p2", Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s2"}}
	reg := prettyJSON(obj("adj-1", obj("role", "adjutant", "identity", identityObj(adjID)), "orch-1", obj("role", "orchestrator", "identity", identityObj(orchID))))
	_ = os.WriteFile(filepath.Join(dir, "peers"), reg, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "peers.approved"), reg, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "setup.json"), []byte(`{"inbox":"`+dir+`","policy_verified":true,"rules":[],"binary":{"path":"/x","checksum":"1-1"}}`+"\n"), 0o600)
	for _, b := range []string{"adj-1", "orch-1"} {
		_ = mkdirs(dir, b+"/tmp", b+"/new", b+"/ready", b+"/cur", b+"/quarantine", b+"/ack", b+"/notices")
	}
	for i, id := range []string{"m000000000001", "m000000000002"} {
		env := prettyJSON(obj("version", 1, "id", id, "from", "adj-1", "to", "orch-1", "from_identity", identityObj(adjID), "to_identity", identityObj(orchID),
			"time", 1700000000+i, "kind", "brief", "reply_to", "", "body", "old "+id))
		_ = mkdirs(dir, "ids/"+id)
		_ = os.WriteFile(filepath.Join(dir, "ids", id, "envelope"), env, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "orch-1", "ready", id+".md"), env, 0o600)
	}
	_ = os.WriteFile(filepath.Join(dir, "state", "sequence"), []byte("2\n"), 0o600)
	return dir, sha256Hex(reg)
}

func TestRouteAdopt(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	if res.Route != "old" || !res.LegacyProof || res.LastMessageID != "m000000000002" || len(res.Epoch) != 32 {
		t.Fatalf("%+v", res)
	}
	if o, ok := ReadOwner(dir); !ok || o.AdoptedFrom == nil || *o.AdoptedFrom != "file" || o.Epoch != res.Epoch {
		t.Fatalf("owner %+v", o)
	}
	if exists(filepath.Join(dir, "setup.json")) || !exists(filepath.Join(dir, "state", "legacy", "setup-v1.json")) {
		t.Fatal("the old proof was not moved to state/legacy")
	}
	for _, l := range retainedLocks {
		if _, ok := daemonLockOwner(filepath.Join(dir, l)); !ok {
			t.Fatalf("%s not held", l)
		}
	}
	if exists(filepath.Join(dir, inbox.MutationLock)) || exists(filepath.Join(dir, "state", "adopting.json")) {
		t.Fatal("the input lock or the marker stayed")
	}
	if pl := h.d.readPending(); len(pl.Inboxes) != 0 {
		t.Fatalf("pending %+v", pl)
	}
	if !exists(filepath.Join(dir, "state", "credentials", "adj-1")) {
		t.Fatal("no credential issued at adoption")
	}
	// Authority is HUMAN until setup writes the version 2 proof; the old
	// queue is then served by the daemon.
	var ar AuthorityResult
	h.do("GET", "/v1/routes/old/authority", nil, &ar)
	if ar.Authority != "HUMAN" {
		t.Fatalf("%+v", ar)
	}
	h.setup("old")
	orch := &party{name: "orch-1", pane: "p2"}
	orch.c = h.partyClient(dir, orch)
	cl := orch.read(t, "old")
	if len(cl.Manifest) != 2 {
		t.Fatalf("%+v", cl)
	}
	orch.ack(t, "old", cl.Claim, "m000000000001")
	adj := &party{name: "adj-1", pane: "p1"}
	adj.c = h.partyClient(dir, adj)
	if m := adj.send(t, "old", "orch-1", "new"); m.ID != "m000000000003" {
		t.Fatalf("the sequence was not kept: %s", m.ID)
	}
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil); code(err) != "route_exists" {
		t.Fatalf("second adopt: %v", err)
	}
}

func TestAdoptLockedByOther(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	_ = os.Mkdir(filepath.Join(dir, inbox.ChannelLock), 0o700)
	_ = os.WriteFile(filepath.Join(dir, inbox.ChannelLock, "pid"), []byte("4242\n"), 0o600)
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if code(err) != "adopt_locked" || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("got %v", err)
	}
	if exists(filepath.Join(dir, inbox.MutationLock)) || !exists(filepath.Join(dir, "setup.json")) || exists(filepath.Join(dir, "state", "owner.json")) {
		t.Fatal("a refused adoption left state behind")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, inbox.ChannelLock, "pid")); string(b) != "4242\n" {
		t.Fatal("another owner's lock was touched")
	}
}

func TestAdoptRefusesWrongPair(t *testing.T) {
	h := newHarness(t)
	dir, _ := fileInbox(t, h)
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: strings.Repeat("0", 64)}, nil)
	if code(err) != "checksum_mismatch" {
		t.Fatalf("got %v", err)
	}
	_, err = h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: "/tmp", ConfirmPairSHA256: "x"}, nil)
	if code(err) != "bad_request" {
		t.Fatalf("outside the private home: %v", err)
	}
}

func TestAdoptDrainsPausedHold(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	procs := []Proc{{PID: 999999, Command: "herdsman message hold orch-1 x", Env: []string{"HERDSMAN_INBOX=" + dir}}}
	h.cfg.Procs = func() ([]Proc, error) { return procs, nil }
	h.start()
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if code(err) != "adopt_busy" || !strings.Contains(err.Error(), "pid 999999") {
		t.Fatalf("got %v", err)
	}
	// Only the lock folders, the markers and the moved proof are written.
	if exists(filepath.Join(dir, "state", "owner.json")) || exists(filepath.Join(dir, "state", "events")) {
		t.Fatal("published before the drain")
	}
	if rf, _ := ReadRoutes(h.ph); len(rf.Routes) != 0 {
		t.Fatal("registered before the drain")
	}
	if !exists(filepath.Join(dir, "state", "adopting.json")) || exists(filepath.Join(dir, "setup.json")) {
		t.Fatal("marker or proof move missing")
	}
	// A process of another inbox does not count; the adoption resumes.
	procs = []Proc{{PID: 999998, Command: "herdsman message hold x y", Env: []string{"HERDSMAN_INBOX=/elsewhere"}}}
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	if res.Route != "old" || exists(filepath.Join(dir, "state", "adopting.json")) {
		t.Fatalf("%+v", res)
	}
}

func TestAdoptDrainUnreadableEnvMatches(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	h.cfg.Procs = func() ([]Proc, error) {
		return []Proc{{PID: 999997, Command: "/bin/bash message-courier.sh", Env: nil}}, nil
	}
	h.start()
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if code(err) != "adopt_busy" {
		t.Fatalf("a process whose environment cannot be read must count: %v", err)
	}
}

// adoptCrash runs an adoption with a crash injected at point, restarts the
// daemon without the hook, and checks the adoption completes.
func adoptCrash(t *testing.T, point string) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	h.cfg.Crash = func(p string) {
		if p == point {
			panic(crashSignal{p})
		}
	}
	h.cfg.Log = func(string) {}
	h.start()
	_, _ = h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	h.stop()
	h.cfg.Crash = nil
	h.start()
	// A start resumes an adoption found through the pending list or the
	// marker; one that crashed before either is run again by the human.
	if _, e := h.d.route("old"); e != nil {
		var res AdoptResult
		h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	}
	if _, e := h.d.route("old"); e != nil {
		t.Fatalf("after %s: %v", point, e)
	}
	if exists(filepath.Join(dir, "state", "adopting.json")) || exists(filepath.Join(dir, inbox.MutationLock)) || exists(filepath.Join(dir, "setup.json")) {
		t.Fatalf("after %s: adoption state left", point)
	}
	if o, ok := ReadOwner(dir); !ok || o.Contract != 2 {
		t.Fatalf("after %s: owner %+v", point, o)
	}
	if pl := h.d.readPending(); len(pl.Inboxes) != 0 {
		t.Fatalf("after %s: pending %+v", point, pl)
	}
}

func TestAdoptCrashAfterLocks(t *testing.T)     { adoptCrash(t, "adopt.locks") }
func TestAdoptCrashBeforeRegistry(t *testing.T) { adoptCrash(t, "adopt.owner") }
func TestAdoptCrashAfterOwner(t *testing.T)     { adoptCrash(t, "adopt.registered") }

func TestAdoptCrashBeforeDrainCompletes(t *testing.T) {
	for _, p := range []string{"adopt.pending", "adopt.marker", "adopt.invalidated", "adopt.committed"} {
		t.Run(p, func(t *testing.T) { adoptCrash(t, p) })
	}
}
