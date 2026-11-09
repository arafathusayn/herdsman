package automations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// DefaultTimeout bounds an action whose rule sets none, since actions run in
// event order and a stuck one would hold back the rest.
const DefaultTimeout = 5 * time.Minute

// Event is one thing that happened. Key is stable across repeated deliveries.
// Values come from the event's producer and are never trusted as shell text.
type Event struct {
	Key     string
	Kind    string
	Project string
	Values  map[string]string
}

// Status says how one rule's firing ended.
type Status string

// The outcomes of a firing.
const (
	StatusRan         Status = "ran"
	StatusWouldRun    Status = "would-run"
	StatusUnapproved  Status = "unapproved"
	StatusDenied      Status = "denied"
	StatusDuplicate   Status = "duplicate"
	StatusFailed      Status = "failed"
	StatusUnavailable Status = "unavailable"
	StatusError       Status = "error"
)

// Outcome reports one firing; it is also the log's line.
type Outcome struct {
	Experimental string    `json:"experimental"`
	Time         time.Time `json:"time"`
	Event        string    `json:"event"`
	Key          string    `json:"key"`
	Project      string    `json:"project"`
	Kind         Kind      `json:"kind"`
	Checksum     string    `json:"checksum"`
	Status       Status    `json:"status"`
	Rendered     string    `json:"rendered,omitempty"`
	Exit         int       `json:"exit"`
	TimedOut     bool      `json:"timed_out,omitempty"`
	Log          string    `json:"log,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// Engine fires the configured rules. Set its fields before the first Fire.
type Engine struct {
	Config Config
	// Source, when set, replaces Config, Policy and Dir: it reads the event's
	// project at each firing, so an edit needs its new approval at once, and
	// the action starts in the project.
	Source    func(project string) (Config, policy.Policy, error)
	Approvals *Approvals
	Policy    policy.Policy
	// StateDir holds the log and the record of what has fired.
	StateDir string
	// Dir is where shell actions start.
	Dir string
	// Stderr receives the experimental marker.
	Stderr   io.Writer
	Timeout  time.Duration
	Builtins map[string]Builtin
	Plugins  PluginRunner
	// DryRun reports what would happen and changes nothing.
	DryRun bool

	marker sync.Once
}

// Fire runs the rules for the event. A failure is an outcome, never a panic or
// a skipped check: every path that cannot prove approval ends without running.
func (e *Engine) Fire(ctx context.Context, ev Event) []Outcome {
	config, pol := e.Config, e.Policy
	if e.Source != nil {
		var err error
		if config, pol, err = e.Source(ev.Project); err != nil {
			return e.refuseAll(ev, err)
		}
	}
	if !config.Enabled || len(config.Rules) == 0 {
		return nil
	}
	e.marker.Do(func() {
		if e.Stderr != nil {
			fmt.Fprintf(e.Stderr, "herdsman: automations is experimental (automations/%s) and can change between minor releases\n", Version)
		}
	})
	var outcomes []Outcome
	for _, rule := range config.Rules {
		if rule.Event != ev.Kind {
			continue
		}
		outcome := e.fireRule(ctx, rule, ev, pol)
		if !e.DryRun {
			e.record(&outcome)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// refuseAll reports a project whose settings cannot be read. No rule is known,
// so none can run.
func (e *Engine) refuseAll(ev Event, err error) []Outcome {
	out := Outcome{Experimental: "automations/" + Version, Time: time.Now().UTC(), Event: ev.Kind, Key: ev.Key, Project: ev.Project, Exit: -1}
	out.failWith(StatusError, err)
	if !e.DryRun {
		e.record(&out)
	}
	return []Outcome{out}
}

func (e *Engine) fireRule(ctx context.Context, rule Rule, ev Event, pol policy.Policy) Outcome {
	out := Outcome{
		Experimental: "automations/" + Version, Time: time.Now().UTC(), Event: ev.Kind, Key: ev.Key,
		Project: ev.Project, Kind: rule.Kind, Checksum: Checksum(rule), Exit: -1,
	}
	approved, err := e.Approvals.approved(ev.Project, out.Checksum)
	if err != nil {
		return out.failWith(StatusError, err)
	}
	if !approved {
		out.Status = StatusUnapproved
		return out
	}
	if ev.Key == "" {
		return out.failWith(StatusError, errors.New("automations: the event has no key to deduplicate by"))
	}
	shell, err := e.prepare(rule, ev, pol, &out)
	if err != nil {
		return out
	}
	if e.DryRun {
		out.Status = StatusWouldRun
		return out
	}
	claimed, err := e.claim(ev.Key, out.Checksum)
	if err != nil {
		return out.failWith(StatusError, err)
	}
	if !claimed {
		out.Status = StatusDuplicate
		return out
	}
	e.run(ctx, rule, ev, shell, &out)
	return out
}

func (o *Outcome) failWith(status Status, err error) Outcome {
	o.Status, o.Error = status, err.Error()
	return *o
}

// prepare renders a shell action and applies [policy] deny to the result.
func (e *Engine) prepare(rule Rule, ev Event, pol policy.Policy, out *Outcome) (shellCommand, error) {
	out.Rendered = rule.Body
	if rule.Kind != KindShell {
		return shellCommand{}, nil
	}
	shell, err := render(rule, ev)
	if err != nil {
		out.failWith(StatusError, err)
		return shellCommand{}, err
	}
	out.Rendered = shell.text
	if err := pol.CheckCommand(shell.text); err != nil {
		out.failWith(StatusDenied, err)
		return shellCommand{}, err
	}
	return shell, nil
}

// workdir is where an event's shell action starts.
func (e *Engine) workdir(ev Event) string {
	if e.Source != nil {
		return ev.Project
	}
	return e.Dir
}

func (e *Engine) folder() string { return filepath.Join(e.StateDir, "automations") }

// claim takes the only right to act on this delivery. Delivery is at least
// once, and an action can have effects that cannot be undone, so the claim
// comes before the action and a crash in between does not repeat it.
func (e *Engine) claim(key, checksum string) (bool, error) {
	dir := filepath.Join(e.folder(), "fired")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("automations: state folder: %w", err)
	}
	sum := sha256.Sum256([]byte(key + "\x00" + checksum))
	file, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("automations: claim: %w", err)
	}
	return true, file.Close()
}

// record appends the outcome to the log. A log that cannot be written is said
// on stderr; it does not undo an action that already ran.
func (e *Engine) record(out *Outcome) {
	err := func() error {
		if err := os.MkdirAll(e.folder(), 0o700); err != nil {
			return err
		}
		// Marshal cannot fail: the fields are strings, numbers and a current time.
		line, _ := json.Marshal(out)
		file, err := os.OpenFile(filepath.Join(e.folder(), "log.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = file.Write(append(line, '\n'))
		return errors.Join(err, file.Close())
	}()
	if err != nil && e.Stderr != nil {
		fmt.Fprintf(e.Stderr, "herdsman: automations log: %v\n", err)
	}
}
