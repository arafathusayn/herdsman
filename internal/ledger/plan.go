package ledger

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is the version of the plan document. It only grows.
const SchemaVersion = "1.0.0"

// Scope is what identifies one task on the machine.
type Scope struct {
	Task      string
	Execution string
	// Worktree must be resolved, as the paths lsof and docker report are.
	Worktree  string
	Compose   string
	Ports     PortRange
	Scratch   []string
	Databases []string
}

// The roles of processes that are listed and never signaled.
const (
	RolePaneShell = "pane-shell"
	RoleAgent     = "agent"
)

// Excluded is the pane shell or the agent of the task.
type Excluded struct {
	Identity Identity
	Role     string
}

// Snapshot is everything one plan is built from. Records hold every task's
// records, so that another task's claim on a resource is visible.
type Snapshot struct {
	Scope
	// Started is when the execution began; an older resource is shared.
	Started time.Time
	// Earlier lists this task's own past executions. A task slug is unique
	// only within a project, so a record of any execution not listed here is
	// someone else's.
	Earlier []string
	// Commands is each pid's whole argv from a read with no environment.
	Commands    map[int]string
	Others      []Scope
	Excluded    []Excluded
	Records     []Record
	Processes   []Process
	Cwds        []CwdEntry
	Listeners   []Listener
	Connections []Connection
	Containers  []Container
	Volumes     []Resource
	Networks    []Resource
}

// Action is what teardown may do with a line.
type Action string

// The actions of a plan line.
const (
	// ActionStop runs without a question.
	ActionStop Action = "stop"
	// ActionAsk needs --include associated or a per-line yes.
	ActionAsk Action = "ask"
	// ActionReport lists the resource and never touches it.
	ActionReport Action = "report"
	// ActionExclude is the pane shell or agent: listed, excluded from signals.
	ActionExclude Action = "exclude"
)

// ClassCompensatable is the class of every teardown line: what is stopped can
// be started again, and nothing is removed.
const ClassCompensatable = "compensatable"

// Restart is what a stopped process needs to start again.
type Restart struct {
	Command string   `json:"command"`
	Folder  string   `json:"folder,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Cut marks a command known to differ from what ran.
	Cut bool `json:"cut,omitempty"`
}

// Line is one resource in the plan document.
type Line struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Identity   string   `json:"identity"`
	Level      Level    `json:"level"`
	Class      string   `json:"class"`
	Action     Action   `json:"action"`
	Signals    []Signal `json:"signals,omitempty"`
	SharedWith []string `json:"shared_with,omitempty"`
	Role       string   `json:"role,omitempty"`
	Name       string   `json:"name,omitempty"`
	State      string   `json:"state,omitempty"`
	Parent     int      `json:"parent,omitempty"`
	Group      int      `json:"group,omitempty"`
	Restart    *Restart `json:"restart,omitempty"`
}

// Schema carries the document's version.
type Schema struct {
	Version string `json:"version"`
}

// Plan is the teardown plan document, one line per resource.
type Plan struct {
	Schema    Schema `json:"schema"`
	Task      string `json:"task"`
	Execution string `json:"execution"`
	Worktree  string `json:"worktree"`
	Lines     []Line `json:"lines"`
}

// Encode writes the plan as indented JSON.
func (p Plan) Encode(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(p)
}

type candidate struct {
	line Line
	ev   Evidence
	// proven means a record of this execution names the exact command.
	proven bool
}

func newCandidate(kind, id string, identity Identity) *candidate {
	return &candidate{line: Line{Kind: kind, ID: id, Identity: identity.String(), Class: ClassCompensatable}}
}

type planner struct {
	s     *Snapshot
	used  []bool
	byPid map[int]*Process
}

// applyRecords reads every record of the identity. A record of this execution
// counts when authorize is set and is sharing evidence otherwise, since a name
// that is claimed and unproven may be someone else's. Only this task's listed
// earlier executions are ignored. It reports whether a counted record was an
// adoption.
func (p *planner) applyRecords(ev *Evidence, id Identity, authorize bool) bool {
	adopted := false
	for i, record := range p.s.Records {
		switch {
		case !record.matches(id):
		case p.current(record) && !authorize:
			ev.share("unproven:" + record.Kind + ":" + record.ID)
		case p.current(record):
			ev.Recorded = true
			ev.add(SignalRecord)
			p.used[i] = true
			adopted = adopted || record.Adopted
		case record.Task != p.s.Task:
			ev.share("task:" + record.Task)
		case !slices.Contains(p.s.Earlier, record.Execution):
			ev.share("execution:" + record.Execution)
		}
	}
	return adopted
}

// older applies the age rule. A creation time that could not be read proves
// nothing, so the resource cannot rise above unknown unless a person adopted
// it. lstart and docker timestamps have whole seconds, so the execution's
// start is cut to the second before comparing.
func (p *planner) older(ev *Evidence, start time.Time, adopted bool) {
	switch {
	case adopted:
	case start.IsZero():
		ev.Unverified = true
	case !p.s.Started.IsZero() && start.Before(p.s.Started.Truncate(time.Second)):
		ev.share(SharedOlder)
	}
}

func actionFor(line Line) Action {
	switch {
	case line.Role != "":
		return ActionExclude
	case line.Kind != KindProcess && line.Kind != KindContainer:
		return ActionReport
	case line.Kind == KindContainer && !slices.Contains([]string{"running", "paused", "restarting"}, line.State):
		return ActionReport
	case line.Level == LevelExclusive:
		return ActionStop
	case line.Level == LevelAssociated:
		return ActionAsk
	}
	return ActionReport
}

func (c *candidate) finish() Line {
	line := c.line
	line.Level = c.ev.Level()
	line.Signals = sortedUnique(c.ev.Signals)
	line.SharedWith = sortedUnique(c.ev.SharedWith)
	line.Action = actionFor(line)
	return line
}

func sortedUnique[T cmp.Ordered](values []T) []T {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return slices.Compact(sorted)
}

// BuildPlan runs the four detectors in a fixed order. It is pure: the
// same snapshot gives the same document, which is what the golden test pins.
func BuildPlan(snapshot Snapshot) Plan {
	p := &planner{s: &snapshot, used: make([]bool, len(snapshot.Records)), byPid: make(map[int]*Process, len(snapshot.Processes))}
	for i := range snapshot.Processes {
		p.byPid[snapshot.Processes[i].Pid] = &snapshot.Processes[i]
	}
	processes := p.processes()
	found := p.ports(processes)
	for _, c := range processes {
		found = append(found, c)
	}
	found = append(found, p.containers()...)
	found = append(found, p.labelled(KindVolume, snapshot.Volumes)...)
	found = append(found, p.labelled(KindNetwork, snapshot.Networks)...)
	found = append(found, p.registered()...)

	lines := make([]Line, 0, len(found))
	for _, c := range found {
		lines = append(lines, c.finish())
	}
	order := []string{KindProcess, KindPort, KindContainer, KindVolume, KindNetwork, KindScratch, KindDatabase, KindRecord}
	slices.SortFunc(lines, func(a, b Line) int {
		return cmp.Or(
			cmp.Compare(slices.Index(order, a.Kind), slices.Index(order, b.Kind)),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return Plan{Schema: Schema{Version: SchemaVersion}, Task: snapshot.Task, Execution: snapshot.Execution, Worktree: snapshot.Worktree, Lines: lines}
}

// LevelNoEvidence is the new level of a live resource that lost its line.
const LevelNoEvidence Level = "no-evidence"

// Change is a line whose level or identity moved since the plan was approved.
type Change struct {
	Kind         string
	ID           string
	FromLevel    Level
	ToLevel      Level
	FromIdentity string
	ToIdentity   string
}

func (c Change) String() string {
	text := fmt.Sprintf("%s %s: %s -> %s", c.Kind, c.ID, c.FromLevel, c.ToLevel)
	if c.FromIdentity != c.ToIdentity {
		text += fmt.Sprintf(" (identity %s -> %s)", c.FromIdentity, c.ToIdentity)
	}
	return text
}

// StopError says the plan must stop and be made again.
type StopError struct {
	Changes []Change
}

func (e *StopError) Error() string {
	parts := make([]string, 0, len(e.Changes))
	for _, change := range e.Changes {
		parts = append(parts, change.String())
	}
	return "ledger: plan stopped: " + strings.Join(parts, "; ")
}

// live is the set of pids, ports and ids the snapshot shows, whatever their
// evidence and start or creation time.
func (s Snapshot) live() map[string]bool {
	found := make(map[string]bool)
	pid := func(number int) { found[ProcessIdentity(number, time.Time{}).String()] = true }
	for _, process := range s.Processes {
		found[KindProcess] = true
		pid(process.Pid)
	}
	// ps and lsof are separate reads: a pid either one shows is alive.
	for _, entry := range s.Cwds {
		pid(entry.Pid)
	}
	for _, listener := range s.Listeners {
		pid(listener.Pid)
		found[fmt.Sprintf("%s:%d/%d", KindPort, listener.Port, listener.Pid)] = true
	}
	for _, connection := range s.Connections {
		pid(connection.Pid)
	}
	for _, container := range s.Containers {
		found[KindContainer] = true
		found[Identity{Kind: KindContainer, ID: container.ID}.String()] = true
	}
	for _, volume := range s.Volumes {
		found[KindVolume] = true
		found[Identity{Kind: KindVolume, ID: volume.Name}.String()] = true
	}
	for _, network := range s.Networks {
		found[KindNetwork] = true
		found[Identity{Kind: KindNetwork, ID: network.Name}.String()] = true
	}
	return found
}

// gone reports a missing line whose resource really left the machine. Only
// these kinds can be seen to leave; a registration that vanished is a change.
// A listing with no rows of the kind proves nothing, since the read may have
// failed, so nothing counts as gone from it.
func gone(line Line, live map[string]bool) bool {
	switch line.Kind {
	case KindProcess, KindContainer, KindVolume, KindNetwork:
		return live[line.Kind] && !live[line.Kind+":"+line.ID]
	case KindPort:
		return !live[KindPort+":"+line.ID]
	}
	return false
}

// CheckUnchanged rebuilds the plan from a fresh snapshot and returns a
// *StopError when a line changed level or identity, or has no line now while
// its pid or id still exists. That includes a pid reused by an unrelated
// process: nil must be safe to act on, so only a pid, port or id absent from
// the snapshot counts as gone. A new line is outside what was approved.
func CheckUnchanged(approved Plan, fresh Snapshot) error {
	current := BuildPlan(fresh)
	live := fresh.live()
	type key struct{ kind, id string }
	now := make(map[key]Line, len(current.Lines))
	for _, line := range current.Lines {
		now[key{line.Kind, line.ID}] = line
	}
	var changes []Change
	for _, was := range approved.Lines {
		is, ok := now[key{was.Kind, was.ID}]
		if !ok {
			if !gone(was, live) {
				changes = append(changes, Change{Kind: was.Kind, ID: was.ID, FromLevel: was.Level, ToLevel: LevelNoEvidence, FromIdentity: was.Identity, ToIdentity: was.Identity})
			}
			continue
		}
		if is.Level != was.Level || is.Identity != was.Identity {
			changes = append(changes, Change{Kind: was.Kind, ID: was.ID, FromLevel: was.Level, ToLevel: is.Level, FromIdentity: was.Identity, ToIdentity: is.Identity})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return &StopError{Changes: changes}
}
