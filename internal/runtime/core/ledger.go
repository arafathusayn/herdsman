package core

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Resource kinds a core route holds in the resource ledger.
const (
	ResourceExecution = "core-execution"
	ResourceShell     = "recovery-shell"
	ResourceChild     = "core-child"
)

// Resource is one ledger line of a core route: its identity, the core address
// beside it, and the evidence level that decides what may act on it.
type Resource struct {
	Kind      string
	Address   string
	Execution string
	Identity  ledger.Identity
	Evidence  ledger.Evidence
	Class     Class
}

// Level is the resource's evidence level under the ledger's table.
func (r Resource) Level() ledger.Level { return r.Evidence.Level() }

// String names the resource for a person.
func (r Resource) String() string {
	s := fmt.Sprintf("%s %s at %s (%s)", r.Kind, r.Identity, r.Address, r.Level())
	if r.Kind == ResourceShell {
		s += ", the recovery shell of lost execution " + r.Execution
	}
	return s
}

// Resources lists a route's core resources (spec sections 12 and 13). An
// execution is exclusive, keyed by its (pid, start time), because its record
// proves it. A recovery shell is another process than the one its execution
// recorded, so it is associated at most, and so is every child but a
// declared command. Nothing associated is stopped without a person naming it.
func Resources(inv Inventory, execs []runtime.Execution, terminals []hc.Terminal, procs ProcessTable) []Resource {
	byID := map[string]runtime.Execution{}
	for _, e := range execs {
		byID[e.ID] = e
	}
	byAddr := map[string]hc.Terminal{}
	for _, t := range terminals {
		byAddr[t.Address] = t
	}
	var out []Resource
	for _, entry := range inv.Entries {
		t := byAddr[entry.Address]
		ident := ledger.Identity{Kind: ledger.KindProcess}
		if t.PID != nil {
			pid := int(*t.PID)
			ident.ID = fmt.Sprint(pid)
			if start, err := procs.Identify(pid); err == nil {
				ident.Start = start
			}
		}
		r := Resource{Address: entry.Address, Execution: entry.Execution, Identity: ident, Class: entry.Class}
		switch entry.Class {
		case ClassMember:
			e := byID[entry.Execution]
			start, _ := time.Parse(time.RFC3339Nano, e.Evidence.Start)
			r.Kind, r.Identity = ResourceExecution, ledger.ProcessIdentity(e.Evidence.PID, start)
			r.Evidence = ledger.Evidence{Recorded: true}
		case ClassDeclaredCommand:
			r.Kind, r.Evidence = ResourceChild, ledger.Evidence{Recorded: true}
		case ClassRecoveryShell:
			r.Kind, r.Evidence = ResourceShell, ledger.Evidence{Signals: []ledger.Signal{ledger.SignalExecLabel}}
		case ClassAgentChild, ClassUnclassifiedChild:
			r.Kind, r.Evidence = ResourceChild, ledger.Evidence{Signals: []ledger.Signal{ledger.SignalExecLabel}}
		default:
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Resource) int { return strings.Compare(a.Address, b.Address) })
	return out
}

// StopSet splits resources into what a stop acts on and what it lists and
// leaves: only exclusive resources are acted on, plus an associated one a
// person named by its address. No automation names a recovery shell.
func StopSet(resources []Resource, named map[string]bool) (act, leave []Resource) {
	for _, r := range resources {
		if r.Level() == ledger.LevelExclusive || (r.Level() == ledger.LevelAssociated && named[r.Address]) {
			act = append(act, r)
		} else {
			leave = append(leave, r)
		}
	}
	return act, leave
}
