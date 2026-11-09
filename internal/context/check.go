package context

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Codes of the problems Check reports.
const (
	CodeMalformed     = "malformed"
	CodeMissingParent = "missing-parent"
	CodeCycle         = "cycle"
	CodeNotMonotone   = "not-monotone"
)

const eventExt = ".md"

// Problem is one thing wrong with one event file.
type Problem struct {
	File   string `json:"file"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Report is the result of Check: Events counts the files that parsed.
type Report struct {
	Events   int       `json:"events"`
	Problems []Problem `json:"problems"`
}

// Check validates the chronology. Problems in the files are in the report; the
// error is only for a folder that cannot be read.
func Check(folder string) (Report, error) {
	report, _, err := inspect(folder)
	return report, err
}

func inspect(folder string) (Report, []Event, error) {
	events, problems, err := load(folder)
	if err != nil {
		return Report{}, nil, err
	}
	problems = append(problems, graphProblems(events)...)
	problems = append(problems, orderProblems(events)...)
	slices.SortStableFunc(problems, func(a, b Problem) int { return cmp.Compare(a.File, b.File) })
	return Report{Events: len(events), Problems: problems}, events, nil
}

// load parses the events folder. Events come back in id order, and each file
// that is no valid event is a problem. Dot files are not events.
func load(folder string) ([]Event, []Problem, error) {
	dir := filepath.Join(folder, EventsDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotContextRepo, folder)
	}
	if err != nil {
		return nil, nil, err
	}
	var events []Event
	var problems []Problem
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		e, err := loadFile(dir, entry)
		if err != nil {
			problems = append(problems, Problem{File: name, Code: CodeMalformed, Detail: err.Error()})
			continue
		}
		events = append(events, e)
	}
	return events, problems, nil
}

func loadFile(dir string, entry os.DirEntry) (Event, error) {
	name := entry.Name()
	if !entry.Type().IsRegular() {
		return Event{}, malformed("not a regular file")
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return Event{}, err
	}
	e, err := parseEvent(data)
	if err != nil {
		return Event{}, err
	}
	if name != e.ID+eventExt {
		return Event{}, malformed("the file name does not match id %s", e.ID)
	}
	return e, nil
}

// graphProblems reports parents that do not exist and events on a cycle.
func graphProblems(events []Event) []Problem {
	var problems []Problem
	known := make(map[string]bool, len(events))
	for _, e := range events {
		known[e.ID] = true
	}
	parents := make(map[string][]string, len(events))
	children := make(map[string][]string, len(events))
	for _, e := range events {
		for _, parent := range e.Parents {
			if !known[parent] {
				problems = append(problems, Problem{File: e.ID + eventExt, Code: CodeMissingParent, Detail: "parent " + parent + " does not exist"})
				continue
			}
			parents[e.ID] = append(parents[e.ID], parent)
			children[parent] = append(children[parent], e.ID)
		}
	}
	// Peeling from the roots leaves cycles and what follows them; peeling the
	// rest from the leaves leaves only the cycles.
	left := peel(known, parents, children)
	left = peel(left, children, parents)
	for _, e := range events {
		if left[e.ID] {
			problems = append(problems, Problem{File: e.ID + eventExt, Code: CodeCycle, Detail: "the event is its own ancestor"})
		}
	}
	return problems
}

// peel removes nodes with no remaining incoming edge until none is left to
// remove, and returns the nodes that stayed.
func peel(nodes map[string]bool, incoming, outgoing map[string][]string) map[string]bool {
	count := make(map[string]int, len(nodes))
	var queue []string
	for id := range nodes {
		for _, from := range incoming[id] {
			if nodes[from] {
				count[id]++
			}
		}
		if count[id] == 0 {
			queue = append(queue, id)
		}
	}
	left := make(map[string]bool, len(nodes))
	for id := range nodes {
		left[id] = true
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		delete(left, id)
		for _, to := range outgoing[id] {
			if !left[to] {
				continue
			}
			count[to]--
			if count[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	return left
}

// orderProblems holds each actor to one order: ids, times and parent links
// must agree. Events is in id order.
func orderProblems(events []Event) []Problem {
	var problems []Problem
	last := map[string]Event{}
	actor := make(map[string]string, len(events))
	for _, e := range events {
		actor[e.ID] = e.Actor
	}
	for _, e := range events {
		if prev, ok := last[e.Actor]; ok && e.At.Before(prev.At) {
			problems = append(problems, Problem{File: e.ID + eventExt, Code: CodeNotMonotone, Detail: "earlier than " + prev.ID + " by the same actor"})
		}
		last[e.Actor] = e
		for _, parent := range e.Parents {
			if parent > e.ID && actor[parent] == e.Actor {
				problems = append(problems, Problem{File: e.ID + eventExt, Code: CodeNotMonotone, Detail: "sorts before its parent " + parent + " by the same actor"})
			}
		}
	}
	return problems
}

// heads lists the events no other event names as a parent, in id order.
func heads(events []Event) []string {
	named := map[string]bool{}
	for _, e := range events {
		for _, parent := range e.Parents {
			named[parent] = true
		}
	}
	out := []string{}
	for _, e := range events {
		if !named[e.ID] {
			out = append(out, e.ID)
		}
	}
	return out
}
