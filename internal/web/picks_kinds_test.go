package web

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// The saved picks' catalog groups picks by harness, keeps Codex's skill
// prefix, and leaves out subagent picks and picks without a model.
func TestPickKinds(t *testing.T) {
	ks := pickKinds([]choices.Pick{
		{Role: "adjutant", Runner: "herdr", Harness: "codex", Model: "m-a", Effort: "low"},
		{Role: "orchestrator", Runner: "herdr", Harness: "codex", Model: "m-b"},
		{Role: "reviewer", Runner: "subagent", Harness: "claude", Model: "m-c", Effort: "high"},
		{Role: "implementer", Runner: "herdr", Harness: "pi"},
	})
	if len(ks) != 1 || ks[0].SkillPrefix != "$" || !ks[0].Checkpointer || len(ks[0].Models) != 2 ||
		ks[0].Models[0].Efforts[0] != "low" || len(ks[0].Models[1].Efforts) != 0 || ks[0].DefaultModel != "m-a" {
		t.Fatalf("%+v", ks)
	}
}
