package route

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/profile"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// The steps of a launch on herdr-core (spec section 8). Each is one step of
// the up operation: intended before its effect, done once the effect is
// confirmed.
const (
	effectCorePreflight = "core.preflight"
	effectCoreSpawn     = "core.spawn"
	effectCoreRegister  = "core.register"
	effectCoreVisible   = "core.visibility"
	effectCoreReady     = "core.ready"
	effectCoreAttach    = "core.attach"
	effectCoreEnroll    = "core.enroll"
	effectCorePause     = "core.pause"
	effectCoreStop      = "core.stop"
)

// CoreLaunch is the herdr-core half of an UpRequest, filled by the daemon.
type CoreLaunch struct {
	// Socket is core's socket.
	Socket string
	// Owner is the installation id the labels carry.
	Owner string
	// Wrapper is the herdsman program the core terminal runs.
	Wrapper string
	// LaunchSocket is the daemon's launch socket.
	LaunchSocket string
	// NoticeSocket is the daemon's worker socket, which the harness reaches
	// its notices through.
	NoticeSocket string
	// Login are the login values and PATH a launch takes, and LoginFrom
	// says where they came from.
	Login     map[string]string
	LoginFrom string
	Timeouts  core.Timeouts
}

// coreData is what one core step carries.
type coreData struct {
	Launch   core.Launch   `json:"launch"`
	Timeouts core.Timeouts `json:"timeouts"`
	// Line is the start line a person submits, when the adapter needs one.
	Line string `json:"line,omitempty"`
	// LineHoldsPrompt keeps the line out of the step's reason.
	LineHoldsPrompt bool `json:"line_holds_prompt,omitempty"`
	// Intents are the pair's launch intents, for the enroll step.
	Intents []string `json:"intents,omitempty"`
	// Brief is the first brief a brief step issues as a notice.
	Brief string `json:"brief,omitempty"`
	// Reissue: the brief step of a resume after a stop first carries on what
	// the agent's predecessor left unhandled.
	Reissue bool `json:"reissue,omitempty"`
}

// planCore plans a route's start on herdr-core: the route folder, the inbox,
// and for each coordinator the launch transaction's steps. No pane, prelude
// line or prompt is typed: a start line a harness needs is a wait for a
// person, and so is a placement the app has never verified.
func planCore(q UpRequest, p *UpPlan, coordinators []compiled, text *strings.Builder) ([]ops.StepSpec, error) {
	if q.Core == nil {
		return nil, runtime.Unavailable(runtime.BackendCore, "in this daemon: no launch socket is configured")
	}
	if profile.ShapeFor(q.Policy) == profile.ShapeOwnClone {
		return nil, fmt.Errorf("the restricted profile does not launch on herdr-core: no restricted worker is qualified on core; set backend = %q in [runtime]", runtime.BackendTUI)
	}
	if q.PairManual {
		return nil, fmt.Errorf("--pair manual is not available on herdr-core yet; run up from a terminal with --pair auto, or set backend = %q", runtime.BackendTUI)
	}
	for _, c := range coordinators {
		a, err := core.AdapterFor(c.kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.role, err)
		}
		if err := a.CheckArgs(c.spec.Command[1:]); err != nil {
			return nil, fmt.Errorf("%s: %w", c.role, err)
		}
	}
	stepsDir, err := launch.StepsDir(p.Folder, StepsOperation)
	if err != nil {
		return nil, err
	}
	base := stepData{Key: p.Key, Route: q.Route, Repo: q.Repo, Folder: p.Folder, Workspace: p.Workspace, Socket: q.Core.Socket, Executable: q.Executable, Backend: runtime.BackendCore}
	var steps []ops.StepSpec
	add := func(name, effect, key string, retry ops.Retry, d stepData) {
		b, err := json.Marshal(d)
		if err != nil {
			panic(err) // strings, numbers and maps only
		}
		steps = append(steps, ops.StepSpec{Name: name, Effect: effect, Key: key, Spec: string(b), Retry: retry})
	}
	with := func(edit func(*stepData)) stepData {
		d := base
		edit(&d)
		return d
	}
	fmt.Fprintf(text, "backend: %s\n", runtime.BackendCore)
	add("create the route folder", effectFolder, "folder:"+p.Folder, ops.RetryIdempotent, with(func(d *stepData) {
		d.Prompt, d.Config, d.Adjutant, d.Orchestrator = q.Prompt, q.ConfigPath, p.AdjutantName, p.OrchestratorName
	}))
	channelEnv := map[string]string{}
	if !q.NoAdjutant {
		base.Inbox = p.Inbox
		add("create the private inbox", effectInbox, "inbox:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) { d.PolicyFiles = q.PolicyFiles }))
		channelEnv["HERDSMAN_INBOX"] = p.Inbox
		channelEnv["HERDSMAN_PREFLIGHT"] = filepath.Join(p.Inbox, "setup.json")
		channelEnv["HERDSMAN_ORCHESTRATOR"] = p.OrchestratorName
		channelEnv["HERDSMAN_ADJUTANT"] = p.AdjutantName
	}
	routeID := core.UUIDOf(q.Core.Owner, q.Repo, q.Route)
	space := core.OpaqueID(q.Core.Owner, q.Repo, q.Route, "space")
	n := 0
	var intents []string
	var briefs []func()
	for _, c := range coordinators {
		exec := runtime.NewExecutionID()
		intent := "up:" + q.Repo + ":" + q.Route + ":" + c.name + ":" + exec
		intents = append(intents, intent)
		d := launch.CoreDescriptor{
			Intent: intent, Execution: exec, Kind: c.kind, Harness: append([]string(nil), c.spec.Command...), StepsDir: stepsDir,
			TimeoutMS: c.spec.Timeout().Milliseconds(), Cwd: coreCwd(c, q), Env: mergeEnv(channelEnv, c.spec.Env),
			Login: q.Core.Login, LoginFrom: q.Core.LoginFrom, SecretRefs: q.SecretRefs, NoticeSocket: q.Core.NoticeSocket,
		}
		for _, step := range c.spec.Prelude {
			n++
			d.Prelude = append(d.Prelude, launch.NumberedStep{N: n, Step: step})
		}
		l := core.Launch{
			Intent: intent, Execution: exec, Owner: q.Core.Owner, RouteID: routeID, AgentID: core.UUIDOf(q.Core.Owner, q.Repo, q.Route, c.name),
			Repo: q.Repo, RouteName: q.Route, AgentName: c.name, Role: c.role, Kind: c.kind,
			Placement: herdrapp.PlacementIDs{Space: space, Tab: core.OpaqueID("tab", exec)}, Name: core.CoreName(q.Route, c.role, exec),
			Wrapper: []string{q.Core.Wrapper, "launch", "--socket", q.Core.LaunchSocket, "--intent", intent}, Cwd: d.Cwd, Descriptor: d,
		}
		data := func(edit func(*coreData)) stepData {
			cd := coreData{Launch: l, Timeouts: q.Core.Timeouts}
			if edit != nil {
				edit(&cd)
			}
			return with(func(sd *stepData) { sd.Name, sd.Kind, sd.Label, sd.Core = c.name, c.kind, c.role, &cd })
		}
		fmt.Fprintf(text, "\n%s: %s (kind %s)\ncommand: %s\nexecution: %s\n", c.role, c.name, c.kind, c.line, exec)
		for _, s := range d.Prelude {
			fmt.Fprintf(text, "step %d: %s (run by the core wrapper)\n", s.N, s.Step.Command.String())
		}
		add("check the launch of "+c.name+" against core", effectCorePreflight, "core-preflight:"+c.name, ops.RetryIdempotent, data(nil))
		add("start "+c.name+" in a core terminal", effectCoreSpawn, "core-spawn:"+c.name, ops.RetryNever, data(nil))
		add("register "+c.name, effectCoreRegister, "core-register:"+c.name, ops.RetryNever, data(nil))
		add("show "+c.name+" in the app", effectCoreVisible, "core-visibility:"+c.name, ops.RetryIdempotent, data(nil))
		add("wait until "+c.name+" is ready", effectCoreReady, "core-ready:"+c.name, ops.RetryIdempotent, data(nil))
		// The start line is the adapter's fixed line, with no task content:
		// a person submits it (no start-line argument is added to a launch
		// command while that decision is the owner's). The role command
		// that the TUI typed is the first brief, a notice issued once the
		// pair is enrolled.
		a, _ := core.AdapterFor(c.kind)
		fmt.Fprintf(text, "start line (version %d): submitted by a person in the app\n", a.StartLineVersion)
		if a.Attended {
			fmt.Fprintf(text, "attended: %s is woken by a person on herdr-core; each notice for it waits for the line %q in its pane\n", c.kind, notice.AttendedLine)
		}
		add("have a person submit "+c.name+"'s start line", effectCoreAttach, "core-attach:"+c.name, ops.RetryNever, data(func(cd *coreData) {
			cd.Line = a.StartLine
		}))
		role, _ := startLine(q, p, c)
		brief := data(func(cd *coreData) { cd.Brief = briefText(q.Route, role) })
		name := c.name
		briefs = append(briefs, func() {
			add("issue "+name+"'s first brief as a notice", effectCoreBrief, "core-brief:"+name, ops.RetryIdempotent, brief)
		})
	}
	add("enroll the agents", effectCoreEnroll, "core-enroll:"+p.Key, ops.RetryIdempotent, with(func(d *stepData) {
		d.Core = &coreData{Intents: intents}
	}))
	for _, f := range briefs {
		f()
	}
	return steps, nil
}

func coreCwd(c compiled, q UpRequest) string {
	if c.spec.Cwd != "" {
		return c.spec.Cwd
	}
	return q.Repo
}

// startLine is the line that starts a coordinator's work, as the TUI typed it.
// On core a person submits it; no automation does.
func startLine(q UpRequest, p *UpPlan, c compiled) (string, bool) {
	switch {
	case c.role == roleAdjutant:
		return skillPrefix(c.kind) + "herdsman adjutant --name " + p.AdjutantName + " --orchestrator " + p.OrchestratorName + " --inbox " + p.Inbox, false
	case q.NoAdjutant:
		return skillPrefix(c.kind) + "herdsman run " + shellQuote(q.Prompt), true
	}
	return skillPrefix(c.kind) + "herdsman orchestrator --adjutant " + p.AdjutantName + " --name " + p.OrchestratorName + " --inbox " + p.Inbox, false
}

// coreProvider is the step's provider as the core provider.
func coreProvider(deps Deps) (*core.Provider, error) {
	cp, ok := deps.rt.(*core.Provider)
	if !ok {
		return nil, fmt.Errorf("the step runs on herdr-core and its provider is %s", deps.rt.Backend())
	}
	return cp, nil
}

func probeOf(p core.Probe, detail string, err error) (ops.Verdict, string, error) {
	return [...]ops.Verdict{ops.Unknown, ops.Absent, ops.Matches, ops.Different}[p], detail, err
}

// pinnedCore is the core id the route's executions were recorded on.
func pinnedCore(ctx context.Context, cp *core.Provider, d stepData) (string, error) {
	execs, err := cp.Records.List(ctx, d.Repo, d.Route)
	if err != nil {
		return "", err
	}
	for _, e := range execs {
		if e.Backend == runtime.BackendCore && e.EndpointID != "" {
			return e.EndpointID, nil
		}
	}
	return "", nil
}

func applyCorePreflight(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	expect, err := pinnedCore(ctx, cp, d)
	if err != nil {
		return err
	}
	c, err := cp.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	l := d.Core.Launch
	if _, err = core.Preflight(ctx, c, core.PreflightInput{Kind: l.Kind, Argv: l.Descriptor.Harness, Roots: slices.Compact([]string{d.Repo, l.Cwd}), ExpectCore: expect}); err != nil {
		return err
	}
	return waitBudget(ctx, cp, l.Intent)
}

func probeAbsent(context.Context, Deps, *herdr.Client, stepData) (ops.Verdict, string, error) {
	return ops.Absent, "", nil
}

// resolvedLaunch resolves the harness program as core sees it, right before
// the launch is armed: the daemon's PATH is not core's.
func resolvedLaunch(ctx context.Context, cp *core.Provider, l core.Launch) (core.Launch, error) {
	if filepath.IsAbs(l.Descriptor.Harness[0]) {
		return l, nil
	}
	c, err := cp.Dial(ctx)
	if err != nil {
		return l, err
	}
	defer c.Close()
	program, err := core.ResolveProgram(ctx, c, l.Descriptor.Harness[0])
	if err != nil {
		return l, err
	}
	l.Descriptor.Harness = append([]string{program}, l.Descriptor.Harness[1:]...)
	return l, nil
}

func applyCoreSpawn(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	_, err = cp.SpawnStep(ctx, d.Core.Launch)
	return err
}

func probeCoreSpawn(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	return probeOf(cp.ProbeSpawn(ctx, d.Core.Launch))
}

func applyCoreRegister(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	l, err := resolvedLaunch(ctx, cp, d.Core.Launch)
	if err != nil {
		return err
	}
	_, err = cp.RegisterStep(ctx, l, d.Core.Timeouts)
	return err
}

func probeCoreRegister(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	return probeOf(cp.ProbeRegister(ctx, d.Core.Launch))
}

func applyCoreVisible(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	_, err = cp.VisibilityStep(ctx, d.Core.Launch, d.Core.Timeouts)
	return err
}

func probeCoreVisible(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	v, detail, err := cp.ProbeVisibility(ctx, d.Core.Launch)
	if v == core.ProbeAbsent {
		// A wait for a person stops the start until up --continue.
		return ops.Unknown, detail, err
	}
	return probeOf(v, detail, err)
}

func applyCoreReady(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	_, err = cp.ReadyStep(ctx, d.Core.Launch, d.Core.Timeouts)
	return err
}

func probeCoreReady(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	v, detail, err := cp.ProbeReady(ctx, d.Core.Launch)
	if v == core.ProbeAbsent {
		return ops.Unknown, detail, err
	}
	return probeOf(v, detail, err)
}

// lineFile keeps a start line that holds the prompt out of every reason and
// log: the person reads it from a private file.
func lineFile(d stepData) string {
	return filepath.Join(d.Folder, ".steps", "start-line-"+strings.ReplaceAll(d.Name, "/", "-"))
}

// applyCoreAttach is the first of up's two waits for a person on core: the
// harness's start line, which no automation types. A person submits it in
// the agent's pane and runs up again with --continue, which settles the step.
func applyCoreAttach(_ context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	where := fmt.Sprintf("in %s's pane in the herdr app (space %s, tab %s)", d.Name, d.Route, d.Label)
	if d.Core.LineHoldsPrompt {
		if err := writeFileNew(lineFile(d), []byte(d.Core.Line+"\n")); err != nil {
			return err
		}
		return &core.Error{Code: core.CodeNeedsAttention, Msg: fmt.Sprintf("submit the start line in %s %s; then run: herdsman up %s --continue", lineFile(d), where, d.Route)}
	}
	return &core.Error{Code: core.CodeNeedsAttention, Msg: fmt.Sprintf("submit this start line %s: %s ; then run: herdsman up %s --continue", where, d.Core.Line, d.Route)}
}

func probeCoreAttach(context.Context, Deps, *herdr.Client, stepData) (ops.Verdict, string, error) {
	return ops.Unknown, "only a person can say that the start line was submitted", nil
}

// pairRecord is the enrolled core pair, kept in the route folder.
type pairRecord struct {
	Executions []string `json:"executions"`
	Intents    []string `json:"intents"`
	Checksum   string   `json:"checksum"`
	By         string   `json:"by"`
	PID        int      `json:"pid"`
	Started    string   `json:"started,omitempty"`
	TTY        string   `json:"tty,omitempty"`
	Time       string   `json:"time"`
}

func pairPath(d stepData) string { return filepath.Join(d.Folder, ".steps", "core-pair.json") }

func pairChecksum(execs []runtime.Execution) string {
	var lines []string
	for _, e := range execs {
		lines = append(lines, e.Role+"="+e.ID+"@"+e.EndpointID)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// applyCoreEnroll binds the current execution pair under the approval rules
// of ADR 0025: every execution passes the dispatch gate's launch half, and the
// caller of up is a person's terminal, not an agent and not a core terminal
// started with a command.
func applyCoreEnroll(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	execs, err := cp.Enrollable(ctx, d.Core.Intents)
	if err != nil {
		return err
	}
	sum := pairChecksum(execs)
	// The approved pair is what the notice gates read: each execution under
	// the policy it was launched with, and the route folder for records.
	approve := func() error {
		if cp.Notices == nil {
			return nil
		}
		for _, e := range execs {
			if err := cp.Notices.Approve(e, d.Folder); err != nil {
				return err
			}
		}
		return nil
	}
	if b, err := os.ReadFile(pairPath(d)); err == nil {
		var prev pairRecord
		if json.Unmarshal(b, &prev) == nil && prev.Checksum == sum {
			return approve()
		}
	}
	caller, ok := deps.Callers.Get(d.Key)
	if !ok {
		return fmt.Errorf("the pair is left pending: no caller is known to attest; run herdsman up %s --continue from a terminal", d.Route)
	}
	if caller.Execution != "" {
		return fmt.Errorf("the pair is left pending: up was run by execution %s, an agent; run herdsman up %s --continue from a terminal", caller.Execution, d.Route)
	}
	if err := verifyCaller(caller); err != nil {
		return fmt.Errorf("the pair is left pending: %v; run herdsman up %s --continue from a terminal", err, d.Route)
	}
	rec := pairRecord{Intents: d.Core.Intents, Checksum: sum, By: "herdsman up", PID: caller.PID, TTY: caller.TTY, Time: time.Now().UTC().Format(time.RFC3339)}
	for _, e := range execs {
		rec.Executions = append(rec.Executions, e.ID)
	}
	if !caller.Started.IsZero() {
		rec.Started = caller.Started.UTC().Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pairPath(d)), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(pairPath(d), b, 0o600); err != nil {
		return err
	}
	return approve()
}

func probeCoreEnroll(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	b, err := os.ReadFile(pairPath(d))
	if errors.Is(err, os.ErrNotExist) {
		return ops.Unknown, "the pair is not enrolled", nil
	}
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	var rec pairRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return ops.Unknown, err.Error(), nil
	}
	execs, err := cp.Enrollable(ctx, d.Core.Intents)
	if err != nil || pairChecksum(execs) != rec.Checksum {
		return ops.Unknown, "the enrolled pair is not the current one", nil
	}
	return ops.Matches, "", nil
}

// coreLaunches lists the core launches a journaled start reserved, in start
// order, one per agent.
func coreLaunches(op *ops.Operation) []core.Launch {
	var out []core.Launch
	for _, st := range op.Steps {
		if st.Effect != effectCoreSpawn {
			continue
		}
		if d, err := decode(st); err == nil && d.Core != nil {
			out = append(out, d.Core.Launch)
		}
	}
	return out
}

// liveRecords are the agent's executions the daemon still wants running, on
// every generation of the route: a resume after a stop launches new ones.
func liveRecords(ctx context.Context, cp *core.Provider, d stepData) ([]runtime.Execution, error) {
	all, err := cp.Records.List(ctx, d.Repo, d.Route)
	if err != nil {
		return nil, err
	}
	var out []runtime.Execution
	for _, e := range all {
		if e.Backend == runtime.BackendCore && e.Agent == d.Name && e.Desired == runtime.DesiredRunning && e.Evidence.PID != 0 {
			out = append(out, e)
		}
	}
	return out, nil
}

// applyCorePause pauses an agent through the provider's rules (spec section
// 12): at most one interrupt per pause, none to an agent at rest, none to an
// attended harness unless a person asked, never a kill.
func applyCorePause(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return writeRow(d, AgentRow{Name: d.Name, Status: StatusNotReached})
	}
	c, err := cp.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	procs := cp.Procs
	if procs == nil {
		procs = core.SystemProcs{}
	}
	var outcomes []string
	for _, rec := range recs {
		// The daemon's record of a turn, not the status alone (spec section
		// 12): an execution with an open claim is in a turn; one that only
		// waits is at rest and gets no key.
		inTurn := false
		if cp.Notices != nil {
			_, open, err := cp.Notices.OpenClaim(rec.ID)
			if err != nil {
				return err
			}
			inTurn = open
		}
		res, err := core.Pause(ctx, c, core.PauseInput{Pause: d.Op, Execution: rec, InTurn: inTurn, Ledger: core.FilePauseLedger{Dir: controlDir(d.Folder)}, Procs: procs})
		if err != nil {
			return err
		}
		outcome := res.Outcome
		if res.Outcome == core.PausePending && cp.Notices != nil {
			cp.Notices.Emit(notice.Event{Kind: core.PausePending, Repo: rec.Repo, Route: rec.Route, Execution: rec.ID,
				Detail: "the turn under way ends by itself; nothing new can be claimed meanwhile"})
		}
		if draining, err := cp.Submits.Draining(rec.ID); err != nil {
			return err
		} else if draining != "" {
			outcome += ", " + draining
		}
		outcomes = append(outcomes, outcome)
	}
	return writeRow(d, AgentRow{Name: d.Name, Pane: recs[0].TerminalAddress, Status: strings.Join(outcomes, ", ")})
}

// applyCoreStop stops each running execution of an agent by its recorded
// processes; a recovery shell at its address is left for a person.
func applyCoreStop(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return err
	}
	var statuses []string
	for _, rec := range recs {
		report, why := "", ""
		if cp.Notices != nil {
			report, why = reportOutcome(cp.Notices, d, rec)
			// Interrupt what is still in a turn, once (spec section 12).
			_, open, err := cp.Notices.OpenClaim(rec.ID)
			if err != nil {
				return err
			}
			final, _, _ := cp.Notices.ByKey(finalKey(d.Op, rec.ID))
			if report != notice.ReportReported && (open || final.Open()) {
				if err := interruptOnce(ctx, cp, d, rec); err != nil {
					return err
				}
			}
		}
		status := "stopped"
		got, err := cp.Stop(ctx, runtime.Target{ExecutionID: rec.ID})
		if err != nil {
			return err
		}
		if got.Already {
			status = "already ended; a recovery shell at " + rec.TerminalAddress + " is left for a person"
		}
		if cp.Notices != nil {
			if _, err := cp.Notices.ReturnExecution(rec.ID, "stopped"); err != nil {
				return err
			}
			cp.Notices.Emit(notice.Event{Kind: "report_outcome", Repo: rec.Repo, Route: rec.Route, Execution: rec.ID, Outcome: report, Reason: why})
			status = report + " (" + why + "); " + status
		}
		statuses = append(statuses, status)
	}
	if len(recs) == 0 {
		statuses = append(statuses, notice.ReportNotReached)
	}
	return writeRow(d, AgentRow{Name: d.Name, Status: strings.Join(statuses, "; ")})
}

// interruptOnce sends a stop's one interrupt to an execution still in a turn;
// the stop's id keys the ledger, so a retried step sends none.
func interruptOnce(ctx context.Context, cp *core.Provider, d stepData, rec runtime.Execution) error {
	c, err := cp.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	procs := cp.Procs
	if procs == nil {
		procs = core.SystemProcs{}
	}
	_, err = core.Pause(ctx, c, core.PauseInput{Pause: d.Op, Execution: rec, InTurn: true, PersonAsked: true, Ledger: core.FilePauseLedger{Dir: controlDir(d.Folder)}, Procs: procs})
	return err
}

// planCoreControl plans a verb on a herdr-core route. A pause and a stop act
// through the provider; a resume from a pause only records it, since nothing
// is typed into a harness and a person continues an interrupted turn; a
// resume from a stop launches every agent again under fresh reservations.
func (b *controlBuilder) planCoreControl() error {
	launches := coreLaunches(b.q.Start)
	attach := map[string]coreData{}
	brief := map[string]string{}
	var enroll *ops.Step
	for _, st := range b.q.Start.Steps {
		d, err := decode(st)
		if err != nil {
			return fmt.Errorf("the journaled start: %w", err)
		}
		switch st.Effect {
		case effectCoreAttach:
			attach[d.Name] = *d.Core
		case effectCoreBrief:
			brief[d.Name] = d.Core.Brief
		case effectCoreEnroll:
			enroll = &st
		}
	}
	for _, l := range launches {
		b.agents = append(b.agents, l.AgentName)
	}
	switch b.q.Verb {
	case VerbPause:
		// The hold is persisted before any interrupt.
		b.add("hold the route's notices", effectCoreHold, "core-hold", b.base)
		for _, name := range b.agents {
			b.add("pause "+name, effectCorePause, "core-pause:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		if b.q.Teardown {
			b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
		}
		b.add("record the pause", effectRecord, "record", b.base)
	case VerbStop:
		b.add("put the route in the stopping phase", effectCoreStopping, "core-stopping", b.base)
		for _, name := range b.agents {
			b.add("ask "+name+" for its report", effectCoreReport, "core-report:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		b.add("wait for the reports", effectCoreReports, "core-reports", b.base)
		for _, name := range b.agents {
			b.add("stop "+name, effectCoreStop, "core-stop:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		if b.q.Teardown {
			b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
		}
		b.add("record the stop", effectRecord, "record:stop", b.with(func(d *stepData) {
			d.Control = b.control()
			d.Control.Verb = VerbStop
		}))
	case VerbResume:
		if b.q.From != ControlStopped {
			b.add("lift the route's hold", effectCoreUnhold, "core-unhold", b.base)
			for _, name := range b.agents {
				b.add("restore "+name, effectCoreResume, "core-resume:"+name, b.with(func(d *stepData) { d.Name = name }))
			}
		}
		if b.q.From == ControlStopped {
			b.add("end the route's stopping phase", effectCoreRestart, "core-restart#"+strconv.Itoa(b.q.Generation), b.base)
			var intents []string
			var briefs []stepData
			for _, l := range launches {
				fresh := remapLaunch(l)
				intents = append(intents, fresh.Intent)
				cd := coreData{Launch: fresh}
				if a, ok := attach[l.AgentName]; ok {
					cd.Timeouts = a.Timeouts
				}
				step := func(edit func(*coreData)) stepData {
					c := cd
					if edit != nil {
						edit(&c)
					}
					return b.with(func(d *stepData) { d.Name, d.Kind, d.Label, d.Core = l.AgentName, l.Kind, l.Role, &c })
				}
				gen := strconv.Itoa(b.q.Generation)
				b.addOnce("start "+l.AgentName+" in a core terminal", effectCoreSpawn, "core-spawn:"+l.AgentName+"#"+gen, step(nil))
				b.addOnce("register "+l.AgentName, effectCoreRegister, "core-register:"+l.AgentName+"#"+gen, step(nil))
				b.add("show "+l.AgentName+" in the app", effectCoreVisible, "core-visibility:"+l.AgentName+"#"+gen, step(nil))
				b.add("wait until "+l.AgentName+" is ready", effectCoreReady, "core-ready:"+l.AgentName+"#"+gen, step(nil))
				if a, ok := attach[l.AgentName]; ok {
					b.addOnce("have a person submit "+l.AgentName+"'s start line", effectCoreAttach, "core-attach:"+l.AgentName+"#"+gen, step(func(c *coreData) {
						c.Line, c.LineHoldsPrompt = a.Line, a.LineHoldsPrompt
					}))
				}
				if br, ok := brief[l.AgentName]; ok {
					briefs = append(briefs, step(func(c *coreData) { c.Brief, c.Reissue = br, true }))
				}
			}
			if enroll != nil {
				b.add("enroll the agents", effectCoreEnroll, "core-enroll#"+strconv.Itoa(b.q.Generation), b.with(func(d *stepData) {
					d.Core = &coreData{Intents: intents}
				}))
			}
			for _, d := range briefs {
				b.add("issue "+d.Name+"'s first brief as a notice", effectCoreBrief, "core-brief:"+d.Name+"#"+strconv.Itoa(b.q.Generation), d)
			}
		}
		b.add("record the resume", effectRecord, "record", b.base)
	case VerbDelete:
		return runtime.Unavailable(runtime.BackendCore, "for delete yet: stop the route, then remove its folder")
	default:
		return fmt.Errorf("unknown verb %q", b.q.Verb)
	}
	return nil
}

// addOnce adds a step that may not run twice.
func (b *controlBuilder) addOnce(name, effectName, key string, d stepData) {
	b.add(name, effectName, key, d)
	b.steps[len(b.steps)-1].Retry = ops.RetryNever
}

// coreEffects are the effects of a launch and its control on herdr-core.
func coreEffects(deps Deps) map[string]ops.Effect {
	return map[string]ops.Effect{
		effectCorePreflight: effect{deps, applyCorePreflight, probeAbsent},
		effectCoreSpawn:     effect{deps, applyCoreSpawn, probeCoreSpawn},
		effectCoreRegister:  effect{deps, applyCoreRegister, probeCoreRegister},
		effectCoreVisible:   effect{deps, applyCoreVisible, probeCoreVisible},
		effectCoreReady:     effect{deps, applyCoreReady, probeCoreReady},
		effectCoreAttach:    effect{deps, applyCoreAttach, probeCoreAttach},
		effectCoreEnroll:    effect{deps, applyCoreEnroll, probeCoreEnroll},
		effectCorePause:     effect{deps, applyCorePause, probeControlRow},
		effectCoreStop:      effect{deps, applyCoreStop, probeControlRow},
		effectCoreBrief:     effect{deps, applyCoreBrief, probeCoreBrief},
		effectCoreHold:      effect{deps, applyCoreHold, probeCoreHold},
		effectCoreUnhold:    effect{deps, applyCoreUnhold, probeCoreUnhold},
		effectCoreResume:    effect{deps, applyCoreResume, probeControlRow},
		effectCoreStopping:  effect{deps, applyCoreStopping, probeCoreStopping},
		effectCoreReport:    effect{deps, applyCoreReport, probeReportRow},
		effectCoreReports:   effect{deps, applyCoreReports, probeReports},
		effectCoreRestart:   effect{deps, applyCoreRestart, probeCoreRestart},
	}
}

// probeControlRow: a control step is done once its agent's row is written.
func probeControlRow(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(rowPath(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}

// continueCore settles the person waits of a halted core start: a start line
// a person says they submitted is done, and the steps that wait on the app,
// the harness or a caller run again.
func continueCore(e *ops.Engine, op *ops.Operation) error {
	for _, st := range op.Steps {
		if st.State != ops.StepIntended && st.State != ops.StepConflict {
			continue
		}
		switch st.Effect {
		case effectCoreAttach:
			return e.Resolve(op.ID, st.Ordinal, ops.ResolveDone)
		case effectCoreVisible, effectCoreReady, effectCoreEnroll:
			return e.Resolve(op.ID, st.Ordinal, ops.ResolveRerun)
		}
	}
	return nil
}

// remapLaunch reserves a launch again for a resume after a stop: a new
// execution, intent, tab and terminal name, the same agent and route.
func remapLaunch(l core.Launch) core.Launch {
	exec := runtime.NewExecutionID()
	intent := strings.TrimSuffix(l.Intent, l.Execution) + exec
	l.Execution, l.Intent = exec, intent
	l.Placement.Tab = core.OpaqueID("tab", exec)
	l.Name = core.CoreName(l.RouteName, l.Role, exec)
	for i, w := range l.Wrapper {
		if w == "--intent" && i+1 < len(l.Wrapper) {
			l.Wrapper = append(append([]string(nil), l.Wrapper[:i+1]...), append([]string{intent}, l.Wrapper[i+2:]...)...)
			break
		}
	}
	l.Descriptor.Intent, l.Descriptor.Execution = intent, exec
	return l
}
