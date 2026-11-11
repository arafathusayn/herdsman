package web

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// fakeRuntime is the daemon's RuntimeService for the web's actions: one core
// execution and one TUI execution, and a record of what reached it.
type fakeRuntime struct {
	daemonv1.RuntimeService
	submits []*daemonv1.RuntimeServiceSubmitRequest
	shows   []string
	state   string
}

func (f *fakeRuntime) GetExecution(_ context.Context, req *daemonv1.RuntimeServiceGetExecutionRequest) (*daemonv1.RuntimeServiceGetExecutionResponse, error) {
	switch req.GetExecutionId() {
	case "e-core":
		return &daemonv1.RuntimeServiceGetExecutionResponse{Execution: &daemonv1.Execution{Agent: "proj-t-orchestrator", Route: &corev1.RouteId{Value: "t"},
			Ref: &corev1.RuntimeRef{Backend: runtime.BackendCore, ExecutionId: "e-core", TerminalAddress: "t-1"}, Actions: runtime.Actions(runtime.BackendCore)}}, nil
	case "e-tui":
		return &daemonv1.RuntimeServiceGetExecutionResponse{Execution: &daemonv1.Execution{Agent: "im-auth",
			Ref: &corev1.RuntimeRef{Backend: runtime.BackendTUI, ExecutionId: "e-tui", Pane: "w1:p2"}, Actions: runtime.Actions(runtime.BackendTUI)}}, nil
	}
	return nil, rpc.Errorf(rpc.NotFound, "no execution %s", req.GetExecutionId())
}

func (f *fakeRuntime) Submit(_ context.Context, req *daemonv1.RuntimeServiceSubmitRequest) (*daemonv1.RuntimeServiceSubmitResponse, error) {
	f.submits = append(f.submits, req)
	state := f.state
	if state == "" {
		state = core.SubmitTyped
	}
	return &daemonv1.RuntimeServiceSubmitResponse{SubmitId: "s-1", State: state}, nil
}

func (f *fakeRuntime) Show(_ context.Context, req *daemonv1.RuntimeServiceShowRequest) (*daemonv1.RuntimeServiceShowResponse, error) {
	f.shows = append(f.shows, req.GetExecutionId())
	return &daemonv1.RuntimeServiceShowResponse{Backend: runtime.BackendCore}, nil
}

// herdrMutations lists the herdr calls that change something.
func herdrMutations(f *fixture) []string {
	var out []string
	for _, r := range f.herdr.Requests() {
		if r.Method != "session.snapshot" && r.Method != "pane.read" && r.Method != "ping" && !strings.HasPrefix(r.Method, "events.") {
			out = append(out, r.Method)
		}
	}
	return out
}

// Web actions keyed by a runtime reference: on herdr-core, an action that
// types becomes one person's typed submit through RuntimeService, after the
// warning was shown and confirmed; focus becomes show; keys and pane actions
// are refused by name. No core action reaches herdr. A TUI execution goes to
// its pane as before, within its backend's list.
func TestWebActionsFollowEachBackendsList(t *testing.T) {
	f := newFixture(t)
	rt := &fakeRuntime{}
	f.queue.runtime = rt
	f.herdr.ForgetRequests()

	_, r := f.act(t, Action{Type: "prompt", Execution: "e-core", Text: "carry on"})
	if r.OK || r.Code != "needs_confirmation" || !strings.Contains(r.Error, "nothing can recall it") || len(rt.submits) != 0 {
		t.Fatalf("unconfirmed: %+v %v", r, rt.submits)
	}
	_, r = f.act(t, Action{Type: "prompt", Execution: "e-core", Text: "carry on", Confirmed: true})
	if !r.OK || len(rt.submits) != 1 || !rt.submits[0].GetConfirmed() || rt.submits[0].GetText() != "carry on" || rt.submits[0].GetPid() == 0 {
		t.Fatalf("confirmed: %+v %v", r, rt.submits)
	}
	if _, r = f.act(t, Action{Type: "text", Execution: "e-core", Text: "two\nlines", Confirmed: true}); r.Code != "invalid_action" {
		t.Fatalf("two lines: %+v", r)
	}
	for _, typ := range []string{"keys", "input", "close", "split", "scroll"} {
		if _, r = f.act(t, Action{Type: typ, Execution: "e-core", Keys: []string{"Enter"}}); r.Code != "not_on_backend" || !strings.Contains(r.Error, "show, submit") {
			t.Errorf("%s on core: %+v", typ, r)
		}
	}
	if _, r = f.act(t, Action{Type: "focus", Execution: "e-core"}); !r.OK || !slices.Equal(rt.shows, []string{"e-core"}) {
		t.Fatalf("focus on core: %+v %v", r, rt.shows)
	}
	rt.state = core.SubmitUnresolved
	if _, r = f.act(t, Action{Type: "submit", Execution: "e-core", Text: "again", Confirmed: true}); r.OK || r.Code != "unresolved" || !strings.Contains(r.Error, "send again") {
		t.Fatalf("unresolved: %+v", r)
	}
	if got := herdrMutations(f); len(got) != 0 {
		t.Fatalf("a core action reached herdr: %v", got)
	}

	// The TUI execution: its pane, and only its list.
	if _, r = f.act(t, Action{Type: "submit", Execution: "e-tui", Text: "x", Confirmed: true}); r.Code != "not_on_backend" {
		t.Fatalf("submit on the TUI: %+v", r)
	}
	if _, r = f.act(t, Action{Type: "prompt", Execution: "e-tui", Text: "hi"}); !r.OK {
		t.Fatalf("prompt on the TUI: %+v", r)
	}
	if got := herdrMutations(f); !slices.Contains(got, "agent.prompt") {
		t.Fatalf("the TUI prompt did not reach its pane: %v", got)
	}
	if _, r = f.act(t, Action{Type: "prompt", Execution: "e-gone", Text: "hi"}); r.Code != string(rpc.NotFound) {
		t.Fatalf("unknown execution: %+v", r)
	}
	f.queue.runtime = nil
	if _, r = f.act(t, Action{Type: "show", Execution: "e-core"}); r.Code != "unavailable" {
		t.Fatalf("no daemon: %+v", r)
	}
}
