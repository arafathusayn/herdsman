package web

import (
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// Each role is its own unit: the adjutant's exact saved pick (no effort)
// starts as saved even when the orchestrator's saved pick of the same
// harness and model names an effort.
func TestSavedPicksAreCheckedPerRole(t *testing.T) {
	f := newRouteFixture(t)
	st := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	if err := st.Save(
		choices.Pick{Role: "adjutant", Runner: "herdr", Harness: "opencode", Model: "p/m1"},
		choices.Pick{Role: "orchestrator", Runner: "herdr", Harness: "opencode", Model: "p/m1", Effort: "high", EffortFlag: "--variant"},
	); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = st
	req := f.adjutant()
	req.Kind, req.Model, req.Effort = "opencode", "p/m1", ""
	req.OrchKind, req.OrchModel, req.OrchEffort = "opencode", "p/m1", "high"
	req.Checkpointer.On = false
	if code, out := f.post(t, req); code != 200 || !out.OK {
		t.Fatalf("%d %+v", code, out)
	}
}
