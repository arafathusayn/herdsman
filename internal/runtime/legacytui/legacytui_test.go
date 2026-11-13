package legacytui_test

import (
	"context"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/legacytui"
)

func provider(t *testing.T) (*legacytui.Provider, *herdrtest.Fixture, *herdrtest.World, *herdrtest.Server) {
	t.Helper()
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	world := herdrtest.ServeWorld(fx)
	return legacytui.New(srv.Socket, herdr.New(srv.Socket)), fx, world, srv
}

func TestEnsureExecutionAdoptsOrStartsAsTheTUIDid(t *testing.T) {
	p, _, world, _ := provider(t)
	ctx := context.Background()
	pane := world.AddPane("orchestrator", "")
	placed := 0
	intent := runtime.Intent{Name: "repo-harbor-orchestrator", Kind: "codex", Timeout: time.Second,
		Place: func(context.Context) (string, []string, error) { placed++; return pane, []string{"--yolo"}, nil }}
	got, err := p.EnsureExecution(ctx, intent)
	if err != nil || got.Outcome != runtime.OutcomeCreated || got.Execution.Pane != pane || got.Execution.Kind != "codex" || placed != 1 {
		t.Fatalf("%+v %v placed %d", got, err, placed)
	}
	again, err := p.EnsureExecution(ctx, intent)
	if err != nil || again.Outcome != runtime.OutcomeAdopted || placed != 1 || world.Starts(intent.Name) != 1 {
		t.Fatalf("%+v %v placed %d", again, err, placed)
	}
	if again.Execution.Observation.Program != runtime.ProgramIdle || again.Execution.Observation.Readiness != runtime.ReadinessReady {
		t.Fatalf("%+v", again.Execution.Observation)
	}
	// An agent of the same name and another kind goes on to herdr, which
	// refuses the name: a conflict, and no second agent.
	other := intent
	other.Kind = "claude"
	got, err = p.EnsureExecution(ctx, other)
	if err == nil || got.Outcome != runtime.OutcomeConflict {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestInspectStopAndVerify(t *testing.T) {
	p, fx, world, srv := provider(t)
	ctx := context.Background()
	pane := world.AddPane("orchestrator", "")
	fx.SetAgent(pane, herdrtest.Agent(pane, "session-1"))
	fx.Edit(pane, "agent_status", "compacting")
	got, err := p.Inspect(ctx, runtime.Target{Pane: pane})
	if err != nil || !got.Exists || got.Observation.Program != runtime.ProgramUnknown || got.Observation.RawProgram != "compacting" {
		t.Fatalf("%+v %v", got, err)
	}
	id, code, _ := p.Verifier().Live(ctx, inbox.PaneRef(pane))
	if code != inbox.LiveUnknown {
		t.Fatalf("an unknown status passed the identity check: %d %+v", code, id)
	}
	fx.Edit(pane, "agent_status", "idle")
	if _, code, _ := p.Verifier().Live(ctx, inbox.PaneRef(pane)); code != inbox.LiveOK {
		t.Fatalf("code %d", code)
	}
	if _, code, note := p.Verifier().Live(ctx, inbox.Ref{Backend: inbox.BackendCore, Execution: "x"}); code != inbox.LiveUnknown || note == "" {
		t.Fatalf("a core reference passed the TUI check: %d", code)
	}
	if vis, err := p.EnsureVisible(ctx, runtime.Target{Pane: pane}); err != nil || vis != runtime.VisibilityVerified {
		t.Fatalf("%s %v", vis, err)
	}
	srv.ForgetRequests()
	stopped, err := p.Stop(ctx, runtime.Target{Pane: pane})
	if err != nil || stopped.Already {
		t.Fatalf("%+v %v", stopped, err)
	}
	if again, err := p.Stop(ctx, runtime.Target{Pane: pane}); err != nil || !again.Already {
		t.Fatalf("%+v %v", again, err)
	}
	gone, err := p.Inspect(ctx, runtime.Target{Pane: pane})
	if err != nil || gone.Exists || gone.Observation.Visibility != runtime.VisibilityMissing {
		t.Fatalf("%+v %v", gone, err)
	}
	caps, err := p.Capabilities(ctx)
	if err == nil && (caps.Backend != runtime.BackendTUI || caps.EndpointID != srv.Socket || !caps.TypedInput) {
		t.Fatalf("%+v", caps)
	}
}
