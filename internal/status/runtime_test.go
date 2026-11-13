package status

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Each observation is its own line, attention and unresolved items carry the
// waiter's flag, and the flags read back as the agent snapshot's do.
func TestRuntimeShowsEachFieldSeparately(t *testing.T) {
	var b bytes.Buffer
	WriteRuntime(&b, []Agent{
		{
			Agent: "proj-harbor-orchestrator", Role: "orchestrator", Backend: "herdr-core", Execution: "e1", Generation: 2, Address: "t-7", Source: "record",
			Process: "live", Readiness: "ready", Program: "done", RawProgram: "done", Visibility: "verified", App: "closed", Orchestration: "active",
			LastVerified: time.Unix(1_800_000_000, 0),
			Attention:    []Attention{{Reason: "notice_held", Gate: "route_held", Detail: "the notice cannot be claimed"}},
			Unresolved:   []Unresolved{{Kind: "typed_submit", ID: "s1", State: "unresolved", Detail: "the reply was lost", Choices: []string{"keep_waiting", "stop_execution"}}},
			Notices:      &Notices{Issued: 1, Claimed: 1, Handled: 3, OpenClaim: "n-2", HeldGate: "route_held"},
			Actions:      []string{"show", "submit"},
		},
		{Agent: "proj-harbor-adjutant", Role: "adjutant", Backend: "herdr-core", Execution: "e2", Generation: 1, Process: "lost", Readiness: "identity_unknown",
			Program: "unknown", RawProgram: "thinking", Visibility: "missing"},
		{Agent: "proj-old-orchestrator", Backend: "herdr-tui", Source: "legacy"},
	})
	out := b.String()
	for _, want := range []string{
		"== proj-harbor-orchestrator (orchestrator, herdr-core, e1 gen 2)",
		"   process: live\n", "   readiness: ready\n", "   program: done\n", "   visibility: verified, app closed\n", "   orchestration: active\n",
		"   last verified: 2027-01-15T08:00:00Z",
		"attention: notice_held (route_held): the notice cannot be claimed  <-- NEEDS PERSON notice_held",
		"unresolved: typed_submit s1 unresolved: the reply was lost  <-- UNRESOLVED typed_submit", "choices: keep_waiting, stop_execution",
		"notices: issued 1, claimed 1, handled 3, returned 0, open claim n-2, held by route_held",
		"actions: show, submit",
		"process: lost  <-- LOST", "program: unknown (reported thinking)", "visibility: missing  <-- NOT VISIBLE",
		"== proj-old-orchestrator (-, herdr-tui, legacy)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	want := []string{
		"proj-harbor-adjutant LOST", "proj-harbor-adjutant NOT VISIBLE",
		"proj-harbor-orchestrator NEEDS PERSON notice_held", "proj-harbor-orchestrator UNRESOLVED typed_submit",
	}
	if got := Flags(out); !reflect.DeepEqual(got, want) {
		t.Errorf("flags %q, want %q", got, want)
	}
}

func TestRuntimeWithNoExecutions(t *testing.T) {
	var b bytes.Buffer
	WriteRuntime(&b, nil)
	if b.String() != "executions: none\n" {
		t.Fatalf("%q", b.String())
	}
}
