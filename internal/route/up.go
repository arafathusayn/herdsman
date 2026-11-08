package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/ops"
)

// OperationKind names a route's start in the journal.
const OperationKind = "route.up"

// StepsOperation is the marker folder of a route's start. It is a fixed name,
// not the operation's id: a later operation of the same route must read the
// markers of the earlier one, or a step that ran would look as if it never did.
const StepsOperation = "up"

// DefaultAgentTimeout bounds one agent start; herdr accepts at most five minutes.
const DefaultAgentTimeout = 2 * time.Minute

// The effect names.
const (
	effectFolder    = "route.folder"
	effectInbox     = "route.inbox"
	effectWorkspace = "route.workspace"
	effectPane      = "route.pane"
	effectPrelude   = "route.prelude"
	effectAgent     = "route.agent"
	effectTargets   = "route.targets"
	effectAttach    = "route.attach"
	effectPropose   = "route.propose"
	effectApprove   = "route.approve"
	effectService   = "route.service"
	effectHandshake = "route.handshake"
	effectBrief     = "route.brief"
)

// The pane labels of a route.
const (
	roleAdjutant     = "adjutant"
	roleOrchestrator = "orchestrator"
	labelCourier     = "courier"
	labelCardWatch   = "card-watch"
	labelCheckpoint  = "checkpointer"
)

var routeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// UpRequest is everything a route's start depends on.
type UpRequest struct {
	Route, Repo, Prompt                  string
	AdjutantCommand, OrchestratorCommand string
	AdjutantKind, OrchestratorKind       string
	AdjutantProbe                        string // a probe for the adjutant's first prelude step, shell words
	AdjutantOnce                         bool   // the adjutant's prelude steps never rerun
	NoAdjutant, PairManual               bool
	ConfigPath, HerdrSocket, PrivateHome string
	Executable                           string
	Date                                 string // the date in the route folder's name, YYYY-MM-DD
	PolicyFiles, Kinds                   []string
}

// UpPlan is the computed start: the journal's operation and what to print.
type UpPlan struct {
	Key                            string
	Folder, Inbox, Workspace       string
	AdjutantName, OrchestratorName string
	Spec                           ops.Spec
	Text                           string
	// Prelude maps a step number to its launch step.
	Prelude map[int]launch.Step
}

// stepData is the part of a plan an effect needs. It is written into each
// journal step, so a restarted daemon needs nothing else.
type stepData struct {
	Key          string            `json:"key"`
	Route        string            `json:"route"`
	Repo         string            `json:"repo"`
	Folder       string            `json:"folder"`
	Inbox        string            `json:"inbox,omitempty"`
	Workspace    string            `json:"workspace"`
	Socket       string            `json:"socket"`
	Executable   string            `json:"executable"`
	Label        string            `json:"label,omitempty"`
	Group        string            `json:"group,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Prompt       string            `json:"prompt,omitempty"`
	Config       string            `json:"config,omitempty"`
	PolicyFiles  []string          `json:"policy_files,omitempty"`
	N            int               `json:"n,omitempty"`
	Launch       *launch.Step      `json:"launch,omitempty"`
	TimeoutMS    int64             `json:"timeout_ms,omitempty"`
	Name         string            `json:"name,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Role         string            `json:"role,omitempty"`
	Adjutant     string            `json:"adjutant,omitempty"`
	Orchestrator string            `json:"orchestrator,omitempty"`
	Line         string            `json:"line,omitempty"`
	Manual       bool              `json:"manual,omitempty"`
	Peers        []string          `json:"peers,omitempty"`
	// Op is the running operation's id, filled in when a step is decoded.
	Op string `json:"-"`
	// Tag says which delivery a prompt step is, for its record.
	Tag string `json:"tag,omitempty"`
	// Control is set on the steps of a pause, resume, stop or delete.
	Control *controlData `json:"control,omitempty"`
}

// UpKey is the idempotency key of a route's start.
func UpKey(repo, name string) string { return "up:" + repo + ":" + name }

// Facts is what the journal says about a route's start.
type Facts struct{ Route, Folder, Inbox, Workspace string }

// Describe reads a start's facts from its first step.
func Describe(op *ops.Operation) Facts {
	if len(op.Steps) == 0 {
		return Facts{}
	}
	d, err := decode(op.Steps[0])
	if err != nil {
		return Facts{}
	}
	return Facts{Route: d.Route, Folder: d.Folder, Inbox: d.Inbox, Workspace: d.Workspace}
}

// PlanOf rebuilds a plan from a journaled start, so a resume needs none of the
// original flags and cannot differ from what was journaled.
func PlanOf(op *ops.Operation) (*UpPlan, error) {
	p := &UpPlan{Prelude: map[int]launch.Step{}, Text: op.Plan}
	spec := ops.Spec{Kind: op.Kind, Plan: op.Plan, Preconditions: op.Preconditions}
	for _, st := range op.Steps {
		d, err := decode(st)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", st.Ordinal, err)
		}
		p.Key, p.Folder, p.Inbox, p.Workspace = d.Key, d.Folder, d.Inbox, d.Workspace
		if d.Launch != nil {
			p.Prelude[d.N] = *d.Launch
		}
		spec.Steps = append(spec.Steps, st.StepSpec)
	}
	spec.Key = p.Key
	p.Spec = spec
	return p, nil
}

// HarnessNames lists the programs that count as a coding agent when found
// among a caller's ancestors.
var HarnessNames = []string{"pi", "claude", "codex", "gemini", "cursor", "cursor-agent", "devin", "agy", "cline", "omp", "mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo", "qodercli", "qwen", "letta", "maki", "muse"}

// DateOf returns the date in a start's route folder, so that a later call
// lands in the same folder even on another day.
func DateOf(op *ops.Operation) string {
	folder := filepath.Base(Describe(op).Folder)
	if len(folder) < 10 {
		return ""
	}
	return folder[len(folder)-10:]
}

// compiled is one coordinator's launch.
type compiled struct {
	role, name, kind, line string
	spec                   launch.Spec
}

// PlanUp computes a route's start without touching anything.
func PlanUp(q UpRequest) (*UpPlan, error) {
	if !routeNameRe.MatchString(q.Route) {
		return nil, fmt.Errorf("the route name %q must be lower-case letters, digits and hyphens", q.Route)
	}
	if !filepath.IsAbs(q.Repo) {
		return nil, fmt.Errorf("the repository path %q is not absolute", q.Repo)
	}
	if q.Prompt == "" {
		return nil, errors.New("the route has no prompt")
	}
	if strings.ContainsFunc(q.Prompt, func(r rune) bool { return r == 0 }) {
		return nil, errors.New("the prompt holds a NUL byte")
	}
	repoName := filepath.Base(q.Repo)
	p := &UpPlan{
		Key:              UpKey(q.Repo, q.Route),
		Folder:           filepath.Join(q.Repo, ".herdsman", q.Route+"-"+q.Date),
		Workspace:        repoName + "-" + q.Route,
		OrchestratorName: repoName + "-" + q.Route + "-orchestrator",
		Prelude:          map[int]launch.Step{},
	}
	stepsDir, err := launch.StepsDir(p.Folder, StepsOperation)
	if err != nil {
		return nil, err
	}
	base := stepData{Key: p.Key, Route: q.Route, Repo: q.Repo, Folder: p.Folder, Workspace: p.Workspace, Socket: q.HerdrSocket, Executable: q.Executable}
	var coordinators []compiled
	if !q.NoAdjutant {
		p.AdjutantName = repoName + "-" + q.Route + "-adjutant"
		p.Inbox = filepath.Join(q.PrivateHome, repoName+"-"+q.Route)
		base.Inbox = p.Inbox
		c, err := compileRole(roleAdjutant, p.AdjutantName, q.AdjutantCommand, q.AdjutantKind, "--adjutant-kind", q)
		if err != nil {
			return nil, err
		}
		if err := applyAdjutantOptions(&c, q); err != nil {
			return nil, err
		}
		coordinators = append(coordinators, c)
	}
	orchestrator, err := compileRole(roleOrchestrator, p.OrchestratorName, q.OrchestratorCommand, q.OrchestratorKind, "--orchestrator-kind", q)
	if err != nil {
		return nil, err
	}
	coordinators = append(coordinators, orchestrator)
	channelEnv := map[string]string{}
	if !q.NoAdjutant {
		channelEnv["HERDSMAN_INBOX"] = p.Inbox
		channelEnv["HERDSMAN_PREFLIGHT"] = filepath.Join(p.Inbox, "setup.json")
		channelEnv["HERDSMAN_ORCHESTRATOR"] = p.OrchestratorName
		channelEnv["HERDSMAN_ADJUTANT"] = p.AdjutantName
	}

	var steps []ops.StepSpec
	add := func(name, effect, key string, retry ops.Retry, d stepData) {
		b, err := json.Marshal(d)
		if err != nil {
			panic(err) // the struct holds only strings, numbers and maps
		}
		steps = append(steps, ops.StepSpec{Name: name, Effect: effect, Key: key, Spec: string(b), Retry: retry})
	}
	with := func(edit func(*stepData)) stepData {
		d := base
		edit(&d)
		return d
	}

	var text strings.Builder
	fmt.Fprintf(&text, "route: %s\nfolder: %s\nworkspace: %s\n", q.Route, p.Folder, p.Workspace)
	if p.Inbox != "" {
		fmt.Fprintf(&text, "inbox: %s\n", p.Inbox)
	}
	if q.ConfigPath != "" {
		fmt.Fprintf(&text, "config: %s\n", q.ConfigPath)
	}

	add("create the route folder", effectFolder, "folder:"+p.Folder, ops.RetryIdempotent, with(func(d *stepData) {
		d.Prompt, d.Config, d.Adjutant, d.Orchestrator = q.Prompt, q.ConfigPath, p.AdjutantName, p.OrchestratorName
	}))
	if !q.NoAdjutant {
		add("create the private inbox", effectInbox, "inbox:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) { d.PolicyFiles = q.PolicyFiles }))
	}
	firstEnv := mergeEnv(channelEnv, coordinators[0].spec.Env)
	add("create the workspace", effectWorkspace, "workspace:"+p.Workspace, ops.RetryIdempotent, with(func(d *stepData) { d.Env = firstEnv }))
	for _, c := range coordinators {
		add("open the "+c.role+" pane", effectPane, "pane:"+c.role, ops.RetryIdempotent, with(func(d *stepData) {
			d.Label, d.Group, d.Env = c.role, "coordinators", mergeEnv(channelEnv, c.spec.Env)
		}))
	}
	n := 0
	for _, c := range coordinators {
		plan, err := launch.NewPlan(c.spec, stepsDir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.role, err)
		}
		fmt.Fprintf(&text, "\n%s: %s (kind %s)\ncommand: %s\n", c.role, c.name, c.kind, c.line)
		for i, step := range c.spec.Prelude {
			n++
			step := step
			retry := ops.RetryNever
			if step.RetryPolicy() == launch.RetryIdempotent {
				retry = ops.RetryIdempotent
			}
			p.Prelude[n] = step
			fmt.Fprintf(&text, "step %d: %s\n  retry: %s\n", n, plan.Steps[i].Command, plan.Steps[i].Retry)
			add(fmt.Sprintf("run prelude step %d", n), effectPrelude, "prelude:"+strconv.Itoa(n), retry, with(func(d *stepData) {
				d.N, d.Launch, d.TimeoutMS, d.Label, d.Group, d.Env = n, &step, c.spec.Timeout().Milliseconds(), c.role, "coordinators", mergeEnv(channelEnv, c.spec.Env)
			}))
		}
		for _, e := range plan.Env {
			fmt.Fprintf(&text, "env: %s\n", e.Name)
		}
		fmt.Fprintf(&text, "agent: %s\n", plan.Agent)
		add("start "+c.name, effectAgent, "agent:"+c.name, ops.RetryNever, with(func(d *stepData) {
			d.Name, d.Kind, d.Args, d.Label, d.Group, d.Env = c.name, c.kind, c.spec.Command[1:], c.role, "coordinators", mergeEnv(channelEnv, c.spec.Env)
		}))
	}

	orchestratorKind := coordinators[len(coordinators)-1].kind
	if q.NoAdjutant {
		add("send the first brief", effectBrief, "brief:"+p.OrchestratorName, ops.RetryNever, with(func(d *stepData) {
			d.Name, d.Tag, d.Prompt = p.OrchestratorName, "brief", skillPrefix(orchestratorKind)+"herdsman run "+shellQuote(q.Prompt)
		}))
		addCheckpointer(add, with, orchestratorKind, p, channelEnv, q)
		p.Spec = ops.Spec{Key: p.Key, Kind: OperationKind, Plan: text.String(), Steps: steps}
		p.Text = text.String()
		return p, nil
	}

	identities := []string{p.AdjutantName, p.OrchestratorName}
	add("write the channel targets", effectTargets, "targets:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) { d.Peers = identities }))
	adjutantKind := coordinators[0].kind
	add("attach the adjutant", effectAttach, "attach:"+p.AdjutantName, ops.RetryNever, with(func(d *stepData) {
		d.Name, d.Tag, d.Prompt = p.AdjutantName, "attach", skillPrefix(adjutantKind)+"herdsman adjutant --name "+p.AdjutantName+" --orchestrator "+p.OrchestratorName+" --inbox "+p.Inbox
	}))
	add("attach the orchestrator", effectAttach, "attach:"+p.OrchestratorName, ops.RetryNever, with(func(d *stepData) {
		d.Name, d.Tag, d.Prompt = p.OrchestratorName, "attach", skillPrefix(orchestratorKind)+"herdsman orchestrator --adjutant "+p.AdjutantName+" --name "+p.OrchestratorName+" --inbox "+p.Inbox
	}))
	for _, c := range coordinators {
		add("enroll "+c.name, effectPropose, "propose:"+c.name, ops.RetryIdempotent, with(func(d *stepData) {
			d.Name, d.Role, d.Label = c.name, c.role, c.role
		}))
	}
	add("approve the pair", effectApprove, "approve:"+p.Inbox, ops.RetryNever, with(func(d *stepData) { d.Manual = q.PairManual }))
	for _, svc := range []struct{ label, line string }{
		{labelCourier, shellQuote(q.Executable) + " message courier"},
		{labelCardWatch, shellQuote(q.Executable) + " message card-watch"},
	} {
		add("start "+svc.label, effectService, "service:"+svc.label, ops.RetryIdempotent, with(func(d *stepData) {
			d.Label, d.Group, d.Env, d.Line = svc.label, "services", channelEnv, svc.line
		}))
	}
	addCheckpointer(add, with, orchestratorKind, p, channelEnv, q)
	add("seed the handshake", effectHandshake, "handshake:"+p.Inbox, ops.RetryIdempotent, with(func(d *stepData) {
		d.Adjutant, d.Orchestrator = p.AdjutantName, p.OrchestratorName
	}))
	add("send the first brief", effectBrief, "brief:"+p.AdjutantName, ops.RetryNever, with(func(d *stepData) {
		d.Name, d.Tag, d.Prompt = p.AdjutantName, "brief", q.Prompt
	}))
	p.Spec = ops.Spec{Key: p.Key, Kind: OperationKind, Plan: text.String(), Steps: steps}
	p.Text = text.String()
	return p, nil
}

// addCheckpointer adds the checkpointer pane when the orchestrator's kind has
// a transcript the checkpointer can read.
func addCheckpointer(add func(string, string, string, ops.Retry, stepData), with func(func(*stepData)) stepData, kind string, p *UpPlan, env map[string]string, q UpRequest) {
	if kind != "claude" && kind != "codex" {
		return
	}
	add("start the checkpointer", effectService, "service:"+labelCheckpoint, ops.RetryIdempotent, with(func(d *stepData) {
		d.Label, d.Group, d.Env, d.Name, d.Line = labelCheckpoint, "coordinators", env, p.OrchestratorName, shellQuote(q.Executable)+" checkpointer"
	}))
}

func compileRole(role, name, line, kind, flag string, q UpRequest) (compiled, error) {
	spec, err := launch.Compile(line, launch.Options{})
	if err != nil {
		var refused *launch.RefusedError
		if errors.As(err, &refused) {
			return compiled{}, fmt.Errorf("%s: %w\nwrite it in the config instead:\n%s", role, err, refused.Hint())
		}
		return compiled{}, fmt.Errorf("%s: %w", role, err)
	}
	spec.Cwd = q.Repo
	resolved, err := resolveKind(spec, kind, flag, role, q.Kinds)
	if err != nil {
		return compiled{}, err
	}
	return compiled{role: role, name: name, kind: resolved, line: line, spec: spec}, nil
}

// resolveKind reads the harness kind from the agent command, the last program
// of the line. Only a kind herdr knows counts; anything else asks for the flag
// rather than guessing.
func resolveKind(spec launch.Spec, kind, flag, role string, kinds []string) (string, error) {
	if kind != "" {
		if len(kinds) > 0 && !slices.Contains(kinds, kind) {
			return "", fmt.Errorf("%s: herdr has no agent kind %q", role, kind)
		}
		return kind, nil
	}
	program := filepath.Base(spec.Command[0])
	if slices.Contains(kinds, program) {
		return program, nil
	}
	return "", fmt.Errorf("%s: %q is not an agent kind herdr lists; name one with %s <kind>", role, program, flag)
}

func applyAdjutantOptions(c *compiled, q UpRequest) error {
	if q.AdjutantProbe == "" && !q.AdjutantOnce {
		return nil
	}
	if len(c.spec.Prelude) == 0 {
		return errors.New("adjutant: the launch line has no prelude step to attach options to")
	}
	if q.AdjutantOnce {
		for i := range c.spec.Prelude {
			c.spec.Prelude[i].Once = true
		}
	}
	if q.AdjutantProbe != "" {
		c.spec.Prelude[0].Probe = &launch.Command{Argv: []string{"/bin/sh", "-c", q.AdjutantProbe}}
	}
	return nil
}

func mergeEnv(maps_ ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps_ {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func skillPrefix(kind string) string {
	if kind == "codex" {
		return "$"
	}
	return "/"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// The channel actions.
const (
	ChannelRegister = "register"
	ChannelSend     = "send"
)

// ChannelCall is one action on a route's message channel, made as Pane.
type ChannelCall struct {
	Inbox, Socket, Executable, Pane, Action string
	Args                                    []string
}

// Deps is what the effects reach outside the journal.
type Deps struct {
	// Channel performs a channel action.
	Channel func(context.Context, ChannelCall) error
	// Callers holds who asked, for the pair's attestation.
	Callers *Callers
	// PollEvery is the wait between marker reads (default 200 ms).
	PollEvery time.Duration
	// Poll, when set, runs once per wait; tests use it to stop a run.
	Poll func()
	// AgentTimeout bounds one agent start.
	AgentTimeout time.Duration
}

type effect struct {
	deps  Deps
	apply func(context.Context, Deps, *herdr.Client, stepData) error
	probe func(context.Context, Deps, *herdr.Client, stepData) (ops.Verdict, string, error)
}

func decode(s ops.Step) (stepData, error) {
	var d stepData
	err := json.Unmarshal([]byte(s.Spec), &d)
	d.Op = s.OpID
	return d, err
}

// Apply runs the step.
func (e effect) Apply(ctx context.Context, s ops.Step) error {
	d, err := decode(s)
	if err != nil {
		return err
	}
	return e.apply(ctx, e.deps, herdr.New(d.Socket), d)
}

// Probe looks for the step's effect.
func (e effect) Probe(ctx context.Context, s ops.Step) (ops.Verdict, string, error) {
	d, err := decode(s)
	if err != nil {
		return ops.Unknown, "", err
	}
	return e.probe(ctx, e.deps, herdr.New(d.Socket), d)
}

// Effects returns every effect a route's start uses.
func Effects(deps Deps) map[string]ops.Effect {
	if deps.PollEvery == 0 {
		deps.PollEvery = 200 * time.Millisecond
	}
	if deps.AgentTimeout == 0 {
		deps.AgentTimeout = DefaultAgentTimeout
	}
	if deps.Callers == nil {
		deps.Callers = NewCallers()
	}
	effects := controlEffects(deps)
	maps.Copy(effects, map[string]ops.Effect{
		effectFolder:    effect{deps, applyFolder, probeFolder},
		effectInbox:     effect{deps, applyInbox, probeInbox},
		effectWorkspace: effect{deps, applyWorkspace, probeWorkspace},
		effectPane:      effect{deps, applyPane, probePane},
		effectPrelude:   effect{deps, applyPrelude, probePrelude},
		effectAgent:     effect{deps, applyAgent, probeAgent},
		effectTargets:   effect{deps, applyTargets, probeTargets},
		effectAttach:    effect{deps, applyPrompt, probeSent},
		effectPropose:   effect{deps, applyPropose, probePropose},
		effectApprove:   effect{deps, applyApprove, probeApprove},
		effectService:   effect{deps, applyService, probeService},
		effectHandshake: effect{deps, applyHandshake, probeHandshake},
		effectBrief:     effect{deps, applyPrompt, probeSent},
	})
	return effects
}

func applyPrelude(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	stepsDir, err := launch.StepsDir(d.Folder, StepsOperation)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stepsDir, 0o700); err != nil {
		return err
	}
	step := *d.Launch
	marker, err := launch.ReadMarker(stepsDir, d.N)
	if err != nil {
		return err
	}
	decision := launch.Decide(step, d.N, false, marker)
	for {
		switch decision.Action {
		case launch.ActionDone:
			return nil
		case launch.ActionFail, launch.ActionStop:
			return errors.New(decisionText(decision))
		case launch.ActionRun:
			if err := launch.ClearMarkers(stepsDir, d.N); err != nil {
				return err
			}
			line, err := launch.Wrap(stepsDir, d.N, step)
			if err != nil {
				return err
			}
			marker, err = submitAndWait(ctx, deps, c, d, line, func() (launch.Marker, error) { return launch.ReadMarker(stepsDir, d.N) })
			if err != nil {
				return err
			}
			decision = launch.Decide(step, d.N, false, marker)
			if decision.Action == launch.ActionRun {
				return fmt.Errorf("step %d left no markers", d.N)
			}
		case launch.ActionProbe:
			if err := launch.ClearProbeMarkers(stepsDir, d.N); err != nil {
				return err
			}
			line, err := launch.WrapProbe(stepsDir, d.N, step)
			if err != nil {
				return err
			}
			marker, err = submitAndWait(ctx, deps, c, d, line, func() (launch.Marker, error) { return launch.ReadProbeMarker(stepsDir, d.N) })
			if err != nil {
				return err
			}
			decision = launch.DecideProbe(step, d.N, marker)
			if decision.Action == launch.ActionProbe {
				return fmt.Errorf("the probe of step %d gave no result", d.N)
			}
		default:
			return fmt.Errorf("step %d: unexpected decision %s", d.N, decision.Action)
		}
	}
}

func decisionText(d launch.Decision) string {
	if len(d.Resume) == 0 {
		return d.Reason
	}
	return d.Reason + "; resume with: " + strings.Join(d.Resume, " or ")
}

// submitAndWait types one marked line into the coordinator's pane and waits
// for its status file.
func submitAndWait(ctx context.Context, deps Deps, c *herdr.Client, d stepData, line string, read func() (launch.Marker, error)) (launch.Marker, error) {
	pane, err := ensurePane(ctx, c, d)
	if err != nil {
		return launch.Marker{}, err
	}
	if err := sendLine(ctx, c, pane, line); err != nil {
		return launch.Marker{}, err
	}
	deadline := time.Now().Add(time.Duration(d.TimeoutMS) * time.Millisecond)
	for {
		marker, err := read()
		if err != nil {
			return launch.Marker{}, err
		}
		if marker.State == launch.MarkerFinished {
			return marker, nil
		}
		if time.Now().After(deadline) {
			return marker, fmt.Errorf("step %d timed out after %s", d.N, time.Duration(d.TimeoutMS)*time.Millisecond)
		}
		if deps.Poll != nil {
			deps.Poll()
		}
		select {
		case <-ctx.Done():
			return marker, ctx.Err()
		case <-time.After(deps.PollEvery):
		}
	}
}

// probePrelude tells the engine what the markers prove. It reports Absent for
// a step that may safely reach Apply again, since Apply decides from the same
// markers and never repeats what they forbid.
func probePrelude(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	stepsDir, err := launch.StepsDir(d.Folder, StepsOperation)
	if err != nil {
		return ops.Unknown, "", err
	}
	step := *d.Launch
	marker, err := launch.ReadMarker(stepsDir, d.N)
	if err != nil {
		return ops.Unknown, err.Error(), nil
	}
	decision := launch.Decide(step, d.N, false, marker)
	switch decision.Action {
	case launch.ActionDone:
		return ops.Matches, decision.Reason, nil
	case launch.ActionFail:
		return ops.Different, decisionText(decision), nil
	case launch.ActionStop:
		return ops.Unknown, decisionText(decision), nil
	case launch.ActionProbe:
		// A probe that already found nothing on a once step cannot be retried.
		if m, err := launch.ReadProbeMarker(stepsDir, d.N); err == nil {
			if pd := launch.DecideProbe(step, d.N, m); pd.Action == launch.ActionStop {
				return ops.Unknown, decisionText(pd), nil
			}
		}
	}
	return ops.Absent, decision.Reason, nil
}

func applyAgent(ctx context.Context, deps Deps, c *herdr.Client, d stepData) error {
	if v, _, err := probeAgent(ctx, deps, c, d); err == nil && v == ops.Matches {
		return nil
	}
	pane, err := ensurePane(ctx, c, d)
	if err != nil {
		return err
	}
	_, err = c.AgentStart(ctx, d.Name, d.Kind, pane, d.Args, int(deps.AgentTimeout.Milliseconds()))
	return err
}

func probeAgent(ctx context.Context, _ Deps, c *herdr.Client, d stepData) (ops.Verdict, string, error) {
	record, err := c.AgentGet(ctx, d.Name)
	if err != nil {
		if isNotFound(err) {
			return ops.Absent, "no agent named " + d.Name, nil
		}
		return ops.Unknown, "", err
	}
	if got := agentKind(record); got != "" && got != d.Kind {
		return ops.Different, fmt.Sprintf("an agent named %s runs %s, not %s", d.Name, got, d.Kind), nil
	}
	return ops.Matches, "", nil
}

func agentKind(record herdr.AgentRecord) string {
	if record.Agent.Agent == nil {
		return ""
	}
	return *record.Agent.Agent
}

func isNotFound(err error) bool {
	var he *herdr.Error
	return errors.As(err, &he) && strings.Contains(he.Code, "not_found")
}

// sentStamp is written after a text reached an agent, the only record that a
// prompt was delivered.
func sentStamp(d stepData) string {
	return filepath.Join(d.Folder, ".steps", "sent-"+d.Tag+"-"+strings.ReplaceAll(d.Name, "/", "-"))
}

func applyPrompt(ctx context.Context, _ Deps, c *herdr.Client, d stepData) error {
	if _, err := os.Stat(sentStamp(d)); err == nil {
		return nil
	}
	if err := c.AgentPrompt(ctx, d.Name, d.Prompt); err != nil {
		return err
	}
	return writeFileNew(sentStamp(d), nil)
}

func probeSent(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	if _, err := os.Stat(sentStamp(d)); err == nil {
		return ops.Matches, "", nil
	}
	return ops.Unknown, "no record that the prompt reached " + d.Name, nil
}

func writeFileNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Continue applies a person's verdicts to a halted start, so the next Drive
// goes on from there. A pair left pending is put back to run, since a fresh
// call brings a fresh attestation.
func Continue(e *ops.Engine, callers *Callers, p *UpPlan, op *ops.Operation, overrides map[int]string) error {
	for n, verdict := range overrides {
		step, ok := p.Prelude[n]
		if !ok {
			return fmt.Errorf("the route has no prelude step %d", n)
		}
		if _, err := launch.ApplyOverride(step, n, launch.Override(verdict)); err != nil {
			return err
		}
		st, ok := findStep(op, "prelude:"+strconv.Itoa(n))
		if !ok {
			return fmt.Errorf("the operation has no prelude step %d", n)
		}
		if st.State != ops.StepIntended && st.State != ops.StepConflict {
			return fmt.Errorf("step %d is %s; only a halted step takes a verdict", n, st.State)
		}
		decision := ops.ResolveDone
		if launch.Override(verdict) == launch.OverrideRerun {
			decision = ops.ResolveRerun
			stepsDir, err := launch.StepsDir(p.Folder, StepsOperation)
			if err != nil {
				return err
			}
			if err := launch.ClearMarkers(stepsDir, n); err != nil {
				return err
			}
		}
		if err := e.Resolve(op.ID, st.Ordinal, decision); err != nil {
			return err
		}
	}
	if overrides == nil && (op.State == ops.OpStopped || op.State == ops.OpConflict) {
		for _, st := range op.Steps {
			if st.Effect == effectApprove && st.State == ops.StepIntended {
				return e.Resolve(op.ID, st.Ordinal, ops.ResolveRerun)
			}
		}
	}
	return nil
}

func findStep(op *ops.Operation, key string) (ops.Step, bool) {
	for _, s := range op.Steps {
		if s.Key == key {
			return s, true
		}
	}
	return ops.Step{}, false
}
