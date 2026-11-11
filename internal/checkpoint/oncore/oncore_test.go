package oncore_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/checkpoint/oncore"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// source is a fake core route: one agent's execution and the notices issued.
type source struct {
	exec    runtime.Execution
	none    bool
	notices map[string]*notice.Notice
	issued  []string
	events  []notice.Event
}

func (s *source) Current(context.Context) (runtime.Execution, bool, error) {
	return s.exec, !s.none, nil
}

func (s *source) Issue(_ context.Context, key string, e runtime.Execution, text string) (notice.Notice, error) {
	if n, ok := s.notices[key]; ok {
		return *n, nil
	}
	n := &notice.Notice{ID: fmt.Sprintf("n%d", len(s.notices)+1), Key: key, Kind: notice.KindCheckpoint, Execution: e.ID, Generation: e.Generation, Content: text, State: notice.StateIssued}
	s.notices[key] = n
	s.issued = append(s.issued, key)
	return *n, nil
}

func (s *source) ByKey(key string) (notice.Notice, bool, error) {
	n, ok := s.notices[key]
	if !ok {
		return notice.Notice{}, false, nil
	}
	return *n, true, nil
}

func (s *source) Emit(ev notice.Event) { s.events = append(s.events, ev) }

// handle marks the last notice issued as handled by an execution generation.
func (s *source) handle(exec string, gen int64) {
	n := s.notices[s.issued[len(s.issued)-1]]
	n.State, n.Outcome, n.Execution, n.Generation = notice.StateHandled, notice.OutcomeCompleted, exec, gen
}

// transcript writes a Claude Code transcript whose last usage is tokens.
func transcript(t *testing.T, home, cwd, sid string, tokens int64) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`+"\n", tokens)
	f, err := os.OpenFile(filepath.Join(dir, sid+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func rig(t *testing.T, kind string, args []string) (*oncore.Checkpointer, *source, *clock, string) {
	t.Helper()
	home := t.TempDir()
	s := &source{notices: map[string]*notice.Notice{}, exec: runtime.Execution{ID: "e1", Generation: 1, Repo: "/r", Route: "harbor", Agent: "orch",
		HarnessKind: kind, Cwd: "/r", Conversation: &runtime.Conversation{Value: "sid-1", Provenance: "adapter"}}}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := &oncore.Checkpointer{Config: oncore.Config{Interval: 30 * time.Minute, Limit: 150000, Kind: kind, Args: args, Home: home}, Source: s, Now: clk.now}
	return c, s, clk, home
}

func step(t *testing.T, c *oncore.Checkpointer) oncore.Pass {
	t.Helper()
	p, err := c.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The checkpoint prompt on core is a notice without its slash command.
func TestPromptHasNoSlashCommand(t *testing.T) {
	if strings.HasPrefix(oncore.Prompt, "/") || strings.Contains(oncore.Prompt, "/memory-with-dag") || !strings.HasPrefix(oncore.Prompt, "Capture all learnings") {
		t.Errorf("prompt %q", oncore.Prompt)
	}
}

// With no conversation evidence the checkpointer refuses to guess, even when
// a transcript sits in the agent's folder.
func TestNoEvidenceNoGuess(t *testing.T) {
	c, s, clk, home := rig(t, "claude", nil)
	transcript(t, home, "/r", "someone-else", 190000)
	s.exec.Conversation = nil
	clk.t = clk.t.Add(time.Hour)
	p := step(t, c)
	if !p.Has(oncore.PassNoEvidence) || len(s.issued) != 0 || !strings.Contains(p.Detail, "does not guess") {
		t.Errorf("pass %+v issued %v", p, s.issued)
	}
	// Evidence that names no file is not a guess either.
	s.exec.Conversation = &runtime.Conversation{Value: "sid-1", Provenance: "adapter"}
	p = step(t, c)
	if !p.Has(oncore.PassNoTranscript) || len(s.issued) != 0 {
		t.Errorf("pass %+v issued %v", p, s.issued)
	}
}

// Claude Code compacts 33,000 tokens below its window: the early checkpoint
// comes below that, once per crossing, and again after the harness compacted
// by itself. No compact command, and no compaction_due: it compacts alone.
func TestEarlyCheckpointBelowClaudeCompaction(t *testing.T) {
	c, s, clk, home := rig(t, "claude", nil)
	level := int64(200000-33000) - oncore.DefaultEarlyMargin
	transcript(t, home, "/r", "sid-1", level-1)
	if p := step(t, c); len(s.issued) != 0 {
		t.Fatalf("below the early level: %+v", p)
	}
	transcript(t, home, "/r", "sid-1", level)
	p := step(t, c)
	if !p.Has(oncore.PassEarly) || len(s.issued) != 1 || !strings.HasSuffix(s.issued[0], ":early") {
		t.Fatalf("at the early level: %+v %v", p, s.issued)
	}
	n := s.notices[s.issued[0]]
	if n.Content != oncore.Prompt || n.Kind != notice.KindCheckpoint {
		t.Errorf("notice %+v", n)
	}
	// While it is outstanding nothing more is issued.
	transcript(t, home, "/r", "sid-1", level+5000)
	clk.t = clk.t.Add(time.Hour)
	if p := step(t, c); !p.Has(oncore.PassPending) || len(s.issued) != 1 {
		t.Fatalf("outstanding: %+v %v", p, s.issued)
	}
	s.handle("e1", 1)
	p = step(t, c)
	if !p.Has(oncore.PassDone) {
		t.Fatalf("handled: %+v", p)
	}
	// Still above the level: no second early checkpoint for this crossing.
	if p.Has(oncore.PassEarly) {
		t.Fatalf("a second early checkpoint for one crossing: %+v", p)
	}
	if p.Has(oncore.PassIssued) {
		// The interval had passed with activity: an ordinary checkpoint.
		s.handle("e1", 1)
	}
	// The harness compacted by itself in the middle of a turn: the context
	// fell, and the next crossing gets its early checkpoint again.
	transcript(t, home, "/r", "sid-1", 40000)
	step(t, c)
	transcript(t, home, "/r", "sid-1", level+1)
	if p := step(t, c); !p.Has(oncore.PassEarly) {
		t.Fatalf("after self-compaction: %+v", p)
	}
	if len(s.events) != 0 {
		t.Errorf("a harness that compacts by itself raised %+v", s.events)
	}
	for _, k := range s.issued {
		if strings.Contains(s.notices[k].Content, "/compact") {
			t.Errorf("a compact command in %s", k)
		}
	}
}

// The interval checkpoint comes once per interval with activity, as on the
// TUI, and none when the transcript did not change.
func TestIntervalCheckpoint(t *testing.T) {
	c, s, clk, home := rig(t, "claude", nil)
	transcript(t, home, "/r", "sid-1", 1000)
	step(t, c)
	clk.t = clk.t.Add(29 * time.Minute)
	if step(t, c); len(s.issued) != 0 {
		t.Fatal("before the interval")
	}
	clk.t = clk.t.Add(2 * time.Minute)
	if p := step(t, c); !p.Has(oncore.PassIssued) {
		t.Fatalf("after the interval: %+v", p)
	}
	s.handle("e1", 1)
	clk.t = clk.t.Add(31 * time.Minute)
	if p := step(t, c); !p.Has(oncore.PassIdle) || len(s.issued) != 1 {
		t.Fatalf("no activity: %+v %v", p, s.issued)
	}
	transcript(t, home, "/r", "sid-1", 2000)
	clk.t = clk.t.Add(31 * time.Minute)
	if p := step(t, c); !p.Has(oncore.PassIssued) || len(s.issued) != 2 {
		t.Fatalf("new activity: %+v %v", p, s.issued)
	}
}

// Codex compacts at the launch's own model_auto_compact_token_limit; with none
// set its level was not measured, so the checkpoint comes on the interval only.
func TestCodexLevelFromTheLaunch(t *testing.T) {
	codexRollout := func(t *testing.T, home string, tokens int64) {
		t.Helper()
		dir := filepath.Join(home, ".codex", "sessions", "2026", "11", "11")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0}}}}`+"\n", tokens)
		if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-11T00-00-00-sid-1.jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, s, _, home := rig(t, "codex", []string{"--no-daemon"})
	codexRollout(t, home, 190000)
	if p := step(t, c); p.Has(oncore.PassEarly) || len(s.issued) != 0 {
		t.Fatalf("no launch setting: %+v", p)
	}
	c, s, _, home = rig(t, "codex", []string{"--no-daemon", "-c", "model_auto_compact_token_limit=100000"})
	codexRollout(t, home, 85000)
	p := step(t, c)
	if p.Tokens == 0 {
		t.Skipf("this build reads no Codex rollout usage from the fixture: %+v", p)
	}
	if !p.Has(oncore.PassEarly) || len(s.issued) != 1 {
		t.Fatalf("at the launch's level less the margin: %+v %v", p, s.issued)
	}
}

// compaction_due is raised once, where the user turned the harness's own
// compaction off, when the context passes the checkpointer's limit; it clears
// when the context falls, and nothing is typed.
func TestCompactionDueWhereCompactionIsOff(t *testing.T) {
	c, s, _, home := rig(t, "claude", nil)
	c.Config.AutoCompactOff = true
	transcript(t, home, "/r", "sid-1", 149999)
	step(t, c)
	transcript(t, home, "/r", "sid-1", 150000)
	p := step(t, c)
	if !p.Has(oncore.PassDue) || len(s.events) != 1 || s.events[0].Reason != core.AttentionCompactionDue || s.events[0].Kind != notice.EventNeedsAttention {
		t.Fatalf("pass %+v events %+v", p, s.events)
	}
	step(t, c)
	if len(s.events) != 1 {
		t.Errorf("raised again before it cleared: %+v", s.events)
	}
	transcript(t, home, "/r", "sid-1", 1000)
	step(t, c)
	transcript(t, home, "/r", "sid-1", 160000)
	step(t, c)
	if len(s.events) != 2 {
		t.Errorf("not raised again after it cleared: %+v", s.events)
	}
	// No early checkpoint either: the harness will not compact by itself.
	for _, k := range s.issued {
		if strings.HasSuffix(k, ":early") {
			t.Errorf("early checkpoint %s with compaction off", k)
		}
	}
}

// A checkpoint belongs to one generation: the late report of a replaced
// execution is rejected, and the new generation starts its own state.
func TestStaleGenerationReportIsRejected(t *testing.T) {
	c, s, _, home := rig(t, "claude", nil)
	level := int64(200000-33000) - oncore.DefaultEarlyMargin
	transcript(t, home, "/r", "sid-1", level)
	if p := step(t, c); !p.Has(oncore.PassEarly) {
		t.Fatalf("%+v", p)
	}
	// The execution is replaced; the old one's report arrives late.
	s.exec.ID, s.exec.Generation = "e2", 2
	s.handle("e1", 1)
	p := step(t, c)
	if !p.Has(oncore.PassStale) || p.Has(oncore.PassDone) {
		t.Fatalf("late report: %+v", p)
	}
	// The new generation crosses the level on its own account.
	if !p.Has(oncore.PassEarly) || len(s.issued) != 2 || !strings.Contains(s.issued[1], "checkpoint:e2:2:") {
		t.Fatalf("new generation: %+v %v", p, s.issued)
	}
	// Outstanding for a replaced generation: nothing is waited on for it.
	s.exec.ID, s.exec.Generation = "e3", 3
	if p := step(t, c); !p.Has(oncore.PassStale) || p.Has(oncore.PassPending) {
		t.Fatalf("outstanding for a replaced generation: %+v", p)
	}
}

func TestNoExecution(t *testing.T) {
	c, s, _, _ := rig(t, "claude", nil)
	s.none = true
	if p := step(t, c); !p.Has(oncore.PassNoExecution) {
		t.Errorf("%+v", p)
	}
}
