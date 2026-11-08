package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// PolicyGate is the admission check a new operation passes before it starts.
type PolicyGate struct {
	// Load returns the policy in force, already composed.
	Load func() (policy.Policy, error)
	// Usage returns what the operation's task and route have consumed.
	Usage func(policy.Operation) (policy.Usages, error)
	// Now is the clock; nil means the system clock.
	Now func() time.Time
}

// Admit returns nil when op may start. It fails closed: when the policy or the
// usage cannot be read, the limit cannot be proven safe, so nothing starts. A
// refusal is a human boundary, because retrying cannot help until a person
// changes the policy.
func (g *PolicyGate) Admit(op policy.Operation) *APIError {
	if g == nil || g.Load == nil || g.Usage == nil {
		return Errf("internal", "policy gate is not configured; operation %s refused", op.Kind)
	}
	loaded, err := g.Load()
	if err != nil {
		return Errf("internal", "policy could not be read; operation %s refused: %v", op.Kind, err)
	}
	usage, err := g.Usage(op)
	if err != nil {
		return Errf("internal", "usage could not be read; operation %s refused: %v", op.Kind, err)
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	if err := policy.Admit(loaded, op, usage, now()); err != nil {
		var refusal *policy.Refusal
		if errors.As(err, &refusal) {
			return Errf("human_boundary", "%s", refusal.Message)
		}
		return Errf("internal", "policy check failed; operation %s refused: %v", op.Kind, err)
	}
	return nil
}

// usageSchemaVersion versions the lines of the usage log.
const usageSchemaVersion = "1.0.0"

// UsageRecord is one measurement a harness reported after a turn.
type UsageRecord struct {
	Schema     string    `json:"schema"`
	At         time.Time `json:"at"`
	Project    string    `json:"project,omitempty"`
	Task       string    `json:"task,omitempty"`
	Route      string    `json:"route,omitempty"`
	Harness    string    `json:"harness,omitempty"`
	Tokens     int64     `json:"tokens"`
	CostMicros int64     `json:"cost_micros"`
}

// AppendUsage adds one record to the usage log as a single write, so readers
// and concurrent writers never see half a record.
func AppendUsage(path string, record UsageRecord) error {
	if record.Tokens < 0 || record.CostMicros < 0 {
		return fmt.Errorf("usage record has a negative amount: %d tokens, %d micros", record.Tokens, record.CostMicros)
	}
	record.Schema = usageSchemaVersion
	// A struct of strings, integers and a time always marshals.
	line, _ := json.Marshal(record)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("usage log folder: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("usage log: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("usage log: %w", err)
	}
	return nil
}
