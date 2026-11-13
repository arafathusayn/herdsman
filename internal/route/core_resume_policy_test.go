package route_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/route"
)

func TestCoreRestrictedResumePreservesSandboxAndManualPair(t *testing.T) {
	r := newCoreRig(t)
	q := r.request()
	q.Policy = policy.Policy{Profile: policy.ProfileRestricted}
	q.PairManual = true
	q.AdjutantCommand, q.OrchestratorCommand = "codex --no-daemon", "codex --no-daemon"
	q.Core.NoticeSocket = "/srv/worker.sock"
	p := r.plan(q)
	start := &ops.Operation{ID: "start", Key: p.Spec.Key, Kind: p.Spec.Kind, State: ops.OpDone}
	for _, s := range p.Spec.Steps {
		start.Steps = append(start.Steps, ops.Step{StepSpec: s, State: ops.StepDone})
	}
	resumed, err := route.PlanControl(context.Background(), route.ControlRequest{Verb: route.VerbResume, Start: start, Generation: 2, From: route.ControlStopped, Date: "2026-11-13"})
	if err != nil {
		t.Fatal(err)
	}
	registers, enrolls := 0, 0
	for _, s := range resumed.Spec.Steps {
		var data struct {
			Sandbox *struct{ Root string }
			Manual  bool
		}
		if err := json.Unmarshal([]byte(s.Spec), &data); err != nil {
			t.Fatal(err)
		}
		switch s.Effect {
		case "core.register":
			registers++
			if data.Sandbox == nil || data.Sandbox.Root == "" {
				t.Fatal("restricted resume lost its sandbox")
			}
		case "core.enroll":
			enrolls++
			if !data.Manual {
				t.Fatal("restricted resume silently auto-approved its pair")
			}
		}
	}
	if registers != 2 || enrolls != 1 {
		t.Fatalf("missing restart steps: %d registrations, %d enrollments", registers, enrolls)
	}
}
