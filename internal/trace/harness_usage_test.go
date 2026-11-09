package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

var usageStart = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

func usageEnd() time.Time { return usageStart.Add(time.Hour) }

func TestClaudeUsageCountsEachMessageOnceInsideTheTurn(t *testing.T) {
	home := t.TempDir()
	writeLog(t, filepath.Join(home, ".claude", "projects", "slug", "s1.jsonl"), `{"timestamp":"2026-01-01T09:00:00Z","message":{"id":"before","usage":{"input_tokens":100}}}
{"timestamp":"2026-01-01T10:00:01Z","message":{"id":"m","usage":{"input_tokens":1,"output_tokens":1}}}
{"timestamp":"2026-01-01T10:00:02Z","message":{"id":"m","usage":{"input_tokens":1,"output_tokens":5,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}}
not json
{"timestamp":"2026-01-01T10:00:03Z","costUSD":0.25,"message":{"id":"n","usage":{"output_tokens":4}}}
{"timestamp":"2026-01-01T12:00:00Z","message":{"id":"after","usage":{"input_tokens":100}}}`)
	tokens, cost := HarnessUsage(HarnessSession{Agent: "claude", Kind: "id", Value: "s1", Home: home}, usageStart, usageEnd())
	if tokens == nil || *tokens != 15 || cost == nil || *cost != 250000 {
		t.Fatalf("%v %v", tokens, cost)
	}
}

func TestCodexUsageAddsTheLastTurnCounts(t *testing.T) {
	home := t.TempDir()
	writeLog(t, filepath.Join(home, ".codex", "sessions", "2026", "01", "01", "rollout-x-s2.jsonl"), `{"timestamp":"2026-01-01T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":6}}}}
{"timestamp":"2026-01-01T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":null}}
{"timestamp":"2026-01-01T10:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":4}}}}
`)
	tokens, cost := HarnessUsage(HarnessSession{Agent: "codex", Kind: "id", Value: "s2", Home: home}, usageStart, usageEnd())
	if tokens == nil || *tokens != 10 || cost != nil {
		t.Fatalf("%v %v", tokens, cost)
	}
}

func TestUsageIsUnsetWhenNothingWasReported(t *testing.T) {
	home := t.TempDir()
	log := filepath.Join(home, "direct.jsonl")
	writeLog(t, log, `{"timestamp":"2026-01-01T10:00:01Z","type":"user"}`+"\n")
	for name, session := range map[string]HarnessSession{
		"nothing logged": {Kind: "path", Value: log},
		"no file":        {Agent: "claude", Kind: "id", Value: "absent", Home: home},
		"no agent":       {Agent: "other", Kind: "id", Value: "x", Home: home},
		"no home":        {Agent: "claude", Kind: "id", Value: "x"},
		"escaping id":    {Agent: "claude", Kind: "id", Value: "../x", Home: home},
		"bad pattern":    {Agent: "claude", Kind: "id", Value: "[", Home: home},
		"unreadable":     {Kind: "path", Value: filepath.Join(home, "absent")},
		"a folder":       {Kind: "path", Value: home},
	} {
		if tokens, cost := HarnessUsage(session, usageStart, usageEnd()); tokens != nil || cost != nil {
			t.Errorf("%s: %v %v", name, tokens, cost)
		}
	}
}
