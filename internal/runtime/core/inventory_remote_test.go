package core_test

import (
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// Remote records (spec sections 6 and 11): a terminal on another machine of
// the core is classified by its labels and its own reports only. It is never
// a member, another route's member, a recovery shell or a member's child,
// even when its address ends like a member's, its pid is the member's or a
// record names its address; this machine's process table says nothing of it.
func TestInventoryRemoteRecords(t *testing.T) {
	member := runtime.Execution{ID: "e-member", Repo: "/r", Route: "a", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "t1", Evidence: runtime.ProcessEvidence{PID: 100, Start: "s100"}}
	named := runtime.Execution{ID: "e-named", Repo: "/r", Route: "a", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "vps/t2", Evidence: runtime.ProcessEvidence{PID: 200, Start: "s200"}}
	other := runtime.Execution{ID: "e-other", Repo: "/r", Route: "b", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "vps/t7", Evidence: runtime.ProcessEvidence{PID: 700, Start: "s700"}}
	labels := map[string]string{herdrapp.LabelRoute: "route-a", herdrapp.LabelExecution: "e-member"}
	live := func(addr string, p uint32, labels map[string]string) hc.Terminal {
		if labels == nil {
			labels = map[string]string{}
		}
		return hc.Terminal{Address: addr, State: "live", PID: pid(p), Labels: labels, Argv: []string{"sh"}}
	}
	child := live("vps/t3", 300, nil)
	child.Parent = "t1"
	underHarness := live("vps/t4", 400, nil) // a local claude runs under pid 400
	agent := live("vps/t5", 500, nil)
	agent.Reported = true
	terms := []hc.Terminal{
		live("t1", 100, labels),
		live("vps/t1", 100, labels), // the member's address number, pid and labels
		live("vps/t2", 200, map[string]string{herdrapp.LabelRoute: "route-a", herdrapp.LabelExecution: "e-named"}),
		child, underHarness, agent,
		live("vps/t7", 700, nil),
	}
	in := core.InventoryInput{Terminals: terms, Executions: []runtime.Execution{member, named, other}, Repo: "/r", Route: "a", RouteID: "route-a",
		Procs: []core.Proc{{PID: 401, PPID: 400, Argv: []string{"claude"}}}, HarnessNames: []string{"claude"}}
	want := map[string]core.Class{
		"t1": core.ClassMember, "vps/t1": core.ClassImpostor, "vps/t2": core.ClassImpostor, "vps/t3": core.ClassPlain,
		"vps/t4": core.ClassPlain, "vps/t5": core.ClassForeignAgent, "vps/t7": core.ClassPlain,
	}
	inv := core.Classify(in)
	if len(inv.Entries) != len(want) {
		t.Fatalf("entries = %+v", inv.Entries)
	}
	for _, e := range inv.Entries {
		if want[e.Address] != e.Class || e.Fault != (e.Class == core.ClassImpostor) {
			t.Errorf("%s is %s (fault %v), want %s", e.Address, e.Class, e.Fault, want[e.Address])
		}
	}
}
