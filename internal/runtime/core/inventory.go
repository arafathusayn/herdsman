package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Class is what the inventory found a terminal to be (spec section 6).
type Class string

// The inventory classes.
const (
	ClassMember            Class = "member"
	ClassAgentChild        Class = "agent_child"
	ClassDeclaredCommand   Class = "declared_command"
	ClassUnclassifiedChild Class = "unclassified_child"
	ClassImpostor          Class = "impostor"
	ClassRecoveryShell     Class = "recovery_shell"
	ClassOtherRouteMember  Class = "other_route_member"
	ClassForeignAgent      Class = "foreign_agent"
	ClassPlain             Class = "plain"
)

// The inventory rules.
const (
	// RuleRoute covers what the route created; the default profile.
	RuleRoute = "route"
	// RuleWholeCore faults any other agent on the core; the restricted
	// profile, which needs a dedicated core.
	RuleWholeCore = "whole-core"
)

// InventoryInput is one inventory pass.
type InventoryInput struct {
	// Terminals are every live terminal of the verified core, read without a
	// label filter.
	Terminals []hc.Terminal
	// Executions are every execution record of this installation.
	Executions []runtime.Execution
	Repo       string
	Route      string
	// RouteID is the route's opaque id, as its labels carry it.
	RouteID string
	// Declared are terminal addresses a member registered with `herdsman
	// resource add`.
	Declared map[string]bool
	// Procs is the process table, for harness names under a child's pid.
	Procs []Proc
	// HarnessNames are the program names that count as a coding agent.
	HarnessNames []string
	Restricted   bool
}

// Entry is one classified terminal.
type Entry struct {
	Address string
	Class   Class
	// Execution is the member's (or the lost execution's, for a shell, or
	// the parent member's, for a child) execution id.
	Execution string
	Fault     bool
	Reason    string
}

// Inventory is a pass's result.
type Inventory struct {
	Rule    string
	Entries []Entry
	// Foreign counts foreign agents on the core.
	Foreign int
}

// Faults lists the entries that are faults.
func (inv Inventory) Faults() []Entry {
	var out []Entry
	for _, e := range inv.Entries {
		if e.Fault {
			out = append(out, e)
		}
	}
	return out
}

// OK reports a pass with no fault.
func (inv Inventory) OK() bool { return len(inv.Faults()) == 0 }

// FaultText names the rule in force and the terminals at fault.
func (inv Inventory) FaultText() string {
	faults := inv.Faults()
	if len(faults) == 0 {
		return fmt.Sprintf("inventory rule %s: no fault", inv.Rule)
	}
	var parts []string
	for _, f := range faults {
		parts = append(parts, fmt.Sprintf("%s (%s: %s)", f.Address, f.Class, f.Reason))
	}
	return fmt.Sprintf("inventory rule %s: %s", inv.Rule, strings.Join(parts, "; "))
}

// Classify sorts every live terminal of the core by registration and parent
// chain (spec section 6). A label alone never classifies: a member is a
// terminal whose program is a registered execution's process, and route
// labels without one make an impostor. The lack of a status report decides
// nothing.
func Classify(in InventoryInput) Inventory {
	inv := Inventory{Rule: RuleRoute}
	if in.Restricted {
		inv.Rule = RuleWholeCore
	}
	byPID := map[int]runtime.Execution{}
	lostAt := map[string]runtime.Execution{}
	for _, e := range in.Executions {
		if e.Backend != runtime.BackendCore || !registered(e) {
			continue
		}
		if running(e) {
			byPID[e.Evidence.PID] = e
		}
		if e.Repo == in.Repo && e.Route == in.Route && e.TerminalAddress != "" {
			lostAt[e.TerminalAddress] = e
		}
	}
	members := map[string]runtime.Execution{}
	for _, t := range in.Terminals {
		if !t.Live() || t.PID == nil {
			continue
		}
		if e, ok := byPID[int(*t.PID)]; ok && e.TerminalAddress == t.Address && e.Repo == in.Repo && e.Route == in.Route {
			members[t.Address] = e
		}
	}
	byAddr := map[string]hc.Terminal{}
	for _, t := range in.Terminals {
		byAddr[t.Address] = t
	}
	memberOf := func(t hc.Terminal) (runtime.Execution, bool) {
		seen := map[string]bool{}
		for p := t.Parent; p != "" && !seen[p]; {
			seen[p] = true
			if e, ok := members[p]; ok {
				return e, true
			}
			parent, ok := byAddr[p]
			if !ok {
				break
			}
			p = parent.Parent
		}
		return runtime.Execution{}, false
	}
	for _, t := range in.Terminals {
		if !t.Live() {
			continue
		}
		e := Entry{Address: t.Address}
		pid := 0
		if t.PID != nil {
			pid = int(*t.PID)
		}
		agent := statusEver(t) || harnessIn(t, pid, in.Procs, in.HarnessNames)
		switch m, isMember := members[t.Address]; {
		case isMember:
			e.Class, e.Execution = ClassMember, m.ID
		default:
			if parent, ok := memberOf(t); ok {
				e.Execution = parent.ID
				switch {
				case agent:
					e.Class, e.Fault, e.Reason = ClassAgentChild, true, "an agent a member started outside the route's control"
				case in.Declared[t.Address]:
					e.Class = ClassDeclaredCommand
				default:
					e.Class = ClassUnclassifiedChild
					if in.Restricted {
						e.Fault, e.Reason = true, "the restricted profile allows no unclassified child"
					}
				}
				break
			}
			if other, ok := byPID[pid]; ok && other.TerminalAddress == t.Address {
				e.Class, e.Execution = ClassOtherRouteMember, other.ID
				break
			}
			if in.RouteID != "" && t.Labels[herdrapp.LabelRoute] == in.RouteID {
				if lost, ok := lostAt[t.Address]; ok && t.Labels[herdrapp.LabelExecution] == lost.ID {
					e.Class, e.Execution = ClassRecoveryShell, lost.ID
					break
				}
				e.Class, e.Fault, e.Reason = ClassImpostor, true, "it carries the route's labels and no registration"
				break
			}
			if agent {
				e.Class = ClassForeignAgent
				inv.Foreign++
				if in.Restricted {
					e.Fault, e.Reason = true, "the restricted profile allows no other agent on its core"
				}
				break
			}
			e.Class = ClassPlain
		}
		inv.Entries = append(inv.Entries, e)
	}
	slices.SortFunc(inv.Entries, func(a, b Entry) int { return strings.Compare(a.Address, b.Address) })
	return inv
}

// statusEver reports a terminal that has reported an agent status at any time.
func statusEver(t hc.Terminal) bool { return t.Reported || t.Status != nil }

// harnessIn reports a known harness name in the terminal's argv, its
// foreground, or the process tree under its pid.
func harnessIn(t hc.Terminal, pid int, procs []Proc, names []string) bool {
	is := func(word string) bool {
		return slices.Contains(names, strings.TrimPrefix(filepath.Base(word), "-"))
	}
	for i, w := range t.Argv {
		if i < 2 && is(w) {
			return true
		}
	}
	if f := t.Foreground; f != nil {
		if is(f.Name) {
			return true
		}
		for i, w := range f.Argv {
			if i < 2 && is(w) {
				return true
			}
		}
	}
	if pid > 0 {
		for _, p := range descendants(procs, pid) {
			for i, w := range p.Argv {
				if i < 2 && is(w) {
					return true
				}
			}
		}
	}
	return false
}
