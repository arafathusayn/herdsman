package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Spawn starts the reserved launch's wrapper in a new terminal (spec section
// 8, step 3): no core parent, no core notes, the record kept, the terminal
// kept so that the app adopts it, and the placement and ownership labels
// written at once. Core's env holds nothing at all: the wrapper builds the
// harness environment itself.
func Spawn(ctx context.Context, c *hc.Client, l Launch) (hc.Terminal, error) {
	if err := l.Validate(); err != nil {
		return hc.Terminal{}, err
	}
	labels, err := l.Labels().Map()
	if err != nil {
		return hc.Terminal{}, err
	}
	return c.Run(ctx, hc.RunRequest{Argv: l.Wrapper, Cwd: l.Cwd, Labels: labels, Name: l.Name, Keep: true})
}

// Lister lists terminals on the verified core.
type Lister interface {
	List(ctx context.Context, labels map[string]string, all bool) (hc.ListResult, error)
}

// Verdict is what reconciliation found for one launch.
type Verdict int

// The reconciliation verdicts.
const (
	// Absent: no terminal of this launch exists, live or finished.
	Absent Verdict = iota + 1
	// Adopt: exactly one terminal is this launch's, and its process is the
	// registered one or nothing has registered yet.
	Adopt
	// Conflict: something is found and is not provably this launch: two
	// matches, copied labels, or a different process at the address.
	Conflict
	// Finished: the launch's terminal exists and has ended.
	Finished
)

func (v Verdict) String() string {
	return [...]string{"", "absent", "adopt", "conflict", "finished"}[v]
}

// Found is reconciliation's answer.
type Found struct {
	Verdict  Verdict
	Terminal hc.Terminal
	Detail   string
}

// Reconcile looks for the launch's terminal over live and finished records on
// the verified core (spec section 8, step 4). A label only locates; ownership
// is every launch label with the reserved value, and once the launch has
// registered, the terminal's program must be the registered process. A
// recovery shell at the address is found but different. Nothing here starts
// or kills anything.
func Reconcile(ctx context.Context, c Lister, procs ProcessTable, l Launch, evidence runtime.ProcessEvidence) (Found, error) {
	got, err := c.List(ctx, map[string]string{herdrapp.LabelExecution: l.Execution}, true)
	if err != nil {
		return Found{}, err
	}
	want, err := l.Labels().Map()
	if err != nil {
		return Found{}, err
	}
	var owned, copied []hc.Terminal
	for _, t := range got.Terminals {
		if sameLabels(t.Labels, want) {
			owned = append(owned, t)
		} else {
			copied = append(copied, t)
		}
	}
	switch {
	case len(owned) == 0 && len(copied) == 0:
		return Found{Verdict: Absent, Detail: "no terminal carries execution " + l.Execution}, nil
	case len(owned) > 1:
		return Found{Verdict: Conflict, Detail: fmt.Sprintf("%d terminals carry the labels of execution %s: %s", len(owned), l.Execution, addresses(owned))}, nil
	case len(owned) == 0:
		return Found{Verdict: Conflict, Detail: fmt.Sprintf("terminals %s carry execution %s with other ownership labels", addresses(copied), l.Execution)}, nil
	}
	t := owned[0]
	if !registered(runtime.Execution{Evidence: evidence}) {
		if !t.Live() {
			return Found{Verdict: Finished, Terminal: t, Detail: fmt.Sprintf("terminal %s ended before the launch registered (%s)", t.Address, ending(t))}, nil
		}
		return Found{Verdict: Adopt, Terminal: t}, nil
	}
	if !t.Live() {
		return Found{Verdict: Finished, Terminal: t, Detail: fmt.Sprintf("terminal %s has ended (%s)", t.Address, ending(t))}, nil
	}
	if t.PID == nil || int(*t.PID) != evidence.PID {
		return Found{Verdict: Conflict, Terminal: t, Detail: fmt.Sprintf("terminal %s runs another process than the registered %d: a recovery shell or a replacement, never this execution", t.Address, evidence.PID)}, nil
	}
	alive, err := Alive(procs, evidence)
	if err != nil {
		return Found{}, err
	}
	if !alive {
		return Found{Verdict: Conflict, Terminal: t, Detail: fmt.Sprintf("terminal %s holds pid %d, which is no longer the registered process", t.Address, evidence.PID)}, nil
	}
	return Found{Verdict: Adopt, Terminal: t}, nil
}

func sameLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func addresses(ts []hc.Terminal) string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Address)
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

func ending(t hc.Terminal) string {
	switch {
	case t.ExitCode != nil:
		return fmt.Sprintf("exit %d", *t.ExitCode)
	case t.Signal != nil:
		return fmt.Sprintf("signal %d", *t.Signal)
	case t.Reason != "":
		return t.Reason
	}
	return "finished"
}
