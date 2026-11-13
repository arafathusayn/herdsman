package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/runtime"
)

// Records is the durable home of execution records. The daemon backs it with
// its store; the uniqueness of a launch intent and of a running process's
// (pid, start time) is the store's.
type Records interface {
	// Put writes an execution. A repeated launch intent is the same
	// execution: its id, generation and creation time stay.
	Put(ctx context.Context, e runtime.Execution) (runtime.Execution, error)
	// ByIntent reads the execution of one launch intent.
	ByIntent(ctx context.Context, intent string) (runtime.Execution, bool, error)
	// List reads every execution, or the executions of one route.
	List(ctx context.Context, repo, route string) ([]runtime.Execution, error)
}

// MemRecords keeps records in memory, with the store's rules: one execution
// per intent, generations counted per agent. Tests use it.
type MemRecords struct {
	mu   sync.Mutex
	rows []runtime.Execution
}

// ErrRecordShape is a record without an id, an intent or an agent.
var ErrRecordShape = errors.New("core: an execution needs an id, a launch intent and an agent")

// Put implements Records.
func (m *MemRecords) Put(_ context.Context, e runtime.Execution) (runtime.Execution, error) {
	if e.ID == "" || e.LaunchIntent == "" || e.Agent == "" {
		return runtime.Execution{}, ErrRecordShape
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for i, r := range m.rows {
		if r.LaunchIntent == e.LaunchIntent {
			e.ID, e.Generation, e.Created, e.SchemaVersion, e.Experiment = r.ID, r.Generation, r.Created, r.SchemaVersion, r.Experiment
			if r.Evidence.PID != 0 || e.Evidence.PID == 0 {
				e.Evidence = r.Evidence
			}
			if r.LaunchGuard != "" || e.LaunchGuard == "" {
				e.LaunchGuard, e.ArgvDigest, e.PolicyDigest = r.LaunchGuard, r.ArgvDigest, r.PolicyDigest
			}
			if e.App == nil {
				e.App = r.App
			} else if r.App != nil && r.App.Socket != "" {
				app := *e.App
				app.Socket, app.Binary = r.App.Socket, r.App.Binary
				e.App = &app
			}
			if r.InitialCreatedMS != 0 {
				e.InitialCreatedMS = r.InitialCreatedMS
			}
			if e.Conversation == nil {
				e.Conversation = r.Conversation
			}
			e.Updated = now
			m.rows[i] = e
			return e, nil
		}
	}
	var last int64
	for _, r := range m.rows {
		if r.Repo == e.Repo && r.Route == e.Route && r.Agent == e.Agent && r.Generation > last {
			last = r.Generation
		}
	}
	if e.Evidence.PID != 0 && e.Desired == runtime.DesiredRunning {
		for _, r := range m.rows {
			if r.Evidence == e.Evidence && r.Desired == runtime.DesiredRunning {
				return runtime.Execution{}, errors.New("core: process " + strconv.Itoa(e.Evidence.PID) + " is already a running execution")
			}
		}
	}
	e.Generation, e.SchemaVersion, e.Created, e.Updated = last+1, runtime.SchemaVersion, now, now
	m.rows = append(m.rows, e)
	return e, nil
}

// ByIntent implements Records.
func (m *MemRecords) ByIntent(_ context.Context, intent string) (runtime.Execution, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.LaunchIntent == intent {
			return r, true, nil
		}
	}
	return runtime.Execution{}, false, nil
}

// List implements Records.
func (m *MemRecords) List(_ context.Context, repo, route string) ([]runtime.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []runtime.Execution
	for _, r := range m.rows {
		if route == "" || (r.Repo == repo && r.Route == route) {
			out = append(out, r)
		}
	}
	return slices.Clone(out), nil
}

// running reports an execution Herdsman still wants running on core.
func running(e runtime.Execution) bool {
	return e.Backend == runtime.BackendCore && e.Desired == runtime.DesiredRunning
}

// registered reports an execution with process evidence.
func registered(e runtime.Execution) bool { return e.Evidence.PID > 0 && e.Evidence.Start != "" }
