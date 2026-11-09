package context

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Query narrows a Log. Filters apply to the listing; Heads and Parents still
// describe the whole graph.
type Query struct {
	Since time.Time
	Task  string
}

// Chronology is the graph in reading order: parents before children, and by
// time, then id, among events that are free to come next.
type Chronology struct {
	Events []Event  `json:"events"`
	Heads  []string `json:"heads"`
}

// InvalidError says a chronology cannot be ordered until its problems are
// fixed.
type InvalidError struct {
	Report Report
}

func (e *InvalidError) Error() string {
	first := e.Report.Problems[0]
	return fmt.Sprintf("the chronology has %d problems, first %s: %s", len(e.Report.Problems), first.File, first.Detail)
}

// Log reads the chronology in order.
func Log(folder string, q Query) (Chronology, error) {
	report, events, err := inspect(folder)
	if err != nil {
		return Chronology{}, err
	}
	if len(report.Problems) > 0 {
		return Chronology{}, &InvalidError{Report: report}
	}
	c := Chronology{Events: []Event{}, Heads: heads(events)}
	for _, e := range topological(events) {
		if !e.At.Before(q.Since) && (q.Task == "" || e.Task == q.Task) {
			c.Events = append(c.Events, e)
		}
	}
	return c, nil
}

// topological orders a graph already known to be acyclic and complete.
func topological(events []Event) []Event {
	waiting := make(map[string]int, len(events))
	children := make(map[string][]Event, len(events))
	var free eventHeap
	for _, e := range events {
		waiting[e.ID] = len(e.Parents)
		if len(e.Parents) == 0 {
			free = append(free, e)
		}
		for _, parent := range e.Parents {
			children[parent] = append(children[parent], e)
		}
	}
	heap.Init(&free)
	out := make([]Event, 0, len(events))
	for free.Len() > 0 {
		e := heap.Pop(&free).(Event)
		out = append(out, e)
		for _, child := range children[e.ID] {
			waiting[child.ID]--
			if waiting[child.ID] == 0 {
				heap.Push(&free, child)
			}
		}
	}
	return out
}

type eventHeap []Event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	if !h[i].At.Equal(h[j].At) {
		return h[i].At.Before(h[j].At)
	}
	return h[i].ID < h[j].ID
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(Event)) }
func (h *eventHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// WriteText prints the chronology as a list grouped by UTC date.
func (c Chronology) WriteText(w io.Writer) error {
	out := bufio.NewWriter(w)
	date := ""
	for _, e := range c.Events {
		if day := e.At.Format(time.DateOnly); day != date {
			date = day
			fmt.Fprintln(out, day)
		}
		task := e.Task
		if task == "" {
			task = "-"
		}
		summary, _, _ := strings.Cut(e.Body, "\n")
		line := strings.Join([]string{e.At.Format("15:04:05Z"), e.Kind, task, e.Actor, summary}, "  ")
		fmt.Fprintln(out, "  "+strings.TrimRight(line, " "))
	}
	return out.Flush()
}

// WriteJSON prints the whole graph.
func (c Chronology) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}
