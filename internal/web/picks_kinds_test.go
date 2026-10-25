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
		{Role: "orchestrator", Runner: "herdr", Harness: "codex", Model: "m-a", Effort: "high"},
		{Role: "integrator", Runner: "herdr", Harness: "codex", Model: "m-b"},
		{Role: "reviewer", Runner: "subagent", Harness: "claude", Model: "m-c", Effort: "high"},
		{Role: "implementer", Runner: "herdr", Harness: "muse", Model: "m-d", Effort: "max", EffortFlag: "--reasoning-effort"},
	})
	if len(ks) != 2 || ks[0].SkillPrefix != "$" || !ks[0].Checkpointer || !ks[0].Saved || len(ks[0].Models) != 2 || ks[0].DefaultModel != "m-a" {
		t.Fatalf("%+v", ks)
	}
	// One model, both saved efforts: each role's exact pick launches.
	for _, e := range []string{"low", "high"} {
		if args, err := launchArgs(&ks[0], "m-a", e); err != nil || args[len(args)-1] != "model_reasoning_effort="+e {
			t.Errorf("%s: %v %v", e, args, err)
		}
	}
	if args, err := launchArgs(&ks[0], "m-b", ""); err != nil || len(args) != 2 {
		t.Errorf("no effort: %v %v", args, err)
	}
	// The effort option goes with the saved pick.
	if args, err := launchArgs(&ks[1], "m-d", "max"); err != nil || args[2] != "--reasoning-effort" {
		t.Errorf("muse %v %v", args, err)
	}
	// A saved default pick (no model) starts the harness's default.
	pi := pickKinds([]choices.Pick{{Role: "orchestrator", Runner: "herdr", Harness: "pi"}})
	if args, err := launchArgs(&pi[0], "", ""); err != nil || len(args) != 0 {
		t.Errorf("default pick %v %v", args, err)
	}
}
