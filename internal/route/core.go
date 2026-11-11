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
	"github.com/arafathusayn/herdsman/internal/runtime/coreconfig"
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
	effectCoreService   = "core.service"
	effectCoreQuiet     = "core.service-stop"
	effectCoreEnded     = "core.ended"
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
	// Endpoint is the route's canonical connection to core, persisted with
	// each launch step; Socket is its socket.
	Endpoint coreconfig.Endpoint
	// App is the app the route's agents are shown in ([runtime.app]).
	App coreconfig.App
}

// coreData is what one core step carries.
type coreData struct {
	Launch   core.Launch   `json:"launch"`
	Timeouts core.Timeouts `json:"timeouts"`
	// Endpoint and App are the route's persisted connection to core and the
	// app it shows its agents in; a step journaled before they existed has
	// none and reads as the step's socket and the daemon's app.
	Endpoint *coreconfig.Endpoint `json:"endpoint,omitempty"`
	App      *coreconfig.App      `json:"app,omitempty"`
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
	// Service is the supporting service a service step registers.
	Service *ServiceSpec `json:"service,omitempty"`
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
	fmt.Fprintf(text, "core: %s\n", q.Core.Socket)
	add("create the route folder", effectFolder, "folder:"+p.Folder, ops.RetryIdempotent, with(func(d *stepData) {
		d.Prompt, d.Config, d.Adjutant, d.Orchestrator = q.Prompt, q.ConfigPath, p.AdjutantName, p.OrchestratorName
	}))
	channelEnv := map[string]string{}
	if !q.NoAdjutant {
		base.Inbox = p.Inbox
		// The pair's channel is the daemon's: the inbox is daemon-owned,
		// and each party is the execution its process belongs to.
		add("register the route's daemon channel", effectCoreChannel, "core-channel:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) { d.PolicyFiles = q.PolicyFiles }))
		channelEnv["HERDSMAN_INBOX"] = p.Inbox
		channelEnv["HERDSMAN_PREFLIGHT"] = filepath.Join(p.Inbox, "setup.json")
		channelEnv["HERDSMAN_ORCHESTRATOR"] = p.OrchestratorName
		channelEnv["HERDSMAN_ADJUTANT"] = p.AdjutantName
		// No HERDSMAN_ROUTE: the wrapper refuses it as another caller's
		// scope. The message verbs find the route by its daemon-owned
		// inbox in the daemon's route registry.
		fmt.Fprintf(text, "channel: the daemon's, callers identified by their execution\n")
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
			TimeoutMS: c.spec.Timeout().Milliseconds(), Cwd: coreCwd(c, q), Env: mergeEnv(channelEnv, partyEnv(q, c), c.spec.Env),
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
			endpoint, app := q.Core.Endpoint, q.Core.App
			if endpoint.Socket == "" {
				endpoint.Socket = q.Core.Socket
			}
			cd := coreData{Launch: l, Timeouts: q.Core.Timeouts, Endpoint: &endpoint, App: &app}
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
		writePersonSteps(text, a)
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
	if !q.NoAdjutant {
		add("seed the handshake", effectCoreHandshake, "core-handshake:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) {
			d.Adjutant, d.Orchestrator = p.AdjutantName, p.OrchestratorName
		}))
	}
	for _, f := range briefs {
		f()
	}
	// The supporting services, each registered with its one owner, the
	// daemon. Nothing is typed into a pane for them.
	for _, s := range coreServiceSpecs(q, p, coordinators[len(coordinators)-1]) {
		s := s
		fmt.Fprintf(text, "service: %s (run by the daemon)\n", s.Name)
		add(serviceStepName(s), effectCoreService, "core-service:"+s.Name, ops.RetryIdempotent, with(func(d *stepData) {
			d.Core = &coreData{Service: &s}
		}))
	}
	return steps, nil
}

// writePersonSteps names, before the route starts, the steps of an agent's run
// that will need a person (spec section 10, "Attended and unattended"). Each
// raises needs_attention with its reason on the event stream when it comes
// due; the route's other agents go on. They are the adapter's named steps,
// the ones runtime doctor reports for the harness.
func writePersonSteps(text *strings.Builder, a core.Adapter) {
	fmt.Fprintf(text, "needs a person (%s, %s):\n", a.Kind, a.Facts.Level)
	for _, s := range a.NamedPersonSteps() {
		fmt.Fprintf(text, "  %s: %s: %s\n", s.Reason, s.When, s.What)
	}
}

// partyEnv names the coordinator's party in the route's channel: the daemon
// still identifies the caller by its process, and the name only says which
// party it claims to be.
func partyEnv(q UpRequest, c compiled) map[string]string {
	if q.NoAdjutant {
		return nil
	}
	return map[string]string{"HERDSMAN_NAME": c.name}
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
func applyCoreAttach(_ context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	where := fmt.Sprintf("in %s's pane in the herdr app (space %s, tab %s)", d.Name, d.Route, d.Label)
	// The step is due now: it says so on the event stream as well as in
	// the start's answer, so a person who watches the stream hears of it.
	attention := notice.Event{Kind: notice.EventNeedsAttention, Repo: d.Repo, Route: d.Route, Execution: d.Core.Launch.Execution, Reason: core.AttentionStartLine,
		Detail: fmt.Sprintf("a person submits %s's start line %s, then runs herdsman up %s --continue", d.Name, where, d.Route)}
	if !d.Core.LineHoldsPrompt {
		attention.Line = d.Core.Line
	}
	if cp, err := coreProvider(deps); err == nil && cp.Notices != nil {
		cp.Notices.Emit(attention)
	}
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
			if err := approve(); err != nil {
				return err
			}
			// The same pair was attested: its channel is enrolled and
			// approved again only if a crash came between.
			return enrollChannel(ctx, deps, d, execs)
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
	if err := approve(); err != nil {
		return err
	}
	return enrollChannel(ctx, deps, d, execs)
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
	if !channelApproved(deps, d) {
		return ops.Unknown, "the pair's channel is not approved", nil
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
	services := serviceSteps(b.q.Start)
	stopService := func(name string) {
		if _, ok := services[name]; ok {
			b.add("stop the "+name+" service", effectCoreQuiet, "core-service-stop:"+name, b.with(func(d *stepData) { d.Name, d.Label = "service-"+name, name }))
		}
	}
	startService := func(name, key string) {
		if st, ok := services[name]; ok {
			b.add(serviceStepName(*st.Core.Service), effectCoreService, key, b.with(func(d *stepData) { d.Core = st.Core }))
		}
	}
	stop := func() {
		b.add("put the route in the stopping phase", effectCoreStopping, "core-stopping", b.base)
		for _, name := range b.agents {
			b.add("ask "+name+" for its report", effectCoreReport, "core-report:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		b.add("wait for the reports", effectCoreReports, "core-reports", b.base)
		for _, name := range b.agents {
			b.add("stop "+name, effectCoreStop, "core-stop:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		// The route's owned services end with it, by their records.
		for _, name := range []string{ServiceCheckpoint, ServiceCardWatch, ServiceCourier} {
			stopService(name)
		}
		if b.q.Teardown {
			b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
		}
		b.add("record the stop", effectRecord, "record:stop", b.with(func(d *stepData) {
			d.Control = b.control()
			d.Control.Verb = VerbStop
		}))
	}
	switch b.q.Verb {
	case VerbPause:
		// The hold is persisted before any interrupt.
		b.add("hold the route's notices", effectCoreHold, "core-hold", b.base)
		// The checkpointer's prompts stop with the pause, as on the TUI.
		stopService(ServiceCheckpoint)
		for _, name := range b.agents {
			b.add("pause "+name, effectCorePause, "core-pause:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		if b.base.Inbox != "" {
			for _, name := range b.agents {
				b.add("hold the channel for "+name, effectCoreChanHold, "core-hold:"+name, b.with(func(d *stepData) { d.Name = name }))
			}
		}
		if b.q.Teardown {
			b.add("tear down the route's task processes", effectTeardown, "teardown", b.base)
		}
		b.add("record the pause", effectRecord, "record", b.base)
	case VerbStop:
		stop()
	case VerbResume:
		if b.q.From != ControlStopped {
			b.add("lift the route's hold", effectCoreUnhold, "core-unhold", b.base)
			if b.base.Inbox != "" {
				for _, name := range b.agents {
					b.add("lift the channel hold for "+name, effectCoreChanHold, "core-unhold:"+name, b.with(func(d *stepData) { d.Name = name }))
				}
			}
			for _, name := range b.agents {
				b.add("restore "+name, effectCoreResume, "core-resume:"+name, b.with(func(d *stepData) { d.Name = name }))
			}
			startService(ServiceCheckpoint, "core-service:"+ServiceCheckpoint+"#"+strconv.Itoa(b.q.Generation))
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
					cd.Timeouts, cd.Endpoint, cd.App = a.Timeouts, a.Endpoint, a.App
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
			for _, name := range []string{ServiceCheckpoint, ServiceCourier, ServiceCardWatch} {
				startService(name, "core-service:"+name+"#"+strconv.Itoa(b.q.Generation))
			}
		}
		b.add("record the resume", effectRecord, "record", b.base)
	case VerbDelete:
		// Stop through the provider, then archive (PlanControl adds the
		// snapshot and the removal). Core's spaces and tabs are never
		// closed: they may be shared, and the app adapter has no close.
		if b.q.Force {
			stop()
		}
		for _, name := range b.agents {
			b.add("confirm "+name+" ended, through the provider", effectCoreEnded, "core-ended:"+name, b.with(func(d *stepData) { d.Name = name }))
		}
		if !b.q.Force {
			for _, name := range []string{ServiceCheckpoint, ServiceCardWatch, ServiceCourier} {
				stopService(name)
			}
		}
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
		effectCoreService:   effect{deps, applyCoreService, probeCoreService},
		effectCoreQuiet:     effect{deps, applyCoreServiceStop, probeCoreServiceStop},
		effectCoreChannel:   effect{deps, applyCoreChannel, probeCoreChannel},
		effectCoreHandshake: effect{deps, applyCoreHandshake, probeHandshake},
		effectCoreChanHold:  effect{deps, applyCoreChanHold, probeCoreChanHold},
		effectCoreEnded:     effect{deps, applyCoreEnded, probeCoreEnded},
	}
}

// applyCoreEnded ends an agent before a delete archives its route: every
// execution of the agent still desired running is stopped through the
// provider, which never types and never kills a recovery shell. Without
// --force a delete of a route whose agent still runs is refused, so a
// delete never skips the final report a stop asks for.
func applyCoreEnded(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) error {
	cp, err := coreProvider(deps)
	if err != nil {
		return err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if d.Control == nil || !d.Control.Force {
			return fmt.Errorf("%s still runs: stop the route first, or delete it with --force", d.Name)
		}
		if _, err := cp.Stop(ctx, runtime.Target{ExecutionID: rec.ID}); err != nil {
			return err
		}
	}
	return nil
}

// probeCoreEnded: done once no execution of the agent is desired running.
func probeCoreEnded(ctx context.Context, deps Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	cp, err := coreProvider(deps)
	if err != nil {
		return ops.Unknown, "", err
	}
	recs, err := liveRecords(ctx, cp, d)
	if err != nil {
		return ops.Unknown, "", err
	}
	if len(recs) == 0 {
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
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
