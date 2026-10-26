package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Review a-5 reproductions, kept as fail-first tests.

func TestCheckpointFsyncFailureNoSeqReuse(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("before", key()); e != nil {
		t.Fatal(e)
	}
	before := x.r.seq
	old := fsync
	fsync = func(f *os.File) error {
		if filepath.Base(f.Name()) == journalCurrent {
			return errInjected
		}
		return old(f)
	}
	x.r.j.size = journalRotate // force the actor's normal rotation branch
	e := x.op(func() *APIError { return nil })
	fsync = old
	if e == nil {
		t.Fatal("fault did not fire")
	}
	var after SendResult
	if e := x.op(func() *APIError { var e *APIError; after, e = x.send("after", key()); return e }); e != nil {
		t.Logf("safely refused next send: %v", e)
		return
	}
	t.Logf("checkpoint append failed at next seq=%d; subsequent send succeeded at seq=%d", before+1, after.Seq)
	x.kill()
	r := newRoute(x.d, "r", x.dir)
	defer r.closeFiles()
	if e := r.open(); e != nil {
		t.Fatalf("successful send left unreadable journal after checkpoint fsync failure: %v", e)
	}
}

func TestClaimRetryResyncsMove(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("one", key()); e != nil {
		t.Fatal(e)
	}
	old := fsync
	defer func() { fsync = old }()
	failed := false
	synced := false
	fsync = func(f *os.File) error {
		if f.Name() == x.path("orch", "cur") {
			if !failed {
				failed = true
				return errInjected
			}
			synced = true
		}
		return old(f)
	}
	_, e := x.r.claim("orch", ClaimRequest{ReadKey: key()})
	if e == nil || e.Code != "busy" || !failed {
		t.Fatalf("expected failed destination-directory fsync: %v", e)
	}
	if e := x.op(func() *APIError { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := x.r.writeCheckpoint(true); e != nil {
		t.Fatal(e)
	}
	if !synced {
		t.Fatal("claim retry and checkpoint succeeded without ever fsyncing the renamed cur entry")
	}
}

func TestUnfinishedHoldWaitsForInputLock(t *testing.T) {
	x := newDirect(t)
	unlock, e := x.r.inputLock()
	if e != nil {
		t.Fatal(e)
	}
	reason := "pause"
	restore := failWhere("/state/hold-orch")
	_, e = x.r.hold("adj", "orch", &reason)
	restore()
	unlock()
	if e == nil || e.Code != "busy" || len(x.r.unfinished) != 1 {
		t.Fatalf("expected committed unfinished hold: %v", e)
	}
	if exists(x.path("state", "hold-orch")) {
		t.Fatal("fault left a visible hold")
	}
	// The external executor/gate (another process) takes this exact lock
	// while consuming approval. A status turn must not mutate the
	// protected files under it.
	ext := x.path(inbox.MutationLock)
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ext, "pid"), []byte("1\n"), 0o600)
	e = x.op(func() *APIError { return nil })
	if exists(x.path("state", "hold-orch")) {
		t.Fatalf("unrelated actor turn wrote a hold while another input-lock owner held the boundary; answer=%v", e)
	}
}

func TestLostEpochChangeResumes(t *testing.T) {
	x := newDirect(t)
	if _, e := x.send("one", key()); e != nil {
		t.Fatal(e)
	}
	x.kill()
	_ = os.Remove(x.path("state", "events", journalCurrent))
	_ = os.Remove(x.path("state", "events", journalPrev))
	old := rename
	rename = func(a, b string) error {
		if strings.HasSuffix(b, filepath.Join("courier.lock", "owner.json")) {
			return errors.New("injected lock epoch write failure")
		}
		return old(a, b)
	}
	r := newRoute(x.d, "r", x.dir)
	e := r.open()
	r.closeFiles()
	rename = old
	if e == nil {
		t.Fatal("epoch fault did not fire")
	}
	r2 := newRoute(x.d, "r", x.dir)
	defer r2.closeFiles()
	if e := r2.open(); e != nil {
		t.Fatalf("recovery of lost-journal epoch transaction cannot reopen its own retained locks: %v", e)
	}
}

func TestUncertainApprovalKeepsSecrets(t *testing.T) {
	x := newPairRoute(t)
	approvePair(t, x, "a1", "o1")
	replacePair(t, x, "o1", "o2")
	pb, _ := os.ReadFile(x.path("peers"))
	restore := failWhere(journalCurrent)
	_, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb)})
	restore()
	if e == nil || !x.r.degraded.Load() {
		t.Fatalf("expected unknown-tail refusal: %v", e)
	}
	staged, _ := filepath.Glob(x.path("state", "credentials", "staged-*"))
	t.Logf("unknown-tail refusal: staged generations=%d", len(staged))
	x.kill()
	x.open()
	for n, c := range x.r.creds {
		if c.Option == "file" && !secretMatches(x.path("state", "credentials", n), c.SHA256) {
			t.Errorf("restart accepted credential hash for %s but its committed secret was discarded", n)
		}
	}
}

func TestRetainedProofTargetDrift(t *testing.T) {
	x, _, q := proofRoute(t)
	id := `{"pane":"p1","session":{"agent":"claude","kind":"id","source":"hook","value":"s1"}}`
	q.WatchTargets = json.RawMessage(`[` + id + `]`)
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	if !inbox.ProofV2Valid(x.dir) {
		t.Fatal("fresh retained proof refused")
	}
	_ = os.WriteFile(x.path("watch-targets.json"), []byte("[]\n"), 0600)
	// Check retained reader before daemon detection can create the latch.
	if inbox.ProofV2Valid(x.dir) {
		t.Error("retained approval proof accepts changed targets")
	}
	if x.r.proofValid() {
		t.Error("daemon proof also accepts changed targets")
	}
}

func TestHashCacheBound(t *testing.T) {
	x, _, q := proofRoute(t)
	base := t.TempDir()
	for i := 0; i < 100; i++ {
		p := filepath.Join(base, fmt.Sprintf("rule-%d", i))
		_ = os.WriteFile(p, []byte("ok"), 0600)
		q.Rules = []string{p}
		if _, e := x.r.setup(q); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("one-rule proof after 1000 replacements retains %d file hashes", len(x.r.hashes.m))
	if len(x.r.hashes.m) > 16 {
		t.Fatal("hash cache grows with every obsolete setup policy")
	}
}

func TestKeychainLoadFailureRefused(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain option offered only on macOS")
	}
	x := newPairRoute(t)
	enrollPair(t, x, "a1", "o1")
	kc := &failingKeychain{m: map[string]string{}, failStagedLoad: true}
	x.d.cfg.Keychain = kc
	pb, _ := os.ReadFile(x.path("peers"))
	_, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb), Credentials: map[string]string{"a1": "keychain", "o1": "keychain"}})
	if e != nil {
		t.Logf("safely refused unavailable keychain: %v", e)
		return
	}
	for n, c := range x.r.creds {
		s, e := kc.Load("herdsman/r", n)
		if e != nil || sha256Hex([]byte(s)) != c.SHA256 {
			t.Errorf("approval succeeded without an active keychain secret for %s", n)
		}
	}
}

func TestKeychainReplayPreservesLatest(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain option offered only on macOS")
	}
	x := newPairRoute(t)
	kc := &failingKeychain{m: map[string]string{}, failStagedDeleteOnce: true}
	x.d.cfg.Keychain = kc
	enrollPair(t, x, "a1", "o1")
	approve := func(orch string) {
		pb, _ := os.ReadFile(x.path("peers"))
		if _, e := x.r.approve(ApproveRequest{SHA256: sha256Hex(pb), Credentials: map[string]string{"a1": "keychain", orch: "keychain"}}); e != nil {
			t.Fatal(e)
		}
	}
	approve("o1")
	replacePair(t, x, "o1", "o2")
	approve("o2")
	for n, c := range x.r.creds {
		s, e := kc.Load("herdsman/r", n)
		if e != nil || sha256Hex([]byte(s)) != c.SHA256 {
			t.Fatalf("invalid fixture before replay: %s", n)
		}
	}
	x.kill()
	x.open()
	for n, c := range x.r.creds {
		s, e := kc.Load("herdsman/r", n)
		if e != nil || sha256Hex([]byte(s)) != c.SHA256 {
			t.Errorf("replay replaced latest active keychain secret for %s with an old generation", n)
		}
	}
}

type failingKeychain struct {
	m                    map[string]string
	failStagedLoad       bool
	failStagedDeleteOnce bool
}

func (k *failingKeychain) Store(service, account, secret string) error {
	k.m[service+"/"+account] = secret
	return nil
}
func (k *failingKeychain) Load(service, account string) (string, error) {
	if k.failStagedLoad && strings.Contains(account, ".staged.") {
		return "", errors.New("keychain access unavailable")
	}
	s, ok := k.m[service+"/"+account]
	if !ok {
		return "", os.ErrNotExist
	}
	return s, nil
}
func (k *failingKeychain) Delete(service, account string) error {
	if k.failStagedDeleteOnce && strings.Contains(account, ".staged.") {
		k.failStagedDeleteOnce = false
		return errors.New("keychain delete unavailable")
	}
	delete(k.m, service+"/"+account)
	return nil
}
