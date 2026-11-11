package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Verb is one of a route's lifecycle verbs.
type Verb string

// The verbs.
const (
	VerbPause  Verb = "pause"
	VerbResume Verb = "resume"
	VerbStop   Verb = "stop"
	VerbDelete Verb = "delete"
)

// The operation kinds of the verbs.
const (
	KindPause  = "route.pause"
	KindResume = "route.resume"
	KindStop   = "route.stop"
	KindDelete = "route.delete"
)

// The lifecycle states a route can be in.
const (
	ControlRunning = "running"
	ControlPaused  = "paused"
	ControlStopped = "stopped"
	ControlDeleted = "deleted"
)

// ChannelHold places or lifts a hold on the route's message channel.
const ChannelHold = "hold"

// StatusNotReached marks an agent an interrupt could not reach.
const StatusNotReached = "not reached"

// DefaultGrace is how long stop waits for agents to write their reports.
const DefaultGrace = 2 * time.Minute

const (
	effectInterrupt = "route.interrupt"
	effectQuiet     = "route.checkpointer-stop"
	effectHold      = "route.hold"
	effectUnhold    = "route.unhold"
	effectRecord    = "route.record"
	effectLastCall  = "route.last-call"
	effectClosePane = "route.close-pane"
	effectRetire    = "route.retire"
	effectContinue  = "route.continue"
	effectSnapshot  = "route.snapshot"
	effectDown      = "route.down"
	effectTeardown  = "route.teardown"
	effectRemove    = "route.remove"
)

// controlData is what the steps of a verb carry beyond a start's step data.
type controlData struct {
	Verb       Verb     `json:"verb"`
	Generation int      `json:"generation"`
	Agents     []string `json:"agents,omitempty"`
	GraceMS    int64    `json:"grace_ms,omitempty"`
	Note       string   `json:"note,omitempty"`
	Snapshot   string   `json:"snapshot,omitempty"`
	Captures   []string `json:"captures,omitempty"`
	Targets    []string `json:"targets,omitempty"`
}

// KindOf returns the operation kind of a verb.
func KindOf(v Verb) string { return "route." + string(v) }

// RouteLock names the lock that keeps a route's verbs apart.
func RouteLock(repo, name string) string { return "route:" + repo + ":" + name }

// ControlKeyPrefix starts the idempotency key of a verb. It names the start the
// verb acts on, because a route's lifecycle begins anew with each start.
func ControlKeyPrefix(verb, repo, name, startID string) string {
	return verb + ":" + repo + ":" + name + "@" + startID + "#"
}

// IsControlKind reports whether an operation kind is a lifecycle verb.
func IsControlKind(kind string) bool {
	switch kind {
	case KindPause, KindResume, KindStop, KindDelete:
		return true
	}
	return false
}

// ControlState reads a route's lifecycle state from its verbs, oldest first.
// Only a finished verb counts: a failed one changed nothing it promised.
func ControlState(verbs []*ops.Operation) string {
	state := ControlRunning
	for _, op := range verbs {
		if op.State != ops.OpDone {
			continue
		}
		switch op.Kind {
		case KindPause:
			state = ControlPaused
		case KindResume:
			state = ControlRunning
		case KindStop:
			state = ControlStopped
		case KindDelete:
			state = ControlDeleted
		}
	}
	return state
}

// ControlRequest is one verb on one route.
type ControlRequest struct {
	Verb Verb
	// Start is the route's latest journaled start.
	Start *ops.Operation
	// Generation numbers the route's verbs, so that a repeated verb is new work
	// and a retried one is not.
	Generation int
	// From is the state a resume starts from.
	From       string
	Note       string
	Grace      time.Duration
	Force      bool
	KeepFolder bool
	// KeepWorktrees leaves the route's tasks and their worktrees alone.
	KeepWorktrees bool
	// Teardown also stops the route's task processes, on a pause or stop.
	Teardown bool
	// Date names the snapshot, YYYY-MM-DD.
	Date string
}

// ControlPlan is a verb's journal entry and what to print.
type ControlPlan struct {
	Spec     ops.Spec
	Text     string
	Snapshot string
}

// agentRoles are the coordinator pane labels, in start order.
var agentRoles = []string{roleAdjutant, roleOrchestrator}

// PlanControl computes a verb's steps from the route's journaled start.
func PlanControl(ctx context.Context, q ControlRequest) (*ControlPlan, error) {
	if q.Start == nil || len(q.Start.Steps) == 0 {
		return nil, errors.New("the route has no journaled start")
	}
	first, err := decode(q.Start.Steps[0])
	if err != nil {
		return nil, fmt.Errorf("the journaled start: %w", err)
	}
	if q.Grace == 0 {
		q.Grace = DefaultGrace
	}
	base := stepData{Key: first.Key, Route: first.Route, Repo: first.Repo, Folder: first.Folder, Inbox: first.Inbox, Workspace: first.Workspace, Socket: first.Socket, Executable: first.Executable}
	b := &controlBuilder{q: q, base: base}
	if BackendOf(q.Start) == runtime.BackendCore {
		b.base.Backend = runtime.BackendCore
		if err := b.planCoreControl(); err != nil {
			return nil, err
		}
		return b.plan(first)
	}
	for _, st := range q.Start.Steps {
		if st.Effect == effectAgent {
			d, err := decode(st)
			if err != nil {
				return nil, fmt.Errorf("the journaled start: %w", err)
			}
			b.agents = append(b.agents, d.Name)
		}
	}
	plan := &ControlPlan{}
	switch q.Verb {
	case VerbPause:
		b.pause()
	case VerbResume:
		if err := b.resume(); err != nil {
			return nil, err
		}
	case VerbStop:
		b.stop()
	case VerbDelete:
		if q.Force {
			b.stop()
		}
		root, err := snapshotRoot(ctx, first.Repo)
		if err != nil {
			return nil, err
		}
		plan.Snapshot = filepath.Join(root, fmt.Sprintf("%s-%s-%d", first.Route, q.Date, q.Generation))
		b.remove(plan.Snapshot)
	default:
		return nil, fmt.Errorf("unknown verb %q", q.Verb)
	}
	got, err := b.plan(first)
	if err != nil {
		return nil, err
	}
	got.Snapshot = plan.Snapshot
	return got, nil
}

// plan is the verb's journal entry from the steps built.
func (b *controlBuilder) plan(first stepData) (*ControlPlan, error) {
	q := b.q
	key := ControlKeyPrefix(string(q.Verb), first.Repo, first.Route, q.Start.ID) + strconv.Itoa(q.Generation)
	var text strings.Builder
	fmt.Fprintf(&text, "%s route %s\n", q.Verb, first.Route)
	for _, st := range b.steps {
		fmt.Fprintf(&text, "- %s\n", st.Name)
	}
	plan := &ControlPlan{Text: text.String()}
	plan.Spec = ops.Spec{Key: key, Kind: KindOf(q.Verb), Plan: plan.Text, Locks: []string{RouteLock(first.Repo, first.Route)}, Steps: b.steps}
	return plan, nil
}

type controlBuilder struct {
	q      ControlRequest
	base   stepData
	agents []string
	steps  []ops.StepSpec
}

func (b *controlBuilder) add(name, effectName, key string, d stepData) {
	if d.Control == nil {
		d.Control = b.control()
	}
	raw, err := json.Marshal(d)
	if err != nil {
		panic(err) // the struct holds only strings, numbers and maps
	}
	b.steps = append(b.steps, ops.StepSpec{Name: name, Effect: effectName, Key: key, Spec: string(raw), Retry: ops.RetryIdempotent})
}

func (b *controlBuilder) control() *controlData {
	return &controlData{Verb: b.q.Verb, Generation: b.q.Generation, Agents: b.agents, GraceMS: b.q.Grace.Milliseconds(), Note: b.q.Note}
}

func (b *controlBuilder) with(edit func(*stepData)) stepData {
	d := b.base
	edit(&d)
	return d
}

func (b *controlBuilder) hasCheckpointer() (ops.Step, bool) {
	for _, st := range b.q.Start.Steps {
		if st.Key == "service:"+labelCheckpoint {
			return st, true
		}
	}
	return ops.Step{}, false
}

func (b *controlBuilder) pause() {
	for _, name := range b.agents {
		b.add("interrupt "+name, effectInterrupt, "interrupt:"+name, b.with(func(d *stepData) { d.Name = name }))
	}
	if _, ok := b.hasCheckpointer(); ok {
		b.add("stop the checkpointer's prompts", effectQuiet, "checkpointer", b.with(func(d *stepData) { d.Label = labelCheckpoint }))
	}
	if b.base.Inbox != "" {
		for _, name := range b.agents {
			b.add("hold the channel for "+name, effectHold, "hold:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
	}
	if b.q.Teardown {
		b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
	}
	b.add("record the pause", effectRecord, "record", b.base)
}

func (b *controlBuilder) stop() {
	for _, name := range b.agents {
		b.add("ask "+name+" for its report", effectLastCall, "last-call:"+name, b.with(func(d *stepData) { d.Name = name }))
	}
	labels := append([]string{}, agentRoles...)
	labels = append(labels, labelCourier, labelCardWatch, labelCheckpoint)
	for _, label := range labels {
		b.add("close the "+label+" pane", effectClosePane, "close:"+label, b.with(func(d *stepData) { d.Label = label }))
	}
	if b.base.Inbox != "" {
		b.add("retire the pair", effectRetire, "retire", b.base)
	}
	if b.q.Teardown {
		b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
	}
	b.add("record the stop", effectRecord, "record:stop", b.with(func(d *stepData) {
		d.Control = b.control()
		d.Control.Verb = VerbStop
	}))
}

// restartEffects are the steps of a start that a stopped route runs again. The
// prelude is left out on purpose: it already ran and may not be safe to repeat.
var restartEffects = map[string]bool{effectAgent: true, effectTargets: true, effectAttach: true, effectPropose: true, effectApprove: true, effectService: true}

func (b *controlBuilder) resume() error {
	if b.q.From == ControlStopped {
		for _, st := range b.q.Start.Steps {
			if !restartEffects[st.Effect] {
				continue
			}
			if st.Effect == effectAttach {
				d, err := decode(st)
				if err != nil {
					return fmt.Errorf("the journaled start: %w", err)
				}
				d.Tag = "attach-" + strconv.Itoa(b.q.Generation)
				raw, err := json.Marshal(d)
				if err != nil {
					return err
				}
				st.Spec = string(raw)
			}
			b.steps = append(b.steps, st.StepSpec)
		}
	}
	if b.base.Inbox != "" {
		for _, name := range b.agents {
			b.add("lift the channel hold for "+name, effectUnhold, "unhold:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
	}
	if st, ok := b.hasCheckpointer(); ok && b.q.From == ControlPaused {
		b.steps = append(b.steps, st.StepSpec)
	}
	for _, name := range b.agents {
		b.add("tell "+name+" to continue", effectContinue, "continue:"+name, b.with(func(d *stepData) {
			d.Name, d.Tag = name, "continue-"+strconv.Itoa(b.q.Generation)
		}))
	}
	b.add("record the resume", effectRecord, "record", b.base)
	return nil
}

func (b *controlBuilder) remove(dest string) {
	captures := []string{b.base.Folder}
	if b.base.Inbox != "" {
		captures = append(captures, b.base.Inbox)
	}
	targets := append([]string{}, captures...)
	if b.q.KeepFolder {
		targets = targets[1:]
	}
	data := func() stepData {
		return b.with(func(d *stepData) {
			d.Control = b.control()
			d.Control.Snapshot, d.Control.Captures, d.Control.Targets = dest, captures, targets
		})
	}
	b.add("take and verify the route's snapshot", effectSnapshot, "snapshot", data())
	if !b.q.KeepWorktrees {
		b.add("free the route's tasks", effectDown, "down", data())
	}
	b.add("verify the snapshot again, close the workspace and remove the route", effectRemove, "remove", data())
}

func controlEffects(deps Deps) map[string]ops.Effect {
	return map[string]ops.Effect{
		effectInterrupt: effect{deps, applyInterrupt, probeRow},
		effectQuiet:     effect{deps, applyQuiet, probeQuiet},
		effectHold:      effect{deps, applyHold, probeHold},
		effectUnhold:    effect{deps, applyUnhold, probeUnhold},
		effectRecord:    effect{deps, applyRecord, probeRecord},
		effectLastCall:  effect{deps, applyLastCall, probeRow},
		effectClosePane: effect{deps, applyClosePane, probeClosePane},
		effectRetire:    effect{deps, applyRetire, probeRetire},
		effectContinue:  effect{deps, applyContinue, probeSent},
		effectSnapshot:  effect{deps, applySnapshot, probeSnapshot},
		effectDown:      effect{deps, applyDown, probeUnknown},
		effectTeardown:  effect{deps, applyTeardown, probeUnknown},
		effectRemove:    effect{deps, applyRemove, probeRemove},
	}
}

func controlDir(folder string) string { return filepath.Join(folder, ".control") }

// AgentRow is what one agent was doing when a verb reached it.
type AgentRow struct {
	Name   string `json:"name"`
	Pane   string `json:"pane,omitempty"`
	Status string `json:"status"`
}

// ControlRecord is the last verb's record, kept in the route folder.
type ControlRecord struct {
	Verb       Verb       `json:"verb"`
	Generation int        `json:"generation"`
	Operation  string     `json:"operation"`
	Time       string     `json:"time"`
	Agents     []AgentRow `json:"agents"`
}

// ReadControlRecord reads the last verb's record of a route folder.
func ReadControlRecord(folder string) (*ControlRecord, error) {
	raw, err := os.ReadFile(filepath.Join(controlDir(folder), "record.json"))
	if err != nil {
		return nil, err
	}
	var r ControlRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func rowPath(d stepData) string {
	return filepath.Join(controlDir(d.Folder), fmt.Sprintf("row-%d-%s.json", d.Control.Generation, d.Name))
}

func writeRow(d stepData, row AgentRow) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return writeFileNew(rowPath(d), raw)
}

func probeRow(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(rowPath(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Unknown, "no record that " + d.Name + " was reached", nil
}

func sendKey(ctx context.Context, c *herdr.Client, pane, key string) error {
	return c.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane, "text": "", "keys": []string{key}}, nil)
}

// applyInterrupt sends the harness's interrupt key. It is best effort: a pane
// that cannot be reached is named in the record and the pause goes on, since an
// agent acts on the key at its next turn boundary at the soonest anyway.
func applyInterrupt(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	got, err := deps.provider(c).Interrupt(ctx, runtime.Target{Name: d.Name})
	if err != nil {
		return writeRow(d, AgentRow{Name: d.Name, Status: StatusNotReached})
	}
	row := AgentRow{Name: d.Name, Pane: got.Pane, Status: got.RawProgram}
	if !got.Sent {
		row.Status = StatusNotReached
	}
	return writeRow(d, row)
}

func labelledPane(ctx context.Context, c *herdr.Client, workspace, label string) (string, error) {
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	id := workspaceID(snapshot, workspace)
	if id == "" {
		return "", nil
	}
	for _, p := range snapshot.Panes {
		if p.WorkspaceID == id && labelOf(p) == label {
			return p.ID, nil
		}
	}
	return "", nil
}

// applyQuiet ends the checkpointer's loop and clears its start record, so that
// resume starts it again. The pane stays open.
func applyQuiet(ctx context.Context, _ Deps, c *herdr.Client, d stepData) error {
	pane, err := labelledPane(ctx, c, d.Workspace, d.Label)
	if err != nil {
		return err
	}
	if pane != "" {
		if err := sendKey(ctx, c, pane, "ctrl+c"); err != nil {
			return err
		}
	}
	return removeFile(serviceStamp(d))
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func probeQuiet(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(serviceStamp(d)); errors.Is(err, fs.ErrNotExist) {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func holdFile(d stepData) string { return filepath.Join(d.Inbox, "state", "hold-"+d.Name) }

// channelOpen reports whether the channel can take a hold: an unapproved pair
// has no channel to hold.
func channelOpen(d stepData) (bool, error) {
	_, approved, err := PairState(d.Inbox)
	return approved, err
}

func applyHold(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	open, err := channelOpen(d)
	if err != nil || !open {
		return err
	}
	record, err := c.AgentGet(ctx, d.Name)
	if err != nil {
		return nil // the agent is gone, so nothing would read the channel
	}
	return runChannel(ctx, deps, d, record.PaneID, ChannelHold, d.Name, "hold", d.Name, "the route is paused")
}

func probeHold(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(holdFile(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func applyUnhold(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	if _, err := os.Stat(holdFile(d)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	record, err := c.AgentGet(ctx, d.Name)
	if err != nil {
		return fmt.Errorf("agent %s: %w", d.Name, err)
	}
	return runChannel(ctx, deps, d, record.PaneID, ChannelHold, d.Name, "unhold", d.Name)
}

func probeUnhold(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(holdFile(d)); errors.Is(err, fs.ErrNotExist) {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

func recordStamp(d stepData) string {
	return filepath.Join(controlDir(d.Folder), fmt.Sprintf("recorded-%d-%s", d.Control.Generation, d.Control.Verb))
}

// applyRecord writes the verb's record and its run-state line once.
func applyRecord(_ context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	if _, err := os.Stat(recordStamp(d)); err == nil {
		return nil
	}
	now := time.Now()
	record := ControlRecord{Verb: d.Control.Verb, Generation: d.Control.Generation, Operation: d.Op, Time: now.UTC().Format(time.RFC3339)}
	for _, name := range d.Control.Agents {
		row := AgentRow{Name: name, Status: StatusNotReached}
		named := d
		named.Name = name
		if raw, err := os.ReadFile(rowPath(named)); err == nil {
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
		}
		record.Agents = append(record.Agents, row)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileNew(filepath.Join(controlDir(d.Folder), "record.json"), raw); err != nil {
		return err
	}
	line := map[Verb]string{VerbPause: "paused", VerbResume: "resumed", VerbStop: "stopped"}[d.Control.Verb]
	if err := AppendLog(d.Folder, "route "+line+" by herdsman "+string(d.Control.Verb), now); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeFileNew(recordStamp(d), nil)
}

func probeRecord(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(recordStamp(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// lastCallPrompt asks for a report; the words are the same on every harness.
const lastCallPrompt = "The route is stopping. Write your report now and start no new work."

// applyLastCall gives an agent a bounded time to report, then interrupts it.
// applyLastCall is a TUI step: the report request is typed, and the wait reads
// the agent's status. On herdr-core it becomes the final-report notice.
func applyLastCall(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	tui, err := deps.tui(c)
	if err != nil {
		return err
	}
	target := runtime.Target{Name: d.Name}
	got, err := tui.Inspect(ctx, target)
	if err != nil || !got.Exists {
		return writeRow(d, AgentRow{Name: d.Name, Status: StatusNotReached})
	}
	row := AgentRow{Name: d.Name, Pane: got.Pane, Status: got.Observation.RawProgram}
	if _, err := tui.Deliver(ctx, target, runtime.Delivery{Text: lastCallPrompt, Tag: "last-call"}); err != nil {
		row.Status = StatusNotReached
		return writeRow(d, row)
	}
	want := []runtime.Program{runtime.ProgramIdle, runtime.ProgramDone}
	if _, err := tui.WaitProgram(ctx, target, want, time.Duration(d.Control.GraceMS)*time.Millisecond); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = tui.SendKey(ctx, got.Pane, "esc") // best effort; the pane closes next
	}
	return writeRow(d, row)
}

func applyClosePane(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	pane, err := labelledPane(ctx, c, d.Workspace, d.Label)
	if err != nil {
		return err
	}
	if pane != "" {
		if _, err := deps.provider(c).Stop(ctx, runtime.Target{Pane: pane}); err != nil {
			return err
		}
	}
	return removeFile(serviceStamp(d))
}

func probeClosePane(ctx context.Context, _ Deps, c *herdr.Client, d stepData) (ops.Verdict, string, error) {
	pane, err := labelledPane(ctx, c, d.Workspace, d.Label)
	if err != nil {
		return ops.Unknown, "", err
	}
	if pane == "" {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// applyRetire puts the approved registry back to its empty state. The names
// stay usable, so that a resume can bring the same pair back.
func applyRetire(_ context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	path := filepath.Join(d.Inbox, "peers.approved")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("{}\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func probeRetire(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, approved, err := PairState(d.Inbox); err != nil {
		return ops.Unknown, "", err
	} else if approved {
		return ops.Absent, "", nil
	}
	return ops.Matches, "", nil
}

// applyContinue tells an agent to go on, with what it was doing when stopped.
func applyContinue(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	if _, err := os.Stat(sentStamp(d)); err == nil {
		return nil
	}
	text := "Continue from where you stopped."
	if record, err := ReadControlRecord(d.Folder); err == nil {
		for _, row := range record.Agents {
			if row.Name == d.Name && row.Status != StatusNotReached {
				text += " When the route was " + map[Verb]string{VerbPause: "paused", VerbStop: "stopped"}[record.Verb] + " you were " + row.Status + "."
			}
		}
	}
	if d.Control.Note != "" {
		text += " " + d.Control.Note
	}
	if _, err := deps.provider(c).Deliver(ctx, runtime.Target{Name: d.Name}, runtime.Delivery{Text: text, Tag: d.Tag}); err != nil {
		return err
	}
	return writeFileNew(sentStamp(d), nil)
}

// RepoOf returns the repository of a journaled start.
func RepoOf(op *ops.Operation) string {
	if len(op.Steps) == 0 {
		return ""
	}
	d, err := decode(op.Steps[0])
	if err != nil {
		return ""
	}
	return d.Repo
}

// RunningAgents names the route's agents their backend still knows, in start
// order.
func RunningAgents(ctx context.Context, start *ops.Operation) ([]string, error) {
	var running []string
	for _, st := range start.Steps {
		if st.Effect != effectAgent {
			continue
		}
		d, err := decode(st)
		if err != nil {
			return nil, err
		}
		rt, err := (Deps{}).open(d)
		if err != nil {
			return nil, fmt.Errorf("ask about %s: %w", d.Name, err)
		}
		got, err := rt.Inspect(ctx, runtime.Target{Name: d.Name})
		if err != nil {
			return nil, fmt.Errorf("ask herdr about %s: %w", d.Name, err)
		}
		if got.Exists {
			running = append(running, d.Name)
		}
	}
	return running, nil
}

// ResolveHalted puts a resume that stopped at the pair approval back to run,
// since a fresh call brings a fresh attestation.
func ResolveHalted(e *ops.Engine, op *ops.Operation) error {
	if op.State != ops.OpStopped && op.State != ops.OpConflict {
		return nil
	}
	for _, st := range op.Steps {
		if st.Effect == effectApprove && st.State == ops.StepIntended {
			return e.Resolve(op.ID, st.Ordinal, ops.ResolveRerun)
		}
	}
	return nil
}
