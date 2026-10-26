package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// TestInputLockExclusive: the shared input lock is exclusive between two
// requests of one daemon and against any existing holder; a daemon-owned
// folder from another holder is never adopted nor removed (3.7).
func TestInputLockExclusive(t *testing.T) {
	x := newDirect(t)
	unlock, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.inputLock(); e == nil || e.Code != "busy" {
		t.Fatalf("a second request took the held lock: %v", e)
	}
	unlock()
	if exists(x.path(inbox.MutationLock)) {
		t.Fatal("the lock was not released")
	}
	// A folder held by another daemon process stays its own.
	held := x.path(inbox.MutationLock)
	_ = os.Mkdir(held, 0o700)
	b, _ := encode(lockOwner{Owner: "daemon", Epoch: "other", PID: 1})
	_ = os.WriteFile(filepath.Join(held, "owner.json"), b, 0o600)
	if _, e := x.r.inputLock(); e == nil || e.Code != "busy" {
		t.Fatalf("an existing daemon lock was adopted: %v", e)
	}
	if o, ok := daemonLockOwner(held); !ok || o.Epoch != "other" {
		t.Fatalf("the holder's record changed: %+v", o)
	}
	// Releasing a lock that is no longer this request's leaves it alone.
	dropInputLock(held, "mine")
	if !exists(filepath.Join(held, "owner.json")) {
		t.Fatal("another holder's lock was removed")
	}
}

// enrollPair enrolls an adjutant and an orchestrator in a direct route.
func enrollPair(t *testing.T, x *direct, adj, orch string) {
	t.Helper()
	for _, p := range []struct{ name, role string }{{adj, "adjutant"}, {orch, "orchestrator"}} {
		id := inbox.Identity{Pane: "p-" + p.name, Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s-" + p.name}}
		if _, e := x.r.enroll(p.name, p.role, id); e != nil {
			t.Fatalf("enroll %s: %v", p.name, e)
		}
	}
}

// approvePair enrolls and approves a pair in a direct route.
func approvePair(t *testing.T, x *direct, adj, orch string) ApproveResult {
	t.Helper()
	enrollPair(t, x, adj, orch)
	pb, _ := os.ReadFile(x.path("peers"))
	res, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)})
	if e != nil {
		t.Fatalf("approve: %v", e)
	}
	return res
}

// secrets reads the active file secrets.
func (x *direct) secrets() map[string]string {
	out := map[string]string{}
	ents, _ := os.ReadDir(x.path("state", "credentials"))
	for _, e := range ents {
		if e.Type().IsRegular() {
			b, _ := os.ReadFile(x.path("state", "credentials", e.Name()))
			out[e.Name()] = string(b)
		}
	}
	return out
}

// newPairRoute is a direct route with an empty registry, so a test enrolls
// and approves its own pair.
func newPairRoute(t *testing.T) *direct {
	x := newDirect(t)
	x.kill()
	_ = os.WriteFile(x.path("peers"), []byte("{}\n"), 0o600)
	_ = os.WriteFile(x.path("peers.approved"), []byte("{}\n"), 0o600)
	x.open()
	return x
}

// TestApproveFailureAtomic: an approval that fails before its commit (a
// staged secret, the payload or the journal) leaves the active secrets,
// their records and the accepted hashes exactly as they were, and no
// staged secret behind; a first approval that fails leaves nothing.
func TestApproveFailureAtomic(t *testing.T) {
	for _, where := range []string{"staged-", "state/registry", "write "} {
		t.Run(where, func(t *testing.T) {
			x := newPairRoute(t)
			enrollPair(t, x, "a1", "o1")
			// A first approval that fails leaves no secret.
			restore := failWhere(where)
			pb, _ := os.ReadFile(x.path("peers"))
			if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)}); e == nil {
				t.Fatal("approve succeeded with a failing step")
			}
			restore()
			if len(x.secrets()) != 0 || len(x.r.creds) != 0 {
				t.Fatalf("a failed first approval left %v %v", x.secrets(), x.r.creds)
			}
			if staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*")); len(staged) != 0 && len(x.r.unfinished) == 0 {
				t.Fatalf("staged secrets left: %v", staged)
			}
			if len(x.r.unfinished) != 0 {
				return // committed; the barrier finishes it (the fault matrix covers it)
			}
			approvePair(t, x, "a1", "o1")
			before, creds := x.secrets(), x.r.creds
			credJSON, _ := os.ReadFile(x.path("state", "credentials.json"))
			restore = failWhere(where)
			pb, _ = os.ReadFile(x.path("peers"))
			if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)}); e == nil {
				t.Fatal("re-approval succeeded with a failing step")
			}
			restore()
			after := x.secrets()
			now, _ := os.ReadFile(x.path("state", "credentials.json"))
			if len(after) != 2 || after["a1"] != before["a1"] || after["o1"] != before["o1"] || string(now) != string(credJSON) {
				t.Fatalf("a refused approval changed the active secrets: %v -> %v", before, after)
			}
			for n, c := range creds {
				if !secretMatches(x.path("state", "credentials", n), c.SHA256) {
					t.Fatalf("%s: the active secret no longer matches its accepted hash", n)
				}
			}
			if staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*")); len(staged) != 0 {
				t.Fatalf("staged secrets left: %v", staged)
			}
		})
	}
}

// TestNewPairRestartKeepsCredentials: approve a pair, retire one party,
// approve a new pair with a fresh name, restart: replaying the older
// approval never deletes the newer party's secret, and every active
// secret matches its record (4.4, 7.7).
func TestNewPairRestartKeepsCredentials(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	pb, _ := os.ReadFile(x.path("peers"))
	if _, e := x.r.retire("o1", sha256Hex(pb)); e != nil {
		t.Fatal(e)
	}
	approvePair(t, x, "a1", "o2")
	want := x.secrets()
	if _, ok := want["o2"]; !ok || len(want) != 2 {
		t.Fatalf("secrets %v", want)
	}
	x.kill()
	x.open()
	got := x.secrets()
	if len(got) != 2 || got["o2"] != want["o2"] || got["a1"] != want["a1"] {
		t.Fatalf("after restart %v, want %v", got, want)
	}
	for n, c := range x.r.creds {
		if !secretMatches(x.path("state", "credentials", n), c.SHA256) {
			t.Fatalf("%s does not match its record", n)
		}
	}
}

// TestApproveCrashActivates: a crash after the approval's commit and
// before its activation leaves the new generation staged; the restart
// activates it, and the secrets match the committed hashes.
func TestApproveCrashActivates(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	f := &faults{}
	oldRename := rename
	rename = func(a, b string) error {
		if strings.Contains(a, "staged-") {
			f.n++
			if f.n == 2 { // after the first secret moved
				panic(crashSignal{"activate"})
			}
		}
		return oldRename(a, b)
	}
	func() {
		defer func() { _ = recover() }()
		pb, _ := os.ReadFile(x.path("peers"))
		_, _ = x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)})
	}()
	rename = oldRename
	x.kill()
	x.open()
	for n, c := range x.r.creds {
		if !secretMatches(x.path("state", "credentials", n), c.SHA256) {
			t.Fatalf("%s does not match its record after the restart", n)
		}
	}
	if staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*")); len(staged) != 0 {
		t.Fatalf("staged secrets left: %v", staged)
	}
}

// TestPendingExposureForcesHuman: a waiting exposure record makes the
// authority HUMAN and is folded under the input lock; an observed unknown
// agent is recorded, so RELAY does not come back when it leaves (8).
func TestPendingExposureForcesHuman(t *testing.T) {
	h := newHarness(t)
	_, _, dir := h.route("r")
	boundary := `{"version":1,"inventory_current":true,"private_home":true,"human_checked":true,"protected_domain":true,"launch_admission":true,"courier_panes":[],"lower_trust":[]}`
	h.do("PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{h.rules}, PolicyVerified: true, Boundary: json.RawMessage(boundary)}, nil)
	var ar AuthorityResult
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "RELAY" {
		t.Fatalf("protected setup: %+v", ar)
	}
	// A waiting exposure.
	_ = os.MkdirAll(filepath.Join(dir, "state", "exposure-pending"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "state", "exposure-pending", "1-1-1.json"), []byte(`{"version":1,"time":1,"unknown":[]}`), 0o600)
	h.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" || ar.Reasons[0] != "boundary-pending" {
		t.Fatalf("a waiting exposure: %+v", ar)
	}
	if !exists(filepath.Join(dir, "state", "boundary-tainted")) {
		t.Fatal("the waiting exposure was not folded under the input lock")
	}
	// A fresh route: an observed unknown agent stays recorded after it leaves.
	h2 := newHarness(t)
	_, _, dir2 := h2.route("r")
	h2.do("PUT", "/v1/routes/r/setup", SetupRequest{Rules: []string{h2.rules}, PolicyVerified: true, Boundary: json.RawMessage(boundary)}, nil)
	h2.fx.SetAgent("p9", herdrtest.Agent("p9", "stranger"))
	h2.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" || ar.Reasons[0] != "boundary-inventory" {
		t.Fatalf("unknown agent: %+v", ar)
	}
	h2.fx.SetAgent("p9", nil)
	h2.do("GET", "/v1/routes/r/authority", nil, &ar)
	if ar.Authority != "HUMAN" || !exists(filepath.Join(dir2, "state", "boundary-tainted")) {
		t.Fatalf("RELAY came back after the unknown agent left: %+v", ar)
	}
}
