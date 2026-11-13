package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

const (
	sessA = "0b7f3c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f"
	sessB = "1c8a4d2f-6e3b-4d9f-8a21-3b4c5d6e7f80"
)

// claudeSession writes a Claude Code transcript fixture for cwd under
// projects, as Claude Code lays it out, written at when.
func claudeSession(t *testing.T, projects, cwd, id string, when time.Time, extra ...map[string]any) string {
	t.Helper()
	dir := filepath.Join(projects, checkpoint.Derived(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	// A first record that names no cwd, as a summary does.
	b.WriteString(`{"type":"summary","summary":"fixture"}` + "\n")
	rec := map[string]any{"type": "user", "cwd": cwd, "sessionId": id, "isSidechain": false}
	for _, e := range extra {
		for k, v := range e {
			rec[k] = v
		}
	}
	line, _ := json.Marshal(rec)
	b.Write(line)
	b.WriteString("\n")
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

// codexRollout writes a Codex rollout fixture under sessions, by date.
func codexRollout(t *testing.T, sessions, cwd, id string, when time.Time) string {
	t.Helper()
	dir := filepath.Join(sessions, when.Format("2006"), when.Format("01"), when.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"timestamp": when.UTC().Format(time.RFC3339), "type": "session_meta",
		"payload": map[string]any{"id": id, "cwd": cwd, "originator": "codex_cli_rs", "instructions": strings.Repeat("x", 100<<10)}})
	p := filepath.Join(dir, "rollout-"+when.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := os.WriteFile(p, append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFindConversationClaudeCode(t *testing.T) {
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")
	cwd := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	reg := time.Now().Add(-time.Minute)
	q := core.ConversationQuery{Kind: "claude", Cwd: cwd, Registered: reg}

	if got := core.FindConversation(roots, q); got.Status != core.ConversationAbsent {
		t.Fatalf("with no session: %+v", got)
	}
	// Written before registration: another conversation of the folder.
	claudeSession(t, roots.Claude, cwd, sessB, reg.Add(-time.Hour))
	if got := core.FindConversation(roots, q); got.Status != core.ConversationAbsent {
		t.Fatalf("an older session was taken: %+v", got)
	}
	claudeSession(t, roots.Claude, cwd, sessA, reg.Add(10*time.Second))
	got := core.FindConversation(roots, q)
	if got.Status != core.ConversationFound || got.Conversation.Value != sessA || got.Conversation.Provenance != core.ProvenanceClaudeProject {
		t.Fatalf("the session: %+v", got)
	}
	// A second session of the same folder since registration: ambiguous,
	// unless the other one is another execution's.
	claudeSession(t, roots.Claude, cwd, sessB, reg.Add(20*time.Second))
	if got := core.FindConversation(roots, q); got.Status != core.ConversationAmbiguous || got.Conversation.Value != "" {
		t.Fatalf("two sessions: %+v", got)
	}
	q.Taken = []string{sessB}
	if got := core.FindConversation(roots, q); got.Status != core.ConversationFound || got.Conversation.Value != sessA {
		t.Fatalf("with the other session taken: %+v", got)
	}
}

// Two agents of one folder that both started their sessions: the notice id
// the daemon issued to one execution, printed in its own transcript, settles
// which session is its own. Without one, the choice stays refused.
func TestFindConversationSettlesTwoSessionsByNoticeID(t *testing.T) {
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")
	cwd := filepath.Join(t.TempDir(), "repo")
	reg := time.Now().Add(-time.Minute)
	claudeSession(t, roots.Claude, cwd, sessA, reg.Add(time.Second), map[string]any{"message": "notice n-0123456789abcdef01234567 claimed"})
	claudeSession(t, roots.Claude, cwd, sessB, reg.Add(2*time.Second), map[string]any{"message": "notice n-fedcba9876543210fedcba98 claimed"})
	q := core.ConversationQuery{Kind: "claude", Cwd: cwd, Registered: reg}
	if got := core.FindConversation(roots, q); got.Status != core.ConversationAmbiguous {
		t.Fatalf("with no marks: %+v", got)
	}
	q.Marks = []string{"n-fedcba9876543210fedcba98"}
	got := core.FindConversation(roots, q)
	if got.Status != core.ConversationFound || got.Conversation.Value != sessB || got.Conversation.Provenance != core.ProvenanceClaudeProject+core.ProvenanceByNotice {
		t.Fatalf("settled by the notice: %+v", got)
	}
	// A mark both sessions hold settles nothing.
	q.Marks = []string{"n-0123456789abcdef01234567", "n-fedcba9876543210fedcba98"}
	if got := core.FindConversation(roots, q); got.Status != core.ConversationAmbiguous {
		t.Fatalf("with a mark in each: %+v", got)
	}
}

func TestFindConversationClaudeCodeChecksTheRecordedFolder(t *testing.T) {
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")
	base := t.TempDir()
	cwd := filepath.Join(base, "a.b")
	// "a-b" derives to the same folder name; its record names another cwd.
	other := filepath.Join(base, "a-b")
	reg := time.Now().Add(-time.Minute)
	claudeSession(t, roots.Claude, other, sessA, reg.Add(time.Second))
	if checkpoint.Derived(cwd) != checkpoint.Derived(other) {
		t.Fatal("the fixture's folders should derive alike")
	}
	if got := core.FindConversation(roots, core.ConversationQuery{Kind: "claude", Cwd: cwd, Registered: reg}); got.Status != core.ConversationAbsent {
		t.Fatalf("a session of another folder was taken: %+v", got)
	}
	// A sidechain transcript is never the agent's conversation.
	claudeSession(t, roots.Claude, cwd, sessB, reg.Add(time.Second), map[string]any{"isSidechain": true})
	if got := core.FindConversation(roots, core.ConversationQuery{Kind: "claude", Cwd: cwd, Registered: reg}); got.Status != core.ConversationAbsent {
		t.Fatalf("a sidechain was taken: %+v", got)
	}
}

func TestFindConversationNeverFollowsALinkOutOfTheRoot(t *testing.T) {
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")
	cwd := filepath.Join(t.TempDir(), "repo")
	reg := time.Now().Add(-time.Minute)
	outside := claudeSession(t, filepath.Join(t.TempDir(), "elsewhere"), cwd, sessA, reg.Add(time.Second))
	dir := filepath.Join(roots.Claude, checkpoint.Derived(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, sessA+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := core.FindConversation(roots, core.ConversationQuery{Kind: "claude", Cwd: cwd, Registered: reg}); got.Status != core.ConversationAbsent {
		t.Fatalf("a link out of the projects folder was read: %+v", got)
	}
}

func TestFindConversationCodex(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex")
	roots := core.SessionRootsOf(home, codexHome)
	if roots.Codex != filepath.Join(codexHome, "sessions") {
		t.Fatalf("the Codex root: %s", roots.Codex)
	}
	cwd := filepath.Join(t.TempDir(), "wt")
	reg := time.Now().Add(-time.Minute)
	q := core.ConversationQuery{Kind: "codex", Cwd: cwd, Registered: reg}
	codexRollout(t, roots.Codex, filepath.Join(t.TempDir(), "other"), sessB, reg.Add(time.Second))
	codexRollout(t, roots.Codex, cwd, sessB[:35]+"1", reg.Add(-time.Hour))
	if got := core.FindConversation(roots, q); got.Status != core.ConversationAbsent {
		t.Fatalf("another folder's or an older rollout was taken: %+v", got)
	}
	codexRollout(t, roots.Codex, cwd, sessA, reg.Add(5*time.Second))
	got := core.FindConversation(roots, q)
	if got.Status != core.ConversationFound || got.Conversation.Value != sessA || got.Conversation.Provenance != core.ProvenanceCodexRollout {
		t.Fatalf("the rollout: %+v", got)
	}
	// The default Codex home is the login home's.
	if r := core.SessionRootsOf(home, ""); r.Codex != filepath.Join(home, ".codex", "sessions") {
		t.Fatalf("the default Codex root: %s", r.Codex)
	}
	if r := core.SessionRootsOf("", ""); r.Claude != "" || r.Codex != "" {
		t.Fatalf("no home reads nothing: %+v", r)
	}
}

func TestFindConversationRefusesAnUnqualifiedHarness(t *testing.T) {
	got := core.FindConversation(core.SessionRootsOf(t.TempDir(), ""), core.ConversationQuery{Kind: "aider", Cwd: t.TempDir(), Registered: time.Now()})
	if got.Status != core.ConversationAbsent {
		t.Fatalf("an unqualified harness: %+v", got)
	}
}

// RecordConversation records evidence once, and a later write of the record
// without it does not clear it.
func TestRecordConversationKeepsWhatItRecorded(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")
	cwd := filepath.Join(t.TempDir(), "repo")
	recs := &core.MemRecords{}
	reg := time.Now().Add(-time.Minute)
	e := runtime.Execution{ID: runtime.NewExecutionID(), Repo: "r", Route: "omega", Agent: "omega-o", LaunchIntent: "i1", Backend: runtime.BackendCore,
		HarnessKind: "claude", Cwd: cwd, Desired: runtime.DesiredRunning, Evidence: runtime.ProcessEvidence{PID: 42, Start: "s"},
		LaunchGuard: "registered " + reg.UTC().Format(time.RFC3339Nano), Observation: runtime.Observation{Process: runtime.ProcessLive}}
	if _, err := recs.Put(ctx, e); err != nil {
		t.Fatal(err)
	}
	p := &core.Provider{Records: recs, Sessions: &roots}
	got, l, err := p.RecordConversation(ctx, e.ID)
	if err != nil || l.Status != core.ConversationAbsent || got.Conversation != nil {
		t.Fatalf("before the session: %+v %+v %v", got.Conversation, l, err)
	}
	claudeSession(t, roots.Claude, cwd, sessA, reg.Add(time.Second))
	got, l, err = p.RecordConversation(ctx, e.ID)
	if err != nil || l.Status != core.ConversationFound || got.Conversation == nil || got.Conversation.Value != sessA {
		t.Fatalf("after the session: %+v %+v %v", got.Conversation, l, err)
	}
	if _, err := recs.Put(ctx, e); err != nil { // a writer that never read it
		t.Fatal(err)
	}
	if again, _, _ := recs.ByIntent(ctx, "i1"); again.Conversation == nil || again.Conversation.Value != sessA {
		t.Fatalf("the evidence was cleared: %+v", again.Conversation)
	}
	// With no session folders, nothing is read or recorded.
	if _, l, err := (&core.Provider{Records: recs}).RecordConversation(ctx, e.ID); err != nil || l.Status != "" {
		t.Fatalf("without folders: %+v %v", l, err)
	}
}

// The reconciler records a live execution's conversation once the harness
// has written its session, and the ready step does when it already has.
func TestReconcilerRecordsConversationEvidence(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	ctx := testCtx(t)
	home := t.TempDir()
	roots := core.SessionRootsOf(home, "")

	l, _ := w.up("omega", "omega-o", "orchestrator", "claude")
	rec := w.ready(l)
	if rec.Conversation != nil {
		t.Fatalf("evidence with no session folders: %+v", rec.Conversation)
	}
	ev := &events{}
	r := w.reconciler(ev)
	r.Sessions = &roots
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.record(l.Intent); got.Conversation != nil {
		t.Fatalf("evidence before the harness wrote a session: %+v", got.Conversation)
	}
	claudeSession(t, roots.Claude, rec.Cwd, sessA, time.Now().Add(time.Second))
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	got := w.record(l.Intent)
	if got.Conversation == nil || got.Conversation.Value != sessA || got.Conversation.Provenance != core.ProvenanceClaudeProject {
		t.Fatalf("the recorded evidence: %+v", got.Conversation)
	}

	// A second agent in the same folder: its own session is the one not
	// taken by the first.
	w.p.Sessions = &roots
	l2, _ := w.up("omega", "omega-a", "adjutant", "claude")
	claudeSession(t, roots.Claude, w.record(l2.Intent).Cwd, sessB, time.Now().Add(2*time.Second))
	rec2 := w.ready(l2)
	if rec2.Cwd != rec.Cwd {
		t.Fatalf("the fixture's agents should share a folder: %s, %s", rec2.Cwd, rec.Cwd)
	}
	if rec2.Conversation == nil || rec2.Conversation.Value != sessB {
		t.Fatalf("the second agent's evidence: %+v", rec2.Conversation)
	}
}
