package core_test

import (
	"slices"
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The qualification facts of spec section 9 are adapter data: each row of the
// probes' table is held by the adapter it describes, and the level follows the
// open decisions (Claude Code unattended, Codex attended while its queued wake
// stays the owner's decision).
func TestAdapterFactsMatchTheProbes(t *testing.T) {
	claude, err := core.AdapterFor("claude")
	if err != nil {
		t.Fatal(err)
	}
	codex, err := core.AdapterFor("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"claude rest state", claude.RestState, "done"},
		{"codex rest state", codex.RestState, "done"},
		{"claude waits as", claude.Facts.WaitingStatus, "done"},
		{"codex waits as", codex.Facts.WaitingStatus, "done"},
		{"claude wake", claude.Facts.Wake, core.WakeBackgroundWait},
		// The native queue wakes a proven session without terminal input.
		{"codex wake", codex.Facts.Wake, core.WakeQueue},
		{"claude after an interrupt", claude.Facts.ResumesAfterInterrupt, true},
		{"codex after an interrupt", codex.Facts.ResumesAfterInterrupt, false},
		{"claude interrupt resumable", claude.InterruptResumable, true},
		{"codex interrupt resumable", codex.InterruptResumable, false},
		{"claude compacts by itself", claude.Facts.Compaction.BySelf, true},
		{"codex compacts by itself", codex.Facts.Compaction.BySelf, true},
		{"claude compacts below its window", claude.Facts.Compaction.BelowWindow, int64(33000)},
		{"codex level from", codex.Facts.Compaction.LaunchSetting, "model_auto_compact_token_limit"},
		{"claude level", claude.Facts.Level, core.LevelUnattended},
		{"codex level", codex.Facts.Level, core.LevelAttended},
		{"claude unattended", claude.Unattended(), true},
		{"codex unattended", codex.Unattended(), false},
		{"claude person-only states", len(claude.Facts.PersonOnly), 0},
		{"codex start path", codex.StartPath, core.StartPathPerson},
		{"claude start path", claude.StartPath, core.StartPathPerson},
		{"codex requires --no-daemon", slices.Contains(codex.RequiredArgs, "--no-daemon"), true},
		{"claude probed version", claude.Facts.Version, "2.1.322"},
		{"codex probed version", codex.Facts.Version, "0.164.0"},
		{"restricted codex on core", codex.RestrictedOnCore, true},
		{"restricted claude on core", claude.RestrictedOnCore, false},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}
	for _, a := range []core.Adapter{claude, codex} {
		if a.InterruptAtRest == "" || a.InterruptInTurn == "" || a.Facts.StopReaches == "" || a.Facts.WorkingOutsideTurn == "" || len(a.Facts.IdleMeans) == 0 {
			t.Errorf("%s: an interrupt, stop or status fact is missing: %+v", a.Kind, a)
		}
	}
}

// The early checkpoint is set below the level at which a harness compacts by
// itself: Claude Code 33,000 tokens below its window, Codex at the launch's own
// setting and nowhere when the launch sets none.
func TestCompactionLevel(t *testing.T) {
	claude, _ := core.AdapterFor("claude")
	codex, _ := core.AdapterFor("codex")
	if got, ok := claude.CompactionLevel(nil, 0); !ok || got != 200000-33000 {
		t.Errorf("claude default window: %d %v", got, ok)
	}
	if got, ok := claude.CompactionLevel(nil, 1000000); !ok || got != 1000000-33000 {
		t.Errorf("claude 1M window: %d %v", got, ok)
	}
	if _, ok := claude.CompactionLevel(nil, 20000); ok {
		t.Error("a window below the headroom has no level")
	}
	if _, ok := codex.CompactionLevel([]string{"--no-daemon"}, 0); ok {
		t.Error("codex with no setting has no measured level: interval only")
	}
	for _, args := range [][]string{
		{"--no-daemon", "-c", "model_auto_compact_token_limit=150000"},
		{"--config", "model_auto_compact_token_limit=150000"},
		{"--config=model_auto_compact_token_limit=150000"},
		{"-cmodel_auto_compact_token_limit=150000"},
		{"-c", "model_auto_compact_token_limit=9", "-c", "model_auto_compact_token_limit=150000"},
		{"-c", `model_auto_compact_token_limit="150000"`},
	} {
		if got, ok := codex.CompactionLevel(args, 0); !ok || got != 150000 {
			t.Errorf("%q: %d %v", args, got, ok)
		}
	}
	for _, args := range [][]string{
		{"-c", "model_auto_compact_token_limit=0"},
		{"-c", "model_auto_compact_token_limit=lots"},
		{"-c", "other=150000"},
		{"-c"},
	} {
		if got, ok := codex.CompactionLevel(args, 0); ok {
			t.Errorf("%q: %d is not a level", args, got)
		}
	}
}

func reasons(steps []core.PersonStep) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Reason)
	}
	return out
}

// up names, before the route starts, the steps that will need a person.
func TestPersonSteps(t *testing.T) {
	claude, _ := core.AdapterFor("claude")
	codex, _ := core.AdapterFor("codex")
	if got := reasons(claude.PersonSteps(false)); !slices.Equal(got, []string{core.AttentionStartLine}) {
		t.Errorf("claude: %v", got)
	}
	if got := reasons(claude.PersonSteps(true)); !slices.Equal(got, []string{core.AttentionStartLine, core.AttentionCompactionDue}) {
		t.Errorf("claude with compaction off: %v", got)
	}
	want := []string{core.AttentionStartLine, core.AttentionInterrupted, core.AttentionGoalPaused}
	if got := reasons(codex.PersonSteps(false)); !slices.Equal(got, want) {
		t.Errorf("codex: %v, want %v", got, want)
	}
	for _, s := range codex.PersonSteps(true) {
		if s.When == "" || s.What == "" {
			t.Errorf("a step says neither when nor what: %+v", s)
		}
	}
}
