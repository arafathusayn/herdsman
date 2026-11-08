package ledger

import (
	"context"
	"fmt"
)

// Read gives a fresh snapshot of the machine.
type Read func(ctx context.Context) (Snapshot, error)

type lineKey struct{ kind, id string }

func indexLines(plan Plan) map[lineKey]Line {
	lines := make(map[lineKey]Line, len(plan.Lines))
	for _, line := range plan.Lines {
		lines[lineKey{line.Kind, line.ID}] = line
	}
	return lines
}

// revalidation is what one action may rely on: a fresh read of the machine
// and a plan built from it, proved unchanged against what was approved.
type revalidation struct {
	snapshot Snapshot
	plan     Plan
	lines    map[lineKey]Line
}

// revalidate re-reads the machine immediately before an action. A failed read
// ends the teardown, because nothing was proved.
func revalidate(ctx context.Context, read Read, approved Plan) (*revalidation, error) {
	snapshot, err := read(ctx)
	if err != nil {
		return nil, fmt.Errorf("ledger: revalidate: %w", err)
	}
	if err := CheckUnchanged(approved, snapshot); err != nil {
		return nil, err
	}
	plan := BuildPlan(snapshot)
	return &revalidation{snapshot: snapshot, plan: plan, lines: indexLines(plan)}, nil
}

// stillAuthorized refuses a target whose line is no longer the same action.
// CheckUnchanged reads levels and identities only, so a process that took the
// agent's role since approval is caught here.
func (v *revalidation) stillAuthorized(target Line) error {
	now, ok := v.lines[lineKey{target.Kind, target.ID}]
	if ok && now.Action == target.Action {
		return nil
	}
	current := LevelNoEvidence
	if ok {
		current = now.Level
	}
	return &StopError{Changes: []Change{{Kind: target.Kind, ID: target.ID, FromLevel: target.Level, ToLevel: current, FromIdentity: target.Identity, ToIdentity: target.Identity}}}
}

// processAlive reports the target's pid and start time among the running
// processes; a reused pid has another start time and so another identity.
func (v *revalidation) processAlive(target Line) bool {
	for _, process := range v.snapshot.Processes {
		if ProcessIdentity(process.Pid, process.Start).String() == target.Identity {
			return true
		}
	}
	return false
}
