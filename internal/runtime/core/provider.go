// Package core is the herdr-core backend behind the runtime contract (spec
// sections 6 to 13): each agent is a core terminal, identified by its process
// rather than by an address or a label, placed in the native app, and started
// through a launch transaction whose steps run on the operation engine.
//
// On herdr-core 0.1.20 nothing in this package types into a terminal. A start
// line a harness needs is reported for a person to submit.
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Provider is the herdr-core provider on one core socket.
type Provider struct {
	Socket string
	// ExpectCore pins the core id; empty accepts the core the socket leads
	// to on first contact.
	ExpectCore string
	Records    Records
	Procs      ProcessTable
	Registrar  *Registrar
	App        *herdrapp.App
	Bindings   Bindings
	// Reconciler, when set, freezes mutations after a core identity change.
	Reconciler *Reconciler
	Now        func() time.Time
	Options    hc.Options
}

// Backend implements runtime.Provider.
func (p *Provider) Backend() string { return runtime.BackendCore }

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Provider) procs() ProcessTable {
	if p.Procs == nil {
		return SystemProcs{}
	}
	return p.Procs
}

// Dial opens a control connection to the verified core.
func (p *Provider) Dial(ctx context.Context) (*hc.Client, error) {
	opts := p.Options
	opts.ExpectCore = p.ExpectCore
	c, err := hc.Dial(ctx, p.Socket, opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// mutable refuses a mutation while the endpoint is frozen.
func (p *Provider) mutable() error {
	if p.Reconciler == nil {
		return nil
	}
	if frozen, why := p.Reconciler.Frozen(); frozen {
		return refuse(CodeFrozen, "mutations on this core are frozen until a person settles it: %s", why)
	}
	return nil
}

// Capabilities implements runtime.Provider.
func (p *Provider) Capabilities(ctx context.Context) (runtime.Capabilities, error) {
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Capabilities{}, err
	}
	defer c.Close()
	h := c.Hello()
	return runtime.Capabilities{
		Backend: runtime.BackendCore, EndpointID: h.Core.ID,
		Protocol: fmt.Sprintf("herdr-core %s, generation %d", h.Version, h.Generation),
		Methods:  append([]string(nil), hc.Methods...), Harnesses: Kinds(), TypedInput: false,
	}, nil
}

// EnsureExecution implements runtime.Provider: it reconciles the reserved
// launch in in.Launch against core's live and finished records, adopts the
// one verified match, and starts a terminal only when nothing of the launch
// exists. A record that says a terminal was started, with nothing found, is
// outcome_unknown: never a second start under the same identity.
func (p *Provider) EnsureExecution(ctx context.Context, in runtime.Intent) (runtime.Ensured, error) {
	l, ok := in.Launch.(Launch)
	if !ok {
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown}, refuse(CodeLaunchFailed, "a core execution needs its reserved launch")
	}
	if err := p.mutable(); err != nil {
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
	}
	defer c.Close()
	rec, known, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return runtime.Ensured{}, err
	}
	found, err := Reconcile(ctx, c, p.procs(), l, rec.Evidence)
	if err != nil {
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
	}
	switch found.Verdict {
	case Adopt, Finished:
		return runtime.Ensured{Outcome: runtime.OutcomeAdopted, Detail: found.Detail, Execution: inspection(found.Terminal, rec.Evidence)}, nil
	case Conflict:
		return runtime.Ensured{Outcome: runtime.OutcomeConflict, Detail: found.Detail}, refuse(CodeConflict, "%s", found.Detail)
	}
	if known && rec.TerminalAddress != "" {
		d := fmt.Sprintf("terminal %s was started for this launch and core has no record of it; start the agent again as a new execution", rec.TerminalAddress)
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: d}, refuse(CodeOutcomeUnknown, "%s", d)
	}
	t, err := Spawn(ctx, c, l)
	if err != nil {
		// A lost reply may hide a terminal that runs: look again before
		// saying nothing happened.
		again, rerr := Reconcile(ctx, c, p.procs(), l, rec.Evidence)
		if rerr == nil && again.Verdict == Absent {
			return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
		}
		if rerr == nil && again.Verdict == Adopt {
			return runtime.Ensured{Outcome: runtime.OutcomeCreated, Execution: inspection(again.Terminal, rec.Evidence)}, nil
		}
		d := fmt.Sprintf("the start's answer was lost (%v) and core's records do not settle it", err)
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: d}, refuse(CodeOutcomeUnknown, "%s", d)
	}
	return runtime.Ensured{Outcome: runtime.OutcomeCreated, Execution: inspection(t, rec.Evidence)}, nil
}

func inspection(t hc.Terminal, ev runtime.ProcessEvidence) runtime.Inspection {
	ins := runtime.Inspection{Exists: true, TerminalAddress: t.Address, Evidence: ev, Kind: ""}
	ins.Observation.Process = runtime.ProcessStarting
	if !t.Live() {
		ins.Observation.Process = runtime.ProcessFinished
	}
	if t.Status != nil {
		ins.Observation.Program, ins.Observation.RawProgram = runtime.ProgramOf(t.Status.State), t.Status.State
	} else {
		ins.Observation.Program = runtime.ProgramUnknown
	}
	return ins
}

// execution finds the record a target names.
func (p *Provider) execution(ctx context.Context, t runtime.Target) (runtime.Execution, error) {
	all, err := p.Records.List(ctx, "", "")
	if err != nil {
		return runtime.Execution{}, err
	}
	for _, e := range all {
		if e.Backend == runtime.BackendCore && t.ExecutionID != "" && e.ID == t.ExecutionID {
			return e, nil
		}
	}
	return runtime.Execution{}, refuse(CodeIdentityNotProven, "no core execution %s is recorded", t.ExecutionID)
}

// Inspect implements runtime.Provider. Identity is the registered process:
// an address that runs anything else is a lost execution.
func (p *Provider) Inspect(ctx context.Context, t runtime.Target) (runtime.Inspection, error) {
	e, err := p.execution(ctx, t)
	if err != nil {
		return runtime.Inspection{}, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Inspection{}, err
	}
	defer c.Close()
	term, ok, err := Proven(ctx, c, p.procs(), e)
	if err != nil {
		return runtime.Inspection{}, err
	}
	obs := e.Observation
	obs.ObservedAt = p.now()
	out := runtime.Inspection{Exists: term.Address != "", Kind: e.HarnessKind, TerminalAddress: e.TerminalAddress, Evidence: e.Evidence, Conversation: e.Conversation}
	switch {
	case ok:
		obs.Process = runtime.ProcessLive
		if term.Status != nil {
			obs.Program, obs.RawProgram = runtime.ProgramOf(term.Status.State), term.Status.State
		}
	case term.Address != "" && term.Live():
		obs.Process, obs.Readiness = runtime.ProcessLost, runtime.ReadinessIdentityUnknown
	default:
		obs.Process, obs.Readiness = runtime.ProcessFinished, runtime.ReadinessIdentityUnknown
	}
	out.Observation = obs
	return out, nil
}

// Observe implements runtime.Provider: a fresh inspection in the core's
// epoch. Changes are carried by the reconcilers, not here.
func (p *Provider) Observe(ctx context.Context, t runtime.Target, _ string) (runtime.Snapshot, error) {
	cur, err := p.Inspect(ctx, t)
	if err != nil {
		return runtime.Snapshot{}, err
	}
	return runtime.Snapshot{Epoch: "core:" + p.ExpectCore + ":" + p.now().UTC().Format(time.RFC3339Nano), Current: cur}, nil
}

// Read implements runtime.Provider with terminal.read, bounded.
func (p *Provider) Read(ctx context.Context, t runtime.Target, r runtime.ReadRequest) (runtime.Text, error) {
	e, err := p.execution(ctx, t)
	if err != nil {
		return runtime.Text{}, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Text{}, err
	}
	defer c.Close()
	res, err := c.Read(ctx, e.TerminalAddress, uint(max(r.Lines, 0)))
	if err != nil {
		return runtime.Text{}, err
	}
	text, cut := res.Text, !res.Complete
	if r.MaxBytes > 0 && len(text) > r.MaxBytes {
		text, cut = text[len(text)-r.MaxBytes:], true
	}
	return runtime.Text{Text: text, Truncated: cut}, nil
}

// Deliver implements runtime.Provider. On core an instruction is a notice the
// execution claims; this build issues none yet, and nothing is ever typed.
func (p *Provider) Deliver(context.Context, runtime.Target, runtime.Delivery) (runtime.Delivered, error) {
	return runtime.Delivered{}, refuse(CodeDeliveryNotOnCore, "instructions reach herdr-core agents as notices, which this build does not issue yet; nothing is typed into a core terminal")
}

// Interrupt implements runtime.Provider: one interrupt to the proven
// execution, never to an address that runs anything else, never escalated.
func (p *Provider) Interrupt(ctx context.Context, t runtime.Target) (runtime.Interrupted, error) {
	if err := p.mutable(); err != nil {
		return runtime.Interrupted{}, err
	}
	e, err := p.execution(ctx, t)
	if err != nil {
		return runtime.Interrupted{}, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Interrupted{}, err
	}
	defer c.Close()
	term, ok, err := Proven(ctx, c, p.procs(), e)
	if err != nil {
		return runtime.Interrupted{}, err
	}
	if !ok {
		return runtime.Interrupted{}, refuse(CodeIdentityNotProven, "terminal %s does not run execution %s", e.TerminalAddress, e.ID)
	}
	out := runtime.Interrupted{Program: runtime.ProgramUnknown}
	if term.Status != nil {
		out.Program, out.RawProgram = runtime.ProgramOf(term.Status.State), term.Status.State
	}
	if _, err := c.Interrupt(ctx, term.Address, 0); err != nil {
		return out, err
	}
	out.Sent = true
	return out, nil
}

// Stop implements runtime.Provider: the proven execution and the processes
// recorded under it end, and the end is verified.
func (p *Provider) Stop(ctx context.Context, t runtime.Target) (runtime.Stopped, error) {
	if err := p.mutable(); err != nil {
		return runtime.Stopped{}, err
	}
	e, err := p.execution(ctx, t)
	if err != nil {
		return runtime.Stopped{}, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Stopped{}, err
	}
	defer c.Close()
	res, err := Stop(ctx, c, StopInput{Execution: e, Procs: p.procs()})
	if err != nil {
		return runtime.Stopped{}, err
	}
	if !res.Verified && !res.Shell {
		return runtime.Stopped{}, fmt.Errorf("execution %s did not end: %d recorded processes are left", e.ID, len(res.Left))
	}
	e.Desired = runtime.DesiredStopped
	e.Observation.Process, e.Observation.Readiness, e.Observation.Orchestration = runtime.ProcessFinished, runtime.ReadinessIdentityUnknown, runtime.OrchestrationStopped
	if res.Shell {
		e.Observation.Process = runtime.ProcessLost
	}
	e.Observation.ObservedAt = p.now()
	if _, err := p.Records.Put(ctx, e); err != nil {
		return runtime.Stopped{}, err
	}
	return runtime.Stopped{Already: res.Already || res.Shell}, nil
}

// owned is the execution's placement as recorded.
func owned(e runtime.Execution) (herdrapp.Owned, bool) {
	if e.App == nil || e.App.SpaceID == "" || e.App.TabID == "" {
		return herdrapp.Owned{}, false
	}
	return herdrapp.Owned{Terminal: e.TerminalAddress, Execution: e.ID, Placement: herdrapp.PlacementIDs{Space: e.App.SpaceID, Tab: e.App.TabID}}, true
}

// EnsureVisible implements runtime.Provider: the execution's visibility as
// the window shows it now.
func (p *Provider) EnsureVisible(ctx context.Context, t runtime.Target) (runtime.Visibility, error) {
	e, err := p.execution(ctx, t)
	if err != nil {
		return runtime.VisibilityUnverified, err
	}
	o, ok := owned(e)
	if !ok || p.App == nil {
		return runtime.VisibilityUnverified, nil
	}
	obs, err := p.App.Observe(ctx, o)
	if err != nil || obs.AppClosed {
		if e.App.Visibility == runtime.VisibilityMissing {
			return runtime.VisibilityMissing, nil
		}
		return runtime.VisibilityPending, nil
	}
	if obs.PanePresent {
		return runtime.VisibilityVerified, nil
	}
	return runtime.VisibilityMissing, nil
}

// Show implements runtime.Provider: it restores a parked execution's
// placement labels on core, explicitly, and brings it to the front. It never
// creates a terminal.
func (p *Provider) Show(ctx context.Context, t runtime.Target) error {
	e, err := p.execution(ctx, t)
	if err != nil {
		return err
	}
	o, ok := owned(e)
	if !ok || p.App == nil {
		return refuse(CodeWaitingVisibility, "execution %s has no recorded place in the app", e.ID)
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	term, proven, err := Proven(ctx, c, p.procs(), e)
	if err != nil {
		return err
	}
	if !proven {
		return refuse(CodeIdentityNotProven, "terminal %s does not run execution %s", e.TerminalAddress, e.ID)
	}
	if herdrapp.Parked(term.Labels, o.Placement) {
		restore := map[string]string{herdrapp.LabelSpace: o.Placement.Space, herdrapp.LabelTab: o.Placement.Tab}
		if err := herdrapp.CheckLabels(restore); err != nil {
			return err
		}
		if _, err := c.Label(ctx, term.Address, restore); err != nil {
			return err
		}
	}
	_, err = p.App.Show(ctx, o)
	return err
}

var _ runtime.Provider = (*Provider)(nil)
