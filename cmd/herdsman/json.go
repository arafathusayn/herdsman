package main

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// Event is one line of `herdsman wait --json` or `herdsman watch ... --json`:
// the text line's first word as lower snake_case, the words after it, and
// its closing time ("15:04" or "15:04:05") when it ends in one.
type Event struct {
	Event  string `json:"event"`
	Detail string `json:"detail"`
	Time   string `json:"time,omitempty"`
}

var clockRe = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}(:[0-9]{2})?$`)

// eventJSON turns each text line written to it into one Event line.
type eventJSON struct {
	w   io.Writer
	buf []byte
}

func newEventJSON(w io.Writer) *eventJSON { return &eventJSON{w: w} }

func (e *eventJSON) Write(p []byte) (int, error) {
	e.buf = append(e.buf, p...)
	for {
		i := bytes.IndexByte(e.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(e.buf[:i])
		e.buf = e.buf[i+1:]
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		ev := Event{Event: strings.ToLower(strings.ReplaceAll(f[0], "-", "_"))}
		f = f[1:]
		if len(f) > 0 && clockRe.MatchString(f[len(f)-1]) {
			ev.Time, f = f[len(f)-1], f[:len(f)-1]
		}
		ev.Detail = strings.Join(f, " ")
		b, _ := json.Marshal(ev)
		if _, err := e.w.Write(append(b, '\n')); err != nil {
			return 0, err
		}
	}
}

// StatusJSON is `herdsman status --json`: one object.
type StatusJSON struct {
	Time    string        `json:"time"`
	Machine MachineJSON   `json:"machine"`
	Agents  []AgentStatus `json:"agents"`
}

// MachineJSON is the machine's line and its flags (LOAD HIGH, MEMORY LOW).
type MachineJSON struct {
	Load5       string   `json:"load_5"`
	CPUs        string   `json:"cpus"`
	FreePercent string   `json:"free_percent"`
	Flags       []string `json:"flags"`
}

// AgentStatus is one agent of the snapshot: its header fields, its other
// lines as printed, and its flags (OVER 30m, NO WRITES in 10m, ...).
type AgentStatus struct {
	Name  string   `json:"name"`
	Pane  string   `json:"pane"`
	State string   `json:"state"`
	Model string   `json:"model"`
	Lines []string `json:"lines"`
	Flags []string `json:"flags"`
}

var (
	machineRe = regexp.MustCompile(`^machine: load (\S+) \(5 min\) on (\S+) cores, memory free (\S+)%`)
	agentRe   = regexp.MustCompile(`^== (\S+) \((\S*)\) state=(\S*) model=(.*)$`)
)

// statusJSON reads a text snapshot into a StatusJSON.
func statusJSON(snap string) []byte {
	s := StatusJSON{Machine: MachineJSON{Flags: []string{}}, Agents: []AgentStatus{}}
	var cur *AgentStatus
	for i, l := range strings.Split(strings.TrimRight(snap, "\n"), "\n") {
		flag := ""
		if j := strings.LastIndex(l, "<-- "); j >= 0 {
			flag = strings.TrimSpace(l[j+4:])
		}
		switch m := agentRe.FindStringSubmatch(l); {
		case i == 0:
			s.Time = l
		case machineRe.MatchString(l):
			mm := machineRe.FindStringSubmatch(l)
			s.Machine.Load5, s.Machine.CPUs, s.Machine.FreePercent = mm[1], mm[2], mm[3]
		case m != nil:
			s.Agents = append(s.Agents, AgentStatus{Name: m[1], Pane: m[2], State: m[3], Model: m[4], Lines: []string{}, Flags: []string{}})
			cur = &s.Agents[len(s.Agents)-1]
		case cur == nil:
			if flag != "" {
				s.Machine.Flags = append(s.Machine.Flags, flag)
			}
		default:
			if flag != "" {
				cur.Flags = append(cur.Flags, flag)
			}
			cur.Lines = append(cur.Lines, strings.TrimSpace(l))
		}
	}
	b, _ := json.Marshal(s)
	return append(b, '\n')
}

// CheckJSON is `herdsman route check --json`: one object. ok is true when
// no item is missing; each item is the text line's status word, lower
// case, and the rest of the line.
type CheckJSON struct {
	OK    bool        `json:"ok"`
	Items []CheckItem `json:"items"`
}

// CheckItem is one line of a route check.
type CheckItem struct {
	Status string `json:"status"`
	Item   string `json:"item"`
}

func checkJSON(text string, code int) []byte {
	r := CheckJSON{OK: code == 0, Items: []CheckItem{}}
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if l == "" {
			continue
		}
		word, rest, _ := strings.Cut(l, " ")
		r.Items = append(r.Items, CheckItem{Status: strings.ToLower(word), Item: rest})
	}
	b, _ := json.Marshal(r)
	return append(b, '\n')
}

// takeJSON removes a --json flag from args and says whether it was there.
func takeJSON(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--json" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}
