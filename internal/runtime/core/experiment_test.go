package core_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// New core executions are not labelled as the retired opt-in experiment.
// Historical experiment labels remain preserved by the store migration tests.
func TestCoreRecordsAreNotExperimental(t *testing.T) {
	w := newWorld(t)
	w.openApp()
	l, rec := w.up("omega", "omega-o", "orchestrator", "claude")
	if rec.Experiment != "" {
		t.Fatalf("the launch's record: %q", rec.Experiment)
	}
	if got := w.ready(l); got.Experiment != "" {
		t.Fatalf("after readiness: %q", got.Experiment)
	}
	if runtime.Experiment(runtime.BackendCore) != "" || runtime.Experiment(runtime.BackendTUI) != "" {
		t.Fatalf("the labels: core %q, tui %q", runtime.Experiment(runtime.BackendCore), runtime.Experiment(runtime.BackendTUI))
	}
}
