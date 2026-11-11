package core_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Anything an experiment writes names its version: every execution record
// the core backend writes carries runtime.core/0.1.0 from its reservation on.
func TestCoreRecordsNameTheExperiment(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	l, rec := w.up("omega", "omega-o", "orchestrator", "claude")
	if rec.Experiment != runtime.ExperimentLabel {
		t.Fatalf("the launch's record: %q", rec.Experiment)
	}
	if got := w.ready(l); got.Experiment != runtime.ExperimentLabel {
		t.Fatalf("after readiness: %q", got.Experiment)
	}
	if runtime.Experiment(runtime.BackendCore) != runtime.ExperimentLabel || runtime.Experiment(runtime.BackendTUI) != "" {
		t.Fatalf("the labels: core %q, tui %q", runtime.Experiment(runtime.BackendCore), runtime.Experiment(runtime.BackendTUI))
	}
}
