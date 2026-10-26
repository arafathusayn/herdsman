package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review a-7 (credential P0 closure) reproductions, kept as tests.

// F2 (uncertain tail, full record): the approval record is written but its
// fsync and the recovery fsync fail, so the tail is unknown and the route
// degrades. approve then drops the staged secrets. On restart replay finds
// the complete record; activate treats a missing staged file as "already
// active" without checking the active file, and pruning deletes the old
// secrets: the accepted hashes have no secret.
func TestA7UncertainApprovalFullRecord(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	replacePair(t, x, "o1", "o2")
	pb, _ := os.ReadFile(x.path("peers"))
	before := x.count("pair")
	old := fsync
	fsync = func(f *os.File) error {
		if filepath.Base(f.Name()) == journalCurrent {
			return errInjected
		}
		return old(f)
	}
	_, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)})
	fsync = old
	if e == nil || !x.r.degraded.Load() {
		t.Fatalf("fault not injected: %v degraded=%v", e, x.r.degraded.Load())
	}
	staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*"))
	t.Logf("after the refusal: pair records %d -> %d, staged generations %d, answer %v", before, x.count("pair"), len(staged), e)
	x.kill()
	x.open()
	for n, c := range x.r.creds {
		if c.Option == "file" && !secretMatches(x.path("state", "credentials", n), c.SHA256) {
			t.Errorf("restart accepts a credential hash for %s with no matching secret (party locked out)", n)
		}
	}
}

// approveAsServer is the socket handler's approval, with the asker rule.
func approveAsServer(x *direct, q ApproveRequest) (ApproveResult, *APIError) {
	return x.r.approveAsked(q, true)
}

// asked_by vs. F5: the retry of a committed first pairing approved through
// the orchestrator's ask (allowed: no adjutant yet) must get its recorded
// result; the asker rule now sees an approved adjutant and refuses it.
func TestA7AskedByFirstPairingRetry(t *testing.T) {
	x := newPairRoute(t)
	enrollPair(t, x, "a1", "o1")
	pb, _ := os.ReadFile(x.path("peers"))
	q := ApproveRequest{SHA256: sha256Hex(pb), Interaction: "ask-1", AskedBy: "o1"}
	if _, e := approveAsServer(x, q); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.approve(q); e != nil {
		t.Fatalf("control: approve's own retry path refused: %v", e)
	}
	if _, e := approveAsServer(x, q); e != nil {
		t.Errorf("retry of the committed approval (lost response) refused by the asker rule: %v", e)
	}
}

// asked_by vs. F5 after an adjutant change: approved a1 asks for the pair
// a2+o1; once committed, the retry finds a2 approved and is refused.
func TestA7AskedByAdjutantChangeRetry(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	pb, _ := os.ReadFile(x.path("peers"))
	if _, e := x.r.retire("a1", sha256Hex(pb)); e != nil {
		t.Fatal(e)
	}
	enrollOne(t, x, "a2", "adjutant")
	pb, _ = os.ReadFile(x.path("peers"))
	q := ApproveRequest{SHA256: sha256Hex(pb), Interaction: "ask-2", AskedBy: "a1"}
	if _, e := approveAsServer(x, q); e != nil {
		t.Fatal(e)
	}
	if _, e := approveAsServer(x, q); e != nil {
		t.Errorf("retry of the committed approval refused by the asker rule: %v", e)
	}
}

func enrollOne(t *testing.T, x *direct, name, role string) {
	t.Helper()
	id := inbox.Identity{Pane: "p-" + name, Session: inbox.Session{Agent: "claude", Kind: "id", Source: "hook", Value: "s-" + name}}
	if _, e := x.r.enroll(name, role, id); e != nil {
		t.Fatalf("enroll %s: %v", name, e)
	}
}

// asked_by fails open: an unreadable approved registry has no adjutant,
// so the orchestrator's ask passes the rule.
func TestA7AskedByFailsOpen(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	replacePair(t, x, "o1", "o2")
	pb, _ := os.ReadFile(x.path("peers"))
	_ = os.WriteFile(x.path("peers.approved"), []byte("{\"a1\":"), 0o600)
	if e := x.r.askedByAdjutant(ApproveRequest{SHA256: sha256Hex(pb), Interaction: "ask-3", AskedBy: "o1"}); e == nil {
		t.Error("an unreadable approved registry lets the orchestrator's own ask through")
	}
}

// F3 across options without repeating one approval (F5 consumes it):
// file, then identity, then file, then restart.
func TestA7ReplayAcrossOptions(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	replacePair(t, x, "o1", "o2")
	pb, _ := os.ReadFile(x.path("peers"))
	if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb), Credentials: map[string]string{"a1": "identity", "o2": "identity"}}); e != nil {
		t.Fatal(e)
	}
	replacePair(t, x, "o2", "o3")
	pb, _ = os.ReadFile(x.path("peers"))
	if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)}); e != nil {
		t.Fatal(e)
	}
	before := x.secrets()
	x.kill()
	x.open()
	for n, c := range x.r.creds {
		if c.Option == "file" && !secretMatches(x.path("state", "credentials", n), c.SHA256) {
			t.Errorf("replay lost %s's final secret", n)
		}
	}
	if fmt.Sprint(before) != fmt.Sprint(x.secrets()) {
		t.Error("replay changed the active secrets")
	}
}

// F2 adapted to F5 (a repeated approval of one registry is now refused):
// a journal failure before the commit leaves the active secrets and hashes.
func TestA7ApproveFailureAtomic(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	replacePair(t, x, "o1", "o2")
	before, creds := fmt.Sprint(x.secrets()), fmt.Sprint(x.r.creds)
	_ = x.r.j.f.Close()
	pb, _ := os.ReadFile(x.path("peers"))
	if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)}); e == nil {
		t.Fatal("expected the journal error")
	}
	staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*"))
	if before != fmt.Sprint(x.secrets()) || creds != fmt.Sprint(x.r.creds) || len(staged) != 0 {
		t.Errorf("a refused approval changed the active secrets or hashes, or left %d staged", len(staged))
	}
}

// N3 residual: replay at start redoes a committed hold whose file was not
// written, without the shared input lock, while another owner holds it.
func TestA7ReplayHoldWithoutInputLock(t *testing.T) {
	x := newDirect(t)
	reason := "pause"
	unlock, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	restore := failWhere("/state/hold-orch")
	_, e = x.r.hold("adj", "orch", &reason)
	restore()
	unlock()
	if e == nil || len(x.r.unfinished) != 1 {
		t.Fatalf("expected an unfinished hold: %v", e)
	}
	x.kill()
	ext := x.path(inbox.MutationLock)
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ext, "pid"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	x.open()
	if exists(x.path("state", "hold-orch")) {
		t.Error("replay at start wrote a protected hold while another owner held the input lock")
	}
}

// TestActivateNeedsProof: a generation whose staged secret is gone is
// taken as active only when the active secret is the record's.
func TestActivateNeedsProof(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	if err := x.r.activate("gone", x.r.creds); err != nil {
		t.Fatalf("the active generation: %v", err)
	}
	other := map[string]credential{"a1": {Option: "file", SHA256: sha256Hex([]byte("another secret"))}}
	if err := x.r.activate("gone", other); err == nil {
		t.Fatal("a generation with no staged and no active secret was taken as active")
	}
}

// TestAskedByRefusesControlCharacters: asked_by goes into the audit line
// only as one clean line.
func TestAskedByRefusesControlCharacters(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	for _, by := range []string{"a1\nforged line", "a1\x1b[2K", strings.Repeat("a", 201)} {
		if e := x.r.askedByAdjutant(ApproveRequest{Interaction: "ask-1", AskedBy: by}); e == nil || e.Message != "invalid asked_by" {
			t.Errorf("%q: %v", by, e)
		}
	}
}
