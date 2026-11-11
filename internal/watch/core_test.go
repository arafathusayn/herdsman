package watch

import (
	"context"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// fakeAgents are herdr-core agents read through the runtime.
type fakeAgents struct {
	status map[string]herdr.Status
	screen map[string]string
	reads  []string
}

func (f *fakeAgents) Status(_ context.Context, id string) (herdr.Status, bool) {
	st, ok := f.status[id]
	return st, ok
}

func (f *fakeAgents) Read(_ context.Context, id string, _ int) (string, bool) {
	f.reads = append(f.reads, id)
	s, ok := f.screen[id]
	return s, ok
}

// On herdr-core the waiter reads an agent by its runtime reference: the
// program status core reports and the terminal through the daemon, never a
// pane, and never the Herdr server.
func TestWaiterReadsCoreAgentsByRuntimeReference(t *testing.T) {
	h := newHarness(t)
	agents := &fakeAgents{status: map[string]herdr.Status{"e-a": "blocked"}, screen: map[string]string{"e-a": "Do you want to proceed?\n"}}
	h.agents = agents
	// A pane variable left over from the TUI does not win over the
	// execution's reference.
	h.screen("w9:p1", "• Working (12s • esc to interrupt)")
	out := h.wait("HERDSMAN_IMPLEMENTERS", "im-a", "HERDSMAN_EXEC_im_a", "e-a", "HERDSMAN_REVIEWERS", "rv-1:exec:e-r")
	match(t, "C1 a blocked core agent is BLOCKED", `BLOCKED im-a Do you want to proceed`, out)
	if !strings.Contains(strings.Join(agents.reads, " "), "e-a") || !strings.Contains(strings.Join(agents.reads, " "), "e-r") {
		t.Errorf("the runtime was not read: %v", agents.reads)
	}
	agents.status["e-a"], agents.screen["e-a"] = "working", "• Working (3s • esc to interrupt)\n"
	out = h.wait("HERDSMAN_IMPLEMENTERS", "im-a", "HERDSMAN_EXEC_im_a", "e-a", "HERDSMAN_REVIEWERS", "rv-1:exec:e-r")
	noMatch(t, "C2 a working core agent is not BLOCKED", `BLOCKED im-a`, out)
	// Without a runtime reader nothing is read and nothing is invented.
	h.agents = nil
	out = h.wait("HERDSMAN_IMPLEMENTERS", "im-a", "HERDSMAN_EXEC_im_a", "e-a", "HERDSMAN_REVIEWERS", "rv-1:exec:e-r")
	noMatch(t, "C3 no reader, no event", `BLOCKED`, out)
}
