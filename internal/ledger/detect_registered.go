package ledger

import "slices"

func scopeVars(scope Scope, kind string) []string {
	switch kind {
	case KindScratch:
		return scope.Scratch
	case KindDatabase:
		return scope.Databases
	}
	return nil
}

func (p *planner) current(record Record) bool {
	return record.Task == p.s.Task && record.Execution == p.s.Execution
}

// registered is detector 4: folders and databases named by config variables,
// the sole source of database candidates, and any other kind a
// hook recorded. A variable has no execution, so without a record it stays a
// weak signal. It runs last so that it can report records nothing else
// matched.
func (p *planner) registered() []*candidate {
	var ids []Identity
	seen := make(map[Identity]bool)
	add := func(id Identity) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	vars := make(map[Identity]bool)
	for _, kind := range []string{KindScratch, KindDatabase} {
		for _, name := range scopeVars(p.s.Scope, kind) {
			id := Identity{Kind: kind, ID: name}
			vars[id] = true
			add(id)
		}
	}
	for _, record := range p.s.Records {
		if p.current(record) && !matchedKind(record.Kind) {
			add(Identity{Kind: record.Kind, ID: record.ID})
		}
	}
	found := make([]*candidate, 0, len(ids))
	for _, id := range ids {
		c := newCandidate(id.Kind, id.ID, id)
		if vars[id] {
			c.ev.add(SignalConfigVar)
		}
		p.applyRecords(&c.ev, id, true)
		for _, other := range p.s.Others {
			if slices.Contains(scopeVars(other, id.Kind), id.ID) {
				c.ev.share("task:" + other.Task)
			}
		}
		found = append(found, c)
	}
	for i, record := range p.s.Records {
		if p.current(record) && !p.used[i] {
			c := newCandidate(KindRecord, record.Kind+":"+record.ID, Identity{Kind: record.Kind, ID: record.ID, Start: record.Start})
			c.ev.add(SignalRecordUnmatched)
			found = append(found, c)
		}
	}
	return found
}
