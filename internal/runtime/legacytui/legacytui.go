// Package legacytui is the Herdr TUI behind the runtime contract: every
// operation is the call the route verbs made before backends existed, over the
// same internal/herdr client, so a route on this backend behaves as it did.
// An agent is found by its herdr name or pane; its identity is herdr's agent
// record (inbox.LiveIdentity), not process evidence.
package legacytui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Provider is the Herdr TUI on one server socket.
type Provider struct {
	socket string
	c      *herdr.Client
	now    func() time.Time
}

// New is the provider on c, whose server listens on socket.
func New(socket string, c *herdr.Client) *Provider {
	return &Provider{socket: socket, c: c, now: time.Now}
}

// Open is the registry factory: the provider on a socket path.
func Open(socket string) (runtime.Provider, error) {
	return New(socket, herdr.New(socket)), nil
}

// Register adds the TUI provider to r.
func Register(r runtime.Registry) { r[runtime.BackendTUI] = Open }

// Client is the herdr client, for the route steps that are still TUI-only:
// workspaces, labelled panes and the prelude typed into a pane.
func (p *Provider) Client() *herdr.Client { return p.c }

// ClientOf returns the herdr client of a TUI provider, or nil for any other.
func ClientOf(p runtime.Provider) *herdr.Client {
	if t, ok := p.(*Provider); ok {
		return t.c
	}
	return nil
}

// Verifier is the identity verifier of this backend.
func (p *Provider) Verifier() inbox.Verifier { return inbox.TUIVerifier{Client: p.c} }

// Backend implements runtime.Provider.
func (p *Provider) Backend() string { return runtime.BackendTUI }

// Methods are the herdr methods this provider calls.
var Methods = []string{"ping", "agent.get", "agent.start", "agent.prompt", "pane.read", "pane.send_input", "pane.close", "tab.focus"}

// Capabilities implements runtime.Provider: the server's protocol, checked.
func (p *Provider) Capabilities(ctx context.Context) (runtime.Capabilities, error) {
	pong, err := p.c.Check(ctx)
	if err != nil {
		return runtime.Capabilities{}, err
	}
	return runtime.Capabilities{
		Backend: runtime.BackendTUI, EndpointID: p.socket, Protocol: "herdr " + pong.Version,
		Methods: Methods, TypedInput: true,
	}, nil
}

// IsNotFound reports herdr's answer that a pane or an agent does not exist.
func IsNotFound(err error) bool {
	var he *herdr.Error
	return errors.As(err, &he) && strings.Contains(he.Code, "not_found")
}

func (p *Provider) inspection(r herdr.AgentRecord) runtime.Inspection {
	kind := ""
	if r.Agent.Agent != nil {
		kind = *r.Agent.Agent
	}
	raw := string(r.Status)
	readiness := runtime.ReadinessWaiting
	if r.InteractiveReady && !r.LaunchPending {
		readiness = runtime.ReadinessReady
	}
	return runtime.Inspection{
		Exists: true, Kind: kind, Pane: r.PaneID,
		Observation: runtime.Observation{
			Process: runtime.ProcessLive, Readiness: readiness, Program: runtime.ProgramOf(raw), RawProgram: raw,
			Visibility: runtime.VisibilityVerified, Orchestration: runtime.OrchestrationActive, ObservedAt: p.now(),
		},
	}
}

func target(t runtime.Target) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Pane
}

// Inspect implements runtime.Provider. A missing agent is Exists false with
// no error; herdr's other errors come back as they are.
func (p *Provider) Inspect(ctx context.Context, t runtime.Target) (runtime.Inspection, error) {
	r, err := p.c.AgentGet(ctx, target(t))
	if err != nil {
		if IsNotFound(err) {
			return runtime.Inspection{Observation: runtime.Observation{Process: runtime.ProcessFinished, Readiness: runtime.ReadinessIdentityUnknown,
				Program: runtime.ProgramUnknown, Visibility: runtime.VisibilityMissing, Orchestration: runtime.OrchestrationActive, ObservedAt: p.now()}}, nil
		}
		return runtime.Inspection{}, err
	}
	return p.inspection(r), nil
}

// get reads the agent with herdr's error kept, for the callers that report it.
func (p *Provider) get(ctx context.Context, t runtime.Target) (herdr.AgentRecord, error) {
	return p.c.AgentGet(ctx, target(t))
}

// EnsureExecution implements runtime.Provider with the TUI's rule from before
// backends: an agent of the intent's name and kind (or of no reported kind) is
// adopted; anything else, an agent of another kind or an answer that tells
// nothing, goes on to herdr's agent start in the placed pane, and herdr
// refuses a name that is taken. A start that fails after a kind mismatch is
// reported as a conflict.
func (p *Provider) EnsureExecution(ctx context.Context, in runtime.Intent) (runtime.Ensured, error) {
	conflict := ""
	if r, err := p.c.AgentGet(ctx, in.Name); err == nil {
		got := p.inspection(r)
		if got.Kind == "" || got.Kind == in.Kind {
			return runtime.Ensured{Outcome: runtime.OutcomeAdopted, Execution: got}, nil
		}
		conflict = "an agent named " + in.Name + " runs " + got.Kind + ", not " + in.Kind
	}
	pane, args := in.Pane, in.Args
	if in.Place != nil {
		var err error
		if pane, args, err = in.Place(ctx); err != nil {
			return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
		}
	}
	a, err := p.c.AgentStart(ctx, in.Name, in.Kind, pane, args, int(in.Timeout.Milliseconds()))
	if err != nil {
		if conflict != "" {
			return runtime.Ensured{Outcome: runtime.OutcomeConflict, Detail: conflict}, err
		}
		return runtime.Ensured{Outcome: runtime.OutcomeUnknown, Detail: err.Error()}, err
	}
	got := p.inspection(herdr.AgentRecord{Agent: a})
	if got.Pane == "" {
		got.Pane = pane
	}
	if got.Kind == "" {
		got.Kind = in.Kind
	}
	return runtime.Ensured{Outcome: runtime.OutcomeCreated, Execution: got}, nil
}

// Observe implements runtime.Provider. The TUI keeps no change log for this
// contract, so every call is a resync from a fresh inspection.
func (p *Provider) Observe(ctx context.Context, t runtime.Target, _ string) (runtime.Snapshot, error) {
	cur, err := p.Inspect(ctx, t)
	if err != nil {
		return runtime.Snapshot{}, err
	}
	return runtime.Snapshot{Epoch: "tui:" + p.now().UTC().Format(time.RFC3339Nano), Current: cur}, nil
}

// Read implements runtime.Provider with herdr's pane.read.
func (p *Provider) Read(ctx context.Context, t runtime.Target, r runtime.ReadRequest) (runtime.Text, error) {
	pane := t.Pane
	if pane == "" {
		a, err := p.get(ctx, t)
		if err != nil {
			return runtime.Text{}, err
		}
		pane = a.PaneID
	}
	read, err := p.c.ReadPane(ctx, pane, herdr.ReadOptions{Lines: r.Lines, Visible: !r.Recent})
	if err != nil {
		return runtime.Text{}, err
	}
	text, cut := read.Text, read.Truncated
	if r.MaxBytes > 0 && len(text) > r.MaxBytes {
		text, cut = text[len(text)-r.MaxBytes:], true
	}
	return runtime.Text{Text: text, Truncated: cut}, nil
}

// Deliver implements runtime.Provider: the text is typed as the agent's prompt
// and submitted, as the TUI routes always did.
func (p *Provider) Deliver(ctx context.Context, t runtime.Target, d runtime.Delivery) (runtime.Delivered, error) {
	return runtime.Delivered{}, p.c.AgentPrompt(ctx, target(t), d.Text)
}

// SendKey presses one key in a pane. It is the TUI's own input path, kept for
// the steps that have no runtime operation yet (a service pane's ctrl+c).
func (p *Provider) SendKey(ctx context.Context, pane, key string) error {
	return p.c.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane, "text": "", "keys": []string{key}}, nil)
}

// Interrupt implements runtime.Provider: the harness's interrupt key, once. An
// agent herdr cannot find is an error; a key that does not arrive is Sent
// false, with the pane and the status that were read.
func (p *Provider) Interrupt(ctx context.Context, t runtime.Target) (runtime.Interrupted, error) {
	r, err := p.get(ctx, t)
	if err != nil {
		return runtime.Interrupted{}, err
	}
	out := runtime.Interrupted{Pane: r.PaneID, Program: runtime.ProgramOf(string(r.Status)), RawProgram: string(r.Status)}
	out.Sent = p.SendKey(ctx, r.PaneID, "esc") == nil
	return out, nil
}

// Stop implements runtime.Provider: the agent's pane is closed. A pane herdr
// no longer has is already stopped.
func (p *Provider) Stop(ctx context.Context, t runtime.Target) (runtime.Stopped, error) {
	pane := t.Pane
	if pane == "" {
		r, err := p.get(ctx, t)
		if IsNotFound(err) {
			return runtime.Stopped{Already: true}, nil
		}
		if err != nil {
			return runtime.Stopped{}, err
		}
		pane = r.PaneID
	}
	if err := p.c.Call(ctx, "pane.close", map[string]any{"pane_id": pane}, nil); err != nil {
		if IsNotFound(err) {
			return runtime.Stopped{Already: true}, nil
		}
		return runtime.Stopped{}, err
	}
	return runtime.Stopped{}, nil
}

// EnsureVisible implements runtime.Provider: a TUI pane is its own view.
func (p *Provider) EnsureVisible(ctx context.Context, t runtime.Target) (runtime.Visibility, error) {
	got, err := p.Inspect(ctx, t)
	if err != nil {
		return runtime.VisibilityUnverified, err
	}
	if !got.Exists {
		return runtime.VisibilityMissing, nil
	}
	return runtime.VisibilityVerified, nil
}

// Show implements runtime.Provider: the agent's tab is focused.
func (p *Provider) Show(ctx context.Context, t runtime.Target) error {
	r, err := p.get(ctx, t)
	if err != nil {
		return err
	}
	return p.c.TabFocus(ctx, r.TabID)
}

// WaitProgram waits until the agent reports one of the statuses. It is the
// TUI's status subscription, used by the stop's last call.
func (p *Provider) WaitProgram(ctx context.Context, t runtime.Target, want []runtime.Program, timeout time.Duration) (runtime.Program, error) {
	statuses := make([]herdr.Status, len(want))
	for i, w := range want {
		statuses[i] = herdr.Status(w)
	}
	got, err := p.c.WaitAgentStatus(ctx, target(t), statuses, timeout)
	return runtime.Program(got), err
}

var _ runtime.Provider = (*Provider)(nil)
