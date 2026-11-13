package core_test

import (
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

func pid(n uint32) *uint32 { return &n }

// Every class of the shared-core inventory, under both profiles.
func TestInventoryClasses(t *testing.T) {
	member := runtime.Execution{ID: "e-member", Repo: "/r", Route: "a", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "t1", Evidence: runtime.ProcessEvidence{PID: 100, Start: "s100"}}
	lost := runtime.Execution{ID: "e-lost", Repo: "/r", Route: "a", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "t8", Evidence: runtime.ProcessEvidence{PID: 800, Start: "s800"}}
	other := runtime.Execution{ID: "e-other", Repo: "/r", Route: "b", Backend: runtime.BackendCore, Desired: runtime.DesiredRunning,
		TerminalAddress: "t7", Evidence: runtime.ProcessEvidence{PID: 700, Start: "s700"}}
	routeLabels := func(exec string) map[string]string {
		return map[string]string{herdrapp.LabelRoute: "route-a", herdrapp.LabelExecution: exec}
	}
	live := func(addr string, p uint32, labels map[string]string) hc.Terminal {
		if labels == nil {
			labels = map[string]string{}
		}
		return hc.Terminal{Address: addr, State: "live", PID: pid(p), Labels: labels, Argv: []string{"sh"}}
	}
	reported := live("t3", 300, nil)
	reported.Parent, reported.Reported = "t1", true
	harnessArgv := live("t4", 400, nil)
	harnessArgv.Parent, harnessArgv.Argv = "t1", []string{"/opt/bin/codex", "--no-daemon"}
	headless := live("t5", 500, nil)
	headless.Parent = "t1"
	declared := live("t6", 600, nil)
	declared.Parent = "t1"
	plainChild := live("t9", 900, nil)
	plainChild.Parent = "t1"
	grandchild := live("t10", 1000, nil)
	grandchild.Parent = "t9"
	foreign := live("t11", 1100, nil)
	foreign.Status = &hc.Status{State: "idle"}
	terms := []hc.Terminal{
		live("t1", 100, routeLabels("e-member")),
		live("t2", 200, routeLabels("e-copied")), // an impostor
		reported, harnessArgv, headless, declared,
		live("t7", 700, nil),
		live("t8", 801, routeLabels("e-lost")), // the shell after e-lost
		plainChild, grandchild, foreign,
		live("t12", 1200, nil),
	}
	procs := []core.Proc{{PID: 501, PPID: 500, Argv: []string{"node", "/x/claude"}}}
	in := core.InventoryInput{Terminals: terms, Executions: []runtime.Execution{member, lost, other}, Repo: "/r", Route: "a", RouteID: "route-a",
		Declared: map[string]bool{"t6": true}, Procs: procs, HarnessNames: []string{"claude", "codex"}}
	want := map[string]core.Class{
		"t1": core.ClassMember, "t2": core.ClassImpostor, "t3": core.ClassAgentChild, "t4": core.ClassAgentChild, "t5": core.ClassAgentChild,
		"t6": core.ClassDeclaredCommand, "t7": core.ClassOtherRouteMember, "t8": core.ClassRecoveryShell, "t9": core.ClassUnclassifiedChild,
		"t10": core.ClassUnclassifiedChild, "t11": core.ClassForeignAgent, "t12": core.ClassPlain,
	}
	inv := core.Classify(in)
	faults := map[string]bool{}
	for _, e := range inv.Entries {
		if want[e.Address] != e.Class {
			t.Errorf("%s is %s, want %s", e.Address, e.Class, want[e.Address])
		}
		if e.Fault {
			faults[e.Address] = true
		}
	}
	if inv.Rule != core.RuleRoute || inv.Foreign != 1 {
		t.Fatalf("rule %s, foreign %d", inv.Rule, inv.Foreign)
	}
	for _, addr := range []string{"t2", "t3", "t4", "t5"} {
		if !faults[addr] {
			t.Errorf("%s is no fault under the default profile", addr)
		}
	}
	for _, addr := range []string{"t6", "t9", "t10", "t11", "t8"} {
		if faults[addr] {
			t.Errorf("%s is a fault under the default profile", addr)
		}
	}
	if !strings.Contains(inv.FaultText(), "t2 (impostor") || !strings.Contains(inv.FaultText(), "inventory rule route") {
		t.Errorf("fault text = %s", inv.FaultText())
	}

	in.Restricted = true
	strict := core.Classify(in)
	if strict.Rule != core.RuleWholeCore {
		t.Fatalf("rule %s", strict.Rule)
	}
	strictFaults := map[string]bool{}
	for _, e := range strict.Faults() {
		strictFaults[e.Address] = true
	}
	for _, addr := range []string{"t9", "t10", "t11"} {
		if !strictFaults[addr] {
			t.Errorf("%s is no fault under the restricted profile", addr)
		}
	}
	if strictFaults["t6"] || strictFaults["t12"] {
		t.Errorf("a declared command or a plain terminal is a fault: %v", strictFaults)
	}
}
