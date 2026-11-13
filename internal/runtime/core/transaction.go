package core

import (
	"context"
	"fmt"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// The launch transaction's step results, as a probe reports them.
type Probe int

// The probe answers, in the operation engine's sense.
const (
	ProbeUnknown   Probe = iota // cannot tell
	ProbeAbsent                 // the effect did not happen; applying again is safe
	ProbeMatches                // the effect happened and is this launch's
	ProbeDifferent              // something else holds the launch's place
)

// Timeouts of the launch transaction; zero takes the default.
type Timeouts struct {
	Register   time.Duration // the wrapper's authentication, prelude and registration (default 15 min)
	Ready      time.Duration // the first status since registration (default 90 s)
	Visibility time.Duration // the window's adoption of a placement (default 15 s)
	Poll       time.Duration // how often a wait looks again (default 250 ms)
	Connect    time.Duration // the dial and hello of a connection to core (default 5 s)
}

// DefaultConnect is how long a connection to core may take when nothing
// sets connect_timeout.
const DefaultConnect = 5 * time.Second

// ConnectTimeout is the bound on a connection to core.
func (t Timeouts) ConnectTimeout() time.Duration {
	if t.Connect <= 0 {
		return DefaultConnect
	}
	return t.Connect
}

func (t Timeouts) poll() time.Duration {
	if t.Poll == 0 {
		return 250 * time.Millisecond
	}
	return t.Poll
}

// starting is the record of a launch reserved and not yet registered.
func starting(l Launch, coreID string, now time.Time) runtime.Execution {
	return runtime.Execution{
		SchemaVersion: runtime.SchemaVersion, ID: l.Execution, Repo: l.Repo, Route: l.RouteName, Agent: l.AgentName, Role: l.Role,
		LaunchIntent: l.Intent, Backend: runtime.BackendCore, EndpointID: coreID, Cwd: l.Cwd, HarnessKind: l.Kind, Experiment: runtime.Experiment(runtime.BackendCore),
		ArgvDigest: l.Descriptor.ArgvDigest(), PolicyDigest: l.Descriptor.Digest(), Desired: runtime.DesiredRunning,
		Observation: runtime.Observation{Process: runtime.ProcessStarting, Readiness: runtime.ReadinessWaiting, Program: runtime.ProgramUnknown,
			Visibility: runtime.VisibilityUnverified, Orchestration: runtime.OrchestrationActive, ObservedAt: now},
		App: &runtime.AppBinding{Socket: l.CoreSocket, Binary: l.CoreBinary, CoreID: coreID, SpaceID: l.Placement.Space, TabID: l.Placement.Tab, Visibility: runtime.VisibilityUnverified},
	}
}

// SpawnStep is the reserve and spawn steps: the execution is recorded as
// starting before core is asked for anything, then the launch is reconciled
// and, only when nothing of it exists, started. The terminal's address is
// recorded at once.
func (p *Provider) SpawnStep(ctx context.Context, l Launch) (runtime.Execution, error) {
	c, err := p.Dial(ctx)
	if err != nil {
		return runtime.Execution{}, err
	}
	coreID := c.Core().ID
	_ = c.Close()
	rec, known, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return runtime.Execution{}, err
	}
	if !known {
		if rec, err = p.Records.Put(ctx, starting(l, coreID, p.now())); err != nil {
			return runtime.Execution{}, err
		}
	}
	got, err := p.EnsureExecution(ctx, runtime.Intent{ID: l.Intent, Name: l.AgentName, Kind: l.Kind, Role: l.Role, ExecutionID: l.Execution, Launch: l})
	if err != nil {
		return rec, err
	}
	rec.TerminalAddress = got.Execution.TerminalAddress
	if rec.InitialCreatedMS == 0 {
		if t, err := p.terminal(ctx, rec.TerminalAddress); err == nil {
			rec.InitialCreatedMS = int64(t.CreatedMS)
		}
	}
	rec.Observation.ObservedAt = p.now()
	return p.Records.Put(ctx, rec)
}

func (p *Provider) terminal(ctx context.Context, address string) (hc.Terminal, error) {
	c, err := p.Dial(ctx)
	if err != nil {
		return hc.Terminal{}, err
	}
	defer c.Close()
	return c.Get(ctx, address)
}

// ProbeSpawn reports what reconciliation finds of the launch.
func (p *Provider) ProbeSpawn(ctx context.Context, l Launch) (Probe, string, error) {
	c, err := p.Dial(ctx)
	if err != nil {
		return ProbeUnknown, err.Error(), nil
	}
	defer c.Close()
	rec, known, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return ProbeUnknown, "", err
	}
	found, err := Reconcile(ctx, c, p.procs(), l, rec.Evidence)
	if err != nil {
		return ProbeUnknown, err.Error(), nil
	}
	switch found.Verdict {
	case Adopt, Finished:
		return ProbeMatches, found.Detail, nil
	case Conflict:
		return ProbeDifferent, found.Detail, nil
	}
	if known && rec.TerminalAddress != "" {
		return ProbeUnknown, fmt.Sprintf("%s: terminal %s was started and core has no record of it", CodeOutcomeUnknown, rec.TerminalAddress), nil
	}
	return ProbeAbsent, found.Detail, nil
}

// RegisterStep waits for the wrapper in the reserved terminal to authenticate,
// run its prelude and register (spec section 8, step 5). The registrar
// records the process evidence before the wrapper starts its harness. A
// wrapper that fails, or a terminal that ends first, fails the launch: it was
// never registered, so nothing about it is ready.
func (p *Provider) RegisterStep(ctx context.Context, l Launch, timeouts Timeouts) (runtime.Execution, error) {
	rec, known, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return rec, err
	}
	if !known || rec.TerminalAddress == "" {
		return rec, refuse(CodeLaunchFailed, "launch %s has no terminal to register", l.Intent)
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return rec, err
	}
	defer c.Close()
	if registered(rec) {
		if _, ok, err := Proven(ctx, c, p.procs(), rec); err != nil || ok {
			return rec, err
		}
		return rec, refuse(CodeConflict, "terminal %s no longer runs registered process %d", rec.TerminalAddress, rec.Evidence.PID)
	}
	if p.Registrar == nil {
		return rec, refuse(CodeLaunchFailed, "this daemon has no launch socket")
	}
	address := rec.TerminalAddress
	outcomes := p.Registrar.Arm(Arm{Launch: l, Address: address, Core: c, Registered: func(ctx context.Context, ev runtime.ProcessEvidence) error {
		now := p.now()
		next := rec
		next.Evidence = ev
		next.ArgvDigest, next.PolicyDigest = l.Descriptor.ArgvDigest(), l.Descriptor.Digest()
		next.LaunchGuard = "registered " + now.UTC().Format(time.RFC3339Nano)
		next.Observation.Process, next.Observation.ObservedAt = runtime.ProcessLive, now
		next.LastVerifiedAt = now
		_, err := p.Records.Put(ctx, next)
		return err
	}})
	defer p.Registrar.Disarm(l.Intent)
	wait := timeouts.Register
	if wait == 0 {
		wait = 15 * time.Minute
	}
	deadline := time.After(wait)
	tick := time.NewTicker(timeouts.poll())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return rec, ctx.Err()
		case <-deadline:
			return rec, refuse(CodeLaunchFailed, "the wrapper in terminal %s did not register within %s", address, wait)
		case o := <-outcomes:
			switch o.Stage {
			case StageRegistered:
				got, _, err := p.Records.ByIntent(ctx, l.Intent)
				return got, err
			case StageFailed, StageRefused:
				return rec, refuse(CodeLaunchFailed, "the launch in terminal %s failed before it registered: %s", address, o.Reason)
			}
		case <-tick.C:
			t, err := c.Get(ctx, address)
			if err != nil {
				return rec, err
			}
			if !t.Live() {
				// A last registration may have landed just before the end.
				if got, _, err := p.Records.ByIntent(ctx, l.Intent); err == nil && registered(got) {
					return got, refuse(CodeLaunchFailed, "terminal %s ended right after it registered (%s)", address, ending(t))
				}
				return rec, refuse(CodeLaunchFailed, "terminal %s ended before it registered (%s)", address, ending(t))
			}
		}
	}
}

// ProbeRegister is Matches for a registered launch whose terminal still runs
// the registered process. Anything else is Absent: nothing was registered, so
// nothing needs undoing, and a new attempt is a new execution.
func (p *Provider) ProbeRegister(ctx context.Context, l Launch) (Probe, string, error) {
	rec, known, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return ProbeUnknown, "", err
	}
	if !known || !registered(rec) {
		return ProbeAbsent, "the launch has not registered", nil
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return ProbeUnknown, err.Error(), nil
	}
	defer c.Close()
	if _, ok, err := Proven(ctx, c, p.procs(), rec); err != nil {
		return ProbeUnknown, err.Error(), nil
	} else if ok {
		return ProbeMatches, "", nil
	}
	return ProbeDifferent, fmt.Sprintf("terminal %s no longer runs registered process %d", rec.TerminalAddress, rec.Evidence.PID), nil
}

// VisibilityStep establishes the execution's place in the app (spec section
// 8, step 6). Unverified (no binding ever) and missing hold the launch.
func (p *Provider) VisibilityStep(ctx context.Context, l Launch, timeouts Timeouts) (runtime.Execution, error) {
	rec, _, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return rec, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return rec, err
	}
	defer c.Close()
	prev := runtime.VisibilityUnverified
	if rec.App != nil {
		prev = rec.App.Visibility
	}
	res, err := EstablishVisibility(ctx, c, VisibilityInput{
		App: p.App, Bindings: p.Bindings, Core: herdrapp.Core{ID: c.Core().ID, Endpoint: p.Socket}, Launch: l, Address: rec.TerminalAddress,
		Previous: prev, Timeout: timeouts.Visibility, Poll: timeouts.poll(), Now: p.Now, SpaceName: l.RouteName, TabName: l.Role,
	})
	if err != nil {
		return rec, err
	}
	endpointSocket, endpointBinary := l.CoreSocket, l.CoreBinary
	if rec.App != nil && rec.App.Socket != "" {
		endpointSocket, endpointBinary = rec.App.Socket, rec.App.Binary
	}
	rec.App = res.AppBinding(c.Core().ID)
	rec.App.Socket, rec.App.Binary = endpointSocket, endpointBinary
	rec.Observation.Visibility = runtime.Visibility(res.State)
	rec.Observation.App = runtime.AppOpen
	if res.AppClosed {
		rec.Observation.App = runtime.AppClosed
	}
	rec.Observation.ObservedAt = p.now()
	if rec, err = p.Records.Put(ctx, rec); err != nil {
		return rec, err
	}
	home := "the configured app"
	if p.App != nil {
		home = "the app at " + p.App.Home
	}
	switch res.State {
	case herdrapp.Unverified:
		return rec, refuse(CodeWaitingVisibility, "no binding between %s and core %s was ever verified; open the app on that core once, so it shows terminal %s, then run up again with --continue", home, c.Core().ID, rec.TerminalAddress)
	case herdrapp.Missing:
		return rec, refuse(CodeVisibilityMissing, "%s does not show terminal %s; it may have been parked or closed; restore it with herdsman runtime show, then run up again with --continue", home, rec.TerminalAddress)
	}
	return rec, nil
}

// ProbeVisibility is Matches when the record holds a visibility that allows
// dispatch; otherwise Absent, and the step runs again.
func (p *Provider) ProbeVisibility(ctx context.Context, l Launch) (Probe, string, error) {
	rec, _, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return ProbeUnknown, "", err
	}
	if v := rec.Observation.Visibility; v == runtime.VisibilityPending || v == runtime.VisibilityVerified {
		return ProbeMatches, string(v), nil
	}
	return ProbeAbsent, string(rec.Observation.Visibility), nil
}

// ReadyStep waits for readiness (spec section 8, step 7). A dialog or a
// silent harness needs a person, and nothing is typed.
func (p *Provider) ReadyStep(ctx context.Context, l Launch, timeouts Timeouts) (runtime.Execution, error) {
	rec, _, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return rec, err
	}
	c, err := p.Dial(ctx)
	if err != nil {
		return rec, err
	}
	defer c.Close()
	at := rec.LastVerifiedAt
	if t, err := time.Parse(time.RFC3339Nano, trimRegistered(rec.LaunchGuard)); err == nil {
		at = t
	}
	r, err := AwaitReadiness(ctx, c, ReadinessInput{Address: rec.TerminalAddress, Evidence: rec.Evidence, Registered: at, Timeout: timeouts.Ready, Poll: timeouts.poll(), Procs: p.procs()})
	if err != nil {
		return rec, err
	}
	rec.Observation.Readiness, rec.Observation.Program, rec.Observation.RawProgram, rec.Observation.ObservedAt = r.Readiness, r.Program, r.Raw, p.now()
	if r.Readiness == runtime.ReadinessIdentityUnknown {
		rec.Observation.Process = runtime.ProcessLost
	}
	if p.Sessions != nil {
		if all, err := p.Records.List(ctx, "", ""); err == nil {
			rec, _, _ = withConversation(p.Sessions, p.noticeMarks, all, rec)
		}
	}
	if rec, err = p.Records.Put(ctx, rec); err != nil {
		return rec, err
	}
	switch r.Readiness {
	case runtime.ReadinessNeedsAttention:
		return rec, refuse(CodeNeedsAttention, "%s", r.Reason)
	case runtime.ReadinessIdentityUnknown:
		return rec, refuse(CodeIdentityNotProven, "%s", r.Reason)
	}
	return rec, nil
}

func trimRegistered(guard string) string {
	const prefix = "registered "
	if len(guard) > len(prefix) && guard[:len(prefix)] == prefix {
		return guard[len(prefix):]
	}
	return ""
}

// ProbeReady is Matches for a ready execution.
func (p *Provider) ProbeReady(ctx context.Context, l Launch) (Probe, string, error) {
	rec, _, err := p.Records.ByIntent(ctx, l.Intent)
	if err != nil {
		return ProbeUnknown, "", err
	}
	if rec.Observation.Readiness == runtime.ReadinessReady {
		return ProbeMatches, "", nil
	}
	return ProbeAbsent, string(rec.Observation.Readiness), nil
}

// Enrollable checks the dispatch gate's launch half for every execution of a
// pair (spec sections 7 and 8): registered, its terminal running the
// registered process now, ready, and visible or pending. It returns the
// records in the order given.
func (p *Provider) Enrollable(ctx context.Context, intents []string) ([]runtime.Execution, error) {
	c, err := p.Dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []runtime.Execution
	for _, intent := range intents {
		rec, known, err := p.Records.ByIntent(ctx, intent)
		if err != nil {
			return nil, err
		}
		if !known || !registered(rec) {
			return nil, refuse(CodeIdentityNotProven, "launch %s has not registered", intent)
		}
		if _, ok, err := Proven(ctx, c, p.procs(), rec); err != nil {
			return nil, err
		} else if !ok {
			return nil, refuse(CodeIdentityNotProven, "terminal %s no longer runs execution %s", rec.TerminalAddress, rec.ID)
		}
		if rec.Observation.Readiness != runtime.ReadinessReady {
			return nil, refuse(CodeNeedsAttention, "execution %s is %s, not ready", rec.ID, rec.Observation.Readiness)
		}
		if !herdrapp.Visibility(rec.Observation.Visibility).DispatchAllowed() {
			return nil, refuse(CodeWaitingVisibility, "execution %s is %s in the app", rec.ID, rec.Observation.Visibility)
		}
		out = append(out, rec)
	}
	return out, nil
}
