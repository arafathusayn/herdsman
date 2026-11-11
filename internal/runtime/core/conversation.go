package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// The sources of native conversation evidence, one per adapter (spec section
// 9). The provenance recorded beside the value names the source.
const (
	// SessionsClaudeProject: Claude Code keeps one transcript per
	// conversation in its projects folder, in the folder named after the
	// working folder, as <session id>.jsonl; each record names its cwd.
	SessionsClaudeProject = "claude-project"
	// SessionsCodexRollout: Codex keeps one rollout per conversation under
	// its sessions folder by date; the first line is the session's meta
	// record with its id and cwd.
	SessionsCodexRollout = "codex-rollout"
)

// Provenances recorded with a conversation found in the harness's own
// session files. Neither ends in ":path": the value is an id.
const (
	ProvenanceClaudeProject = "claude-code:project-session-file"
	ProvenanceCodexRollout  = "codex:rollout-session-meta"
	// ProvenanceByNotice is added when a notice id of the execution settled
	// a choice between sessions of one folder.
	ProvenanceByNotice = "+notice-id"
)

// SessionRoots are the only folders conversation evidence is read from: the
// harnesses' own session folders. An empty root is never read.
type SessionRoots struct {
	// Claude is Claude Code's projects folder (<home>/.claude/projects).
	Claude string
	// Codex is Codex's sessions folder (<codex home>/sessions).
	Codex string
}

// SessionRootsOf is the session folders of a login environment's home and
// Codex home, as the checkpointer finds transcripts.
func SessionRootsOf(home, codexHome string) SessionRoots {
	var r SessionRoots
	if home != "" {
		r.Claude = filepath.Join(home, ".claude", "projects")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if codexHome != "" {
		r.Codex = filepath.Join(codexHome, "sessions")
	}
	return r
}

// ConversationStatus is what a lookup found.
type ConversationStatus string

const (
	ConversationFound     ConversationStatus = "found"
	ConversationAbsent    ConversationStatus = "absent"
	ConversationAmbiguous ConversationStatus = "ambiguous"
)

// ConversationLookup is the answer of FindConversation. Conversation is set
// only when exactly one session qualifies.
type ConversationLookup struct {
	Status       ConversationStatus
	Conversation runtime.Conversation
	Detail       string
}

// ConversationQuery names the execution a conversation is looked up for.
type ConversationQuery struct {
	Kind string
	Cwd  string
	// Registered is when the execution registered: a session file not
	// written since then is not its conversation.
	Registered time.Time
	// Taken are conversation ids other running executions already hold;
	// they are never another execution's.
	Taken []string
	// Marks are strings only this execution's conversation can hold: the
	// random ids of the notices issued to it, which its harness printed
	// when it read them. They settle a choice between sessions of one
	// folder; they never stand in for one.
	Marks []string
}

// candidate is a session that qualifies, with the file it was read from.
type candidate struct {
	conv runtime.Conversation
	file string
}

// Bounds of what a lookup reads from one session file.
const (
	sessionLineLimit     = 8 << 20 // a Codex meta line carries its instructions
	sessionClaudeRecords = 64      // Claude Code's first records may name no cwd
	sessionMarkScan      = 16 << 20
)

// FindConversation finds the native conversation of one execution in its
// harness's own session files, and refuses to guess: a session qualifies
// when its recorded cwd is the execution's and its file was written since
// registration, and it is recorded only when exactly one qualifies.
func FindConversation(roots SessionRoots, q ConversationQuery) ConversationLookup {
	a, err := AdapterFor(q.Kind)
	if err != nil {
		return ConversationLookup{Status: ConversationAbsent, Detail: err.Error()}
	}
	if q.Cwd == "" || q.Registered.IsZero() {
		return ConversationLookup{Status: ConversationAbsent, Detail: "the execution has no working folder or registration time"}
	}
	var found []candidate
	switch a.Sessions {
	case SessionsClaudeProject:
		found = claudeSessions(roots.Claude, q)
	case SessionsCodexRollout:
		found = codexSessions(roots.Codex, q)
	default:
		return ConversationLookup{Status: ConversationAbsent, Detail: a.Kind + " declares no session files"}
	}
	found = slices.DeleteFunc(found, func(c candidate) bool { return slices.Contains(q.Taken, c.conv.Value) })
	if len(found) > 1 && len(q.Marks) > 0 {
		var marked []candidate
		for _, c := range found {
			if holdsMark(c.file, q.Marks) {
				marked = append(marked, c)
			}
		}
		if len(marked) == 1 {
			c := marked[0].conv
			c.Provenance += ProvenanceByNotice
			return ConversationLookup{Status: ConversationFound, Conversation: c}
		}
	}
	switch len(found) {
	case 0:
		return ConversationLookup{Status: ConversationAbsent, Detail: "no " + a.Kind + " session of " + q.Cwd + " was written since the execution registered"}
	case 1:
		return ConversationLookup{Status: ConversationFound, Conversation: found[0].conv}
	}
	ids := make([]string, 0, len(found))
	for _, c := range found {
		ids = append(ids, c.conv.Value)
	}
	return ConversationLookup{Status: ConversationAmbiguous,
		Detail: "more than one " + a.Kind + " session of " + q.Cwd + " was written since the execution registered (" + strings.Join(ids, ", ") + ") and none alone holds a notice of the execution; none is recorded"}
}

// holdsMark reports whether the first part of a session file holds one of
// the marks.
func holdsMark(file string, marks []string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, sessionMarkScan))
	if err != nil {
		return false
	}
	for _, m := range marks {
		if len(m) >= 8 && bytes.Contains(b, []byte(m)) {
			return true
		}
	}
	return false
}

// sameFolder compares a recorded cwd with the execution's, through links.
func sameFolder(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// sessionFile reports a regular file under root (never through a link)
// written at or after since.
func sessionFile(root, p string, since time.Time) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular() && !st.ModTime().Before(since)
}

// claudeSessions reads the top level of the projects folder named after the
// working folder (and after its resolved form), never anything else.
func claudeSessions(root string, q ConversationQuery) []candidate {
	if root == "" {
		return nil
	}
	dirs := []string{claudeFolder(q.Cwd)}
	if real, err := filepath.EvalSymlinks(q.Cwd); err == nil && claudeFolder(real) != dirs[0] {
		dirs = append(dirs, claudeFolder(real))
	}
	var out []candidate
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(root, d, "*.jsonl"))
		for _, f := range files {
			if !sessionFile(root, f, q.Registered) {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			if !uuidLike(id) {
				continue
			}
			cwd, sid := claudeRecord(f)
			if sid != id || !sameFolder(cwd, q.Cwd) {
				continue
			}
			if !slices.ContainsFunc(out, func(c candidate) bool { return c.conv.Value == id }) {
				out = append(out, candidate{runtime.Conversation{Value: id, Provenance: ProvenanceClaudeProject}, f})
			}
		}
	}
	return out
}

// claudeRecord is the cwd and session id of a Claude Code transcript's first
// record that names both.
func claudeRecord(p string) (cwd, sid string) {
	f, err := os.Open(p)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, sessionLineLimit))
	sc.Buffer(make([]byte, 0, 64<<10), sessionLineLimit)
	for i := 0; i < sessionClaudeRecords && sc.Scan(); i++ {
		var rec struct {
			Cwd         string `json:"cwd"`
			SessionID   string `json:"sessionId"`
			IsSidechain bool   `json:"isSidechain"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Cwd == "" || rec.SessionID == "" {
			continue
		}
		if rec.IsSidechain {
			return "", ""
		}
		return rec.Cwd, rec.SessionID
	}
	return "", ""
}

// codexSessions reads the rollouts under the sessions folder by date
// (<year>/<month>/<day>/rollout-*.jsonl) written since registration.
func codexSessions(root string, q ConversationQuery) []candidate {
	if root == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	var out []candidate
	for _, f := range files {
		if !sessionFile(root, f, q.Registered) {
			continue
		}
		id, cwd := codexMeta(f)
		if id == "" || !strings.HasSuffix(strings.TrimSuffix(filepath.Base(f), ".jsonl"), "-"+id) || !sameFolder(cwd, q.Cwd) {
			continue
		}
		if !slices.ContainsFunc(out, func(c candidate) bool { return c.conv.Value == id }) {
			out = append(out, candidate{runtime.Conversation{Value: id, Provenance: ProvenanceCodexRollout}, f})
		}
	}
	return out
}

// codexMeta is the session id and cwd of a rollout's meta record, its first
// line.
func codexMeta(p string) (id, cwd string) {
	f, err := os.Open(p)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, sessionLineLimit))
	sc.Buffer(make([]byte, 0, 64<<10), sessionLineLimit)
	if !sc.Scan() {
		return "", ""
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Type != "session_meta" || !uuidLike(rec.Payload.ID) {
		return "", ""
	}
	return rec.Payload.ID, rec.Payload.Cwd
}

// uuidLike is the 8-4-4-4-12 hex form both harnesses name sessions with.
func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

// claudeFolder is the folder name Claude Code gives a working folder in its
// projects folder: every character but an ASCII letter or digit becomes "-".
func claudeFolder(pwd string) string {
	var b strings.Builder
	for _, r := range pwd {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// registeredAt is when an execution registered, from its launch guard; the
// last verification when the guard names no time.
func registeredAt(e runtime.Execution) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, trimRegistered(e.LaunchGuard)); err == nil {
		return t
	}
	return e.LastVerifiedAt
}

// takenConversations are the conversation ids other running executions hold.
func takenConversations(all []runtime.Execution, self string) []string {
	var out []string
	for _, o := range all {
		if o.ID != self && o.Conversation != nil && o.Conversation.Value != "" && o.Desired == runtime.DesiredRunning {
			out = append(out, o.Conversation.Value)
		}
	}
	return out
}

// withConversation records the conversation of a live, registered execution
// that has none yet, when exactly one session qualifies. It reports whether
// the record changed and what the lookup found.
func withConversation(roots *SessionRoots, marks func(string) []string, all []runtime.Execution, e runtime.Execution) (runtime.Execution, ConversationLookup, bool) {
	if roots == nil || e.Conversation != nil || !registered(e) || e.Observation.Process != runtime.ProcessLive {
		return e, ConversationLookup{}, false
	}
	q := ConversationQuery{Kind: e.HarnessKind, Cwd: e.Cwd, Registered: registeredAt(e), Taken: takenConversations(all, e.ID)}
	if marks != nil {
		q.Marks = marks(e.ID)
	}
	l := FindConversation(*roots, q)
	if l.Status != ConversationFound {
		return e, l, false
	}
	conv := l.Conversation
	e.Conversation = &conv
	return e, l, true
}

// RecordConversation looks up and records the native conversation of one
// execution, once: a recorded conversation is never replaced by a lookup.
func (p *Provider) RecordConversation(ctx context.Context, executionID string) (runtime.Execution, ConversationLookup, error) {
	all, err := p.Records.List(ctx, "", "")
	if err != nil {
		return runtime.Execution{}, ConversationLookup{}, err
	}
	for _, e := range all {
		if e.ID != executionID {
			continue
		}
		next, l, changed := withConversation(p.Sessions, p.noticeMarks, all, e)
		if !changed {
			return e, l, nil
		}
		got, err := p.Records.Put(ctx, next)
		return got, l, err
	}
	return runtime.Execution{}, ConversationLookup{}, refuse(CodeIdentityNotProven, "no execution %s", executionID)
}

// noticeMarks are the ids of the notices issued to an execution.
func (p *Provider) noticeMarks(execution string) []string {
	if p.Notices == nil {
		return nil
	}
	list, err := p.Notices.List(execution)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.ID)
	}
	return out
}
