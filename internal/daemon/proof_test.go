package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// proofRoute is a direct route with a fresh policy file and a setup
// request over it.
func proofRoute(t *testing.T) (*direct, string, SetupRequest) {
	t.Helper()
	x := newDirect(t)
	rule := writeFile(t, t.TempDir(), "policy.sb", "(allow default)\n")
	q := SetupRequest{Rules: []string{rule}, PolicyVerified: true, Boundary: json.RawMessage(`{"version":1}`)}
	if _, e := x.r.setup(q); e != nil {
		t.Fatalf("setup: %v", e)
	}
	if !x.r.proofValid() {
		t.Fatal("a fresh proof is not valid")
	}
	return x, rule, q
}

// TestProofSymlinkReplacement: a rule file replaced by a link to a file
// with the same bytes is no longer the proof's file (7.3).
func TestProofSymlinkReplacement(t *testing.T) {
	x, rule, _ := proofRoute(t)
	same := writeFile(t, t.TempDir(), "same.sb", "(allow default)\n")
	if err := os.Remove(rule); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(same, rule); err != nil {
		t.Fatal(err)
	}
	if x.r.proofValid() {
		t.Fatal("a rule replaced by a link still proves the route")
	}
	// Setup refuses a link too.
	if _, e := x.r.setup(SetupRequest{Rules: []string{rule}, PolicyVerified: true, Boundary: json.RawMessage(`{}`)}); e == nil || e.Code != "bad_request" {
		t.Fatalf("setup over a link: %v", e)
	}
}

// TestProofRestoredPolicyStaysStale: once a proof is seen stale the route
// stays held after the file is put back, until the human's next setup.
func TestProofRestoredPolicyStaysStale(t *testing.T) {
	x, rule, q := proofRoute(t)
	if err := os.WriteFile(rule, []byte("(deny default)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if x.r.proofValid() {
		t.Fatal("a changed rule still proves the route")
	}
	if err := os.WriteFile(rule, []byte("(allow default)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if x.r.proofValid() {
		t.Fatal("the restored rule cleared the stale proof without a setup")
	}
	x.kill()
	x.open()
	if x.r.proofValid() {
		t.Fatal("a restart cleared the stale latch")
	}
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	if !x.r.proofValid() {
		t.Fatal("a new setup did not clear the latch")
	}
}

// TestProofInstalledBinary: the proof's binary is the installed
// executable; another file is refused at setup, a link to the executable
// is accepted and recorded as the executable, and a proof naming another
// binary is stale (7.3).
func TestProofInstalledBinary(t *testing.T) {
	x, rule, q := proofRoute(t)
	other := writeFile(t, t.TempDir(), "other-bin", "z")
	q.Binary = other
	if _, e := x.r.setup(q); e == nil || e.Code != "bad_request" {
		t.Fatalf("setup with another binary: %v", e)
	}
	link := filepath.Join(t.TempDir(), "herdsman")
	if err := os.Symlink(x.d.cfg.Executable, link); err != nil {
		t.Fatal(err)
	}
	q.Binary = link
	if _, e := x.r.setup(q); e != nil {
		t.Fatalf("setup with a link to the executable: %v", e)
	}
	doc, _ := x.r.readSetup()
	if doc.Binary.Path != canon(x.d.cfg.Executable) {
		t.Fatalf("binary recorded as %q", doc.Binary.Path)
	}
	// A proof naming another binary with its true checksum is stale.
	doc.Binary = FileSum{Path: other, SHA256: sha256Hex([]byte("z"))}
	_ = os.WriteFile(x.path("setup.json"), prettyJSON(doc), 0o600)
	if x.r.proofValid() {
		t.Fatal("a proof of another binary proves the route")
	}
	_ = rule
}

// TestSetupRejectsWrongBoundaryType: setup validates the whole version 2
// schema before writing anything (7.3).
func TestSetupRejectsWrongBoundaryType(t *testing.T) {
	x, rule, _ := proofRoute(t)
	before, _ := os.ReadFile(x.path("setup.json"))
	id := `{"pane":"p1","session":{"agent":"claude","kind":"id","source":"hook","value":"s1"}}`
	bad := []SetupRequest{
		{Boundary: json.RawMessage(`[]`)},
		{Boundary: json.RawMessage(`"x"`)},
		{Boundary: json.RawMessage(`{}`), WatchTargets: json.RawMessage(`{}`)},
		{Boundary: json.RawMessage(`{}`), WatchTargets: json.RawMessage(`[{"pane":"p1"}]`)},
		{Boundary: json.RawMessage(`{}`), WatchTargets: json.RawMessage(`[` + id + `,"x"]`)},
		{Boundary: json.RawMessage(`{}`), ResumeTargets: json.RawMessage(`[` + id + `]`)},
		{Boundary: json.RawMessage(`{}`), ResumeTargets: json.RawMessage(`{"Bad Name":` + id + `}`)},
		{Boundary: json.RawMessage(`{}`), Probes: []Probe{{Identity: json.RawMessage(id), RecordedBy: "agent"}}},
		{Boundary: json.RawMessage(`{}`), Probes: []Probe{{Identity: json.RawMessage(`{"pane":"p1"}`), RecordedBy: "human"}}},
	}
	for i, q := range bad {
		q.Rules, q.PolicyVerified = []string{rule}, true
		if _, e := x.r.setup(q); e == nil || e.Code != "bad_request" {
			t.Errorf("%d: accepted: %v", i, e)
		}
	}
	if now, _ := os.ReadFile(x.path("setup.json")); string(now) != string(before) {
		t.Fatal("a refused setup changed the proof")
	}
	good := SetupRequest{Rules: []string{rule}, PolicyVerified: true, Boundary: json.RawMessage(`{}`),
		WatchTargets: json.RawMessage(`[` + id + `]`), ResumeTargets: json.RawMessage(`{"impl-1":` + id + `}`),
		Probes: []Probe{{Identity: json.RawMessage(id), InboxWriteDenied: true, RecordedBy: "human"}}}
	if _, e := x.r.setup(good); e != nil {
		t.Fatalf("a full setup: %v", e)
	}
}

// TestProofTargetDrift: the target files are part of the proof: a change
// to one, or one that appears without the proof naming it, makes the
// proof stale; a setup without targets removes the old files.
func TestProofTargetDrift(t *testing.T) {
	x, rule, _ := proofRoute(t)
	id := `{"pane":"p1","session":{"agent":"claude","kind":"id","source":"hook","value":"s1"}}`
	q := SetupRequest{Rules: []string{rule}, PolicyVerified: true, Boundary: json.RawMessage(`{}`), WatchTargets: json.RawMessage(`[` + id + `]`)}
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	if !x.r.proofValid() {
		t.Fatal("the proof with targets is not valid")
	}
	_ = os.WriteFile(x.path("watch-targets.json"), []byte(`[]`+"\n"), 0o600)
	if x.r.proofValid() {
		t.Fatal("changed watch targets still prove the route")
	}
	q.WatchTargets = nil
	if _, e := x.r.setup(q); e != nil {
		t.Fatal(e)
	}
	if exists(x.path("watch-targets.json")) || !x.r.proofValid() {
		t.Fatal("a setup without targets kept the old file")
	}
	_ = os.WriteFile(x.path("resume-targets.json"), []byte(`{}`+"\n"), 0o600)
	if x.r.proofValid() {
		t.Fatal("a target file the proof does not name still proves the route")
	}
}

// TestSetupRejectsFIFO: a named pipe is never opened for hashing, so a
// setup over one returns at once; the hash cache rereads only a changed
// file.
func TestSetupRejectsFIFO(t *testing.T) {
	x, rule, _ := proofRoute(t)
	fifo := filepath.Join(t.TempDir(), "pipe.sb")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan *APIError, 1)
	go func() {
		_, e := x.r.setup(SetupRequest{Rules: []string{fifo}, PolicyVerified: true, Boundary: json.RawMessage(`{}`)})
		done <- e
	}()
	select {
	case e := <-done:
		if e == nil || e.Code != "bad_request" {
			t.Fatalf("setup over a FIFO: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setup blocked on a FIFO")
	}
	var c hashCache
	if _, err := c.sum(fifo); err == nil {
		t.Fatal("a FIFO was hashed")
	}
	if _, err := c.sum("relative"); err == nil {
		t.Fatal("a relative path was hashed")
	}
	a, _ := c.sum(rule)
	c.m[rule] = hashEntry{stamp: c.m[rule].stamp, sum: "cached"}
	if s, _ := c.sum(rule); s != "cached" {
		t.Fatal("an unchanged file was hashed again")
	}
	_ = os.WriteFile(rule, []byte("(deny default) ; longer\n"), 0o600)
	if s, _ := c.sum(rule); s == "cached" || s == a {
		t.Fatal("a changed file kept its old sum")
	}
	if !strings.HasPrefix(rule, "/") {
		t.Fatal(rule)
	}
}
