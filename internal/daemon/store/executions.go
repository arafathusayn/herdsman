package store

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ExecutionSchema is the version of the execution record this binary writes.
const ExecutionSchema = 1

// Execution is one row of the executions table, in the account store (routes
// the operation journal starts) and in a route store alike (herdr-core backend
// spec, section 6). Empty strings and zero numbers are unknown values.
type Execution struct {
	ID                 string
	SchemaVersion      int
	Repo, Route        string
	Agent, Role        string
	TaskID             string
	LaunchIntent       string
	Generation         int64
	Backend            string
	EndpointID         string
	TerminalAddress    string
	Pane               string
	InitialCreatedMS   int64
	PID                int64
	ProcessStart       string
	ArgvDigest, Cwd    string
	HarnessKind        string
	HarnessVersion     string
	PolicyDigest       string
	LaunchGuard        string
	NativeConversation string
	DesiredState       string
	Process            string
	Readiness          string
	Program            string
	RawProgram         string
	Visibility         string
	App                string
	Orchestration      string
	TaskOutcome        string
	ObservedAt         int64
	AppBinding         string
	TUIBinding         string
	LastVerifiedAt     int64
	Created, Updated   int64
	// Experiment is the label of the experimental backend that wrote the
	// record (runtime.core/0.1.0); empty for a stable one. Written once.
	Experiment string
}

const executionColumns = `execution_id, schema_version, repo, route, agent, role, task_id, launch_intent_id, generation,
	backend, endpoint_id, terminal_address, pane, initial_created_ms, observed_pid, observed_process_start,
	argv_digest, cwd, harness_kind, harness_version, policy_digest, launch_guard_registration, native_conversation,
	desired_state, process_state, readiness, program_status, raw_program_status, visibility, app_state, orchestration,
	task_outcome, observed_at, app_binding, tui_binding, last_verified_at, created_at, updated_at, experiment`

// nullable turns an unknown value into SQL NULL.
func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

func scanExecution(r Row) Execution {
	return Execution{
		ID: r.Text(0), SchemaVersion: int(r.Int64(1)), Repo: r.Text(2), Route: r.Text(3), Agent: r.Text(4), Role: r.Text(5),
		TaskID: r.Text(6), LaunchIntent: r.Text(7), Generation: r.Int64(8), Backend: r.Text(9), EndpointID: r.Text(10),
		TerminalAddress: r.Text(11), Pane: r.Text(12), InitialCreatedMS: r.Int64(13), PID: r.Int64(14), ProcessStart: r.Text(15),
		ArgvDigest: r.Text(16), Cwd: r.Text(17), HarnessKind: r.Text(18), HarnessVersion: r.Text(19), PolicyDigest: r.Text(20),
		LaunchGuard: r.Text(21), NativeConversation: r.Text(22), DesiredState: r.Text(23), Process: r.Text(24),
		Readiness: r.Text(25), Program: r.Text(26), RawProgram: r.Text(27), Visibility: r.Text(28), App: r.Text(29),
		Orchestration: r.Text(30), TaskOutcome: r.Text(31), ObservedAt: r.Int64(32), AppBinding: r.Text(33),
		TUIBinding: r.Text(34), LastVerifiedAt: r.Int64(35), Created: r.Int64(36), Updated: r.Int64(37), Experiment: r.Text(38),
	}
}

// PutExecution records the execution of a launch intent. A repeated intent is
// the same execution: its id, generation, creation time and process evidence
// stay, and only what is observed moves. A new intent of the same agent gets
// the next generation. It runs in a transaction of its own.
func PutExecution(c Conn, e Execution, now int64) (Execution, error) {
	if e.ID == "" || e.LaunchIntent == "" || e.Agent == "" {
		return Execution{}, errors.New("store: an execution needs an id, a launch intent and an agent")
	}
	err := c.Tx(func(tx Conn) error {
		prev, found, err := executionWhere(tx, "launch_intent_id = ?", e.LaunchIntent)
		if err != nil {
			return err
		}
		if found {
			e.ID, e.Generation, e.Created, e.SchemaVersion, e.Experiment = prev.ID, prev.Generation, prev.Created, prev.SchemaVersion, prev.Experiment
			if prev.PID != 0 || e.PID == 0 {
				e.PID, e.ProcessStart = prev.PID, prev.ProcessStart
			}
			// Registration seals the effective command and policy. Later
			// observers cannot replace identity or erase launch evidence.
			if prev.LaunchGuard != "" || e.LaunchGuard == "" {
				e.LaunchGuard, e.ArgvDigest, e.PolicyDigest = prev.LaunchGuard, prev.ArgvDigest, prev.PolicyDigest
			}
			if prev.InitialCreatedMS != 0 {
				e.InitialCreatedMS = prev.InitialCreatedMS
			}
			if e.AppBinding == "" {
				e.AppBinding = prev.AppBinding
			} else if prev.AppBinding != "" {
				var old, next map[string]json.RawMessage
				if json.Unmarshal([]byte(prev.AppBinding), &old) == nil && json.Unmarshal([]byte(e.AppBinding), &next) == nil && next != nil {
					for _, key := range []string{"socket", "binary"} {
						if value, ok := old[key]; ok {
							next[key] = value
						}
					}
					if raw, err := json.Marshal(next); err == nil {
						e.AppBinding = string(raw)
					}
				}
			}
			// Conversation evidence, once recorded, is never cleared by a
			// writer that did not read it.
			if e.NativeConversation == "" {
				e.NativeConversation = prev.NativeConversation
			}
			e.Updated = now
			_, err := tx.Exec(`UPDATE executions SET endpoint_id = ?, terminal_address = ?, pane = ?, observed_pid = ?,
				observed_process_start = ?, harness_version = ?, native_conversation = ?, desired_state = ?, process_state = ?,
				readiness = ?, program_status = ?, raw_program_status = ?, visibility = ?, app_state = ?, orchestration = ?,
				task_outcome = ?, observed_at = ?, app_binding = ?, tui_binding = ?, last_verified_at = ?, updated_at = ?, initial_created_ms = ?, launch_guard_registration = ?, argv_digest = ?, policy_digest = ?
				WHERE execution_id = ?`,
				e.EndpointID, e.TerminalAddress, e.Pane, nullable(e.PID), nullable(e.ProcessStart), e.HarnessVersion,
				nullable(e.NativeConversation), e.DesiredState, e.Process, e.Readiness, e.Program, e.RawProgram, e.Visibility,
				e.App, e.Orchestration, e.TaskOutcome, e.ObservedAt, nullable(e.AppBinding), nullable(e.TUIBinding),
				nullable(e.LastVerifiedAt), e.Updated, nullable(e.InitialCreatedMS), e.LaunchGuard, e.ArgvDigest, e.PolicyDigest, e.ID)
			return err
		}
		var last int64
		if err := tx.Query("SELECT COALESCE(MAX(generation), 0) FROM executions WHERE repo = ? AND route = ? AND agent = ?",
			[]any{e.Repo, e.Route, e.Agent}, func(r Row) error { last = r.Int64(0); return nil }); err != nil {
			return err
		}
		e.Generation, e.SchemaVersion, e.Created, e.Updated = last+1, ExecutionSchema, now, now
		_, err = tx.Exec(`INSERT INTO executions(`+executionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.SchemaVersion, e.Repo, e.Route, e.Agent, e.Role, e.TaskID, e.LaunchIntent, e.Generation,
			e.Backend, e.EndpointID, e.TerminalAddress, e.Pane, nullable(e.InitialCreatedMS), nullable(e.PID), nullable(e.ProcessStart),
			e.ArgvDigest, e.Cwd, e.HarnessKind, e.HarnessVersion, e.PolicyDigest, e.LaunchGuard, nullable(e.NativeConversation),
			e.DesiredState, e.Process, e.Readiness, e.Program, e.RawProgram, e.Visibility, e.App, e.Orchestration,
			e.TaskOutcome, e.ObservedAt, nullable(e.AppBinding), nullable(e.TUIBinding), nullable(e.LastVerifiedAt), e.Created, e.Updated, e.Experiment)
		return err
	})
	if err != nil {
		return Execution{}, fmt.Errorf("store: record execution: %w", err)
	}
	return e, nil
}

func executionWhere(c Conn, where string, args ...any) (Execution, bool, error) {
	var out Execution
	found := false
	err := c.Query("SELECT "+executionColumns+" FROM executions WHERE "+where, args, func(r Row) error {
		out, found = scanExecution(r), true
		return nil
	})
	return out, found, err
}

// ExecutionByIntent reads the execution of one launch intent.
func ExecutionByIntent(c Conn, intent string) (Execution, bool, error) {
	return executionWhere(c, "launch_intent_id = ?", intent)
}

// ExecutionByID reads one execution.
func ExecutionByID(c Conn, id string) (Execution, bool, error) {
	return executionWhere(c, "execution_id = ?", id)
}

// Executions lists the executions of one route, or of every route when route
// is empty, newest first.
func Executions(c Conn, repo, route string) ([]Execution, error) {
	q, args := "SELECT "+executionColumns+" FROM executions", []any{}
	if route != "" {
		q, args = q+" WHERE repo = ? AND route = ?", []any{repo, route}
	}
	var out []Execution
	err := c.Query(q+" ORDER BY created_at DESC, generation DESC, execution_id", args, func(r Row) error {
		out = append(out, scanExecution(r))
		return nil
	})
	return out, err
}

// Installation default sources, as the account store records them.
const (
	DefaultFromExistingStore = "existing-store"
	DefaultFromNewStore      = "new-store"
	DefaultSet               = "set"
)

// InstallationDefault reads the installation default backend, written once by
// the account migration and changed only by SetInstallationDefault.
func InstallationDefault(c Conn) (backend, source string, writtenAt int64, err error) {
	found := false
	err = c.Query("SELECT v, source, written_at FROM installation WHERE k = 'runtime.backend'", nil, func(r Row) error {
		backend, source, writtenAt, found = r.Text(0), r.Text(1), r.Int64(2), true
		return nil
	})
	if err == nil && !found {
		err = errors.New("store: the account has no installation default")
	}
	return backend, source, writtenAt, err
}

// SetInstallationDefault changes the installation default and returns the one
// it replaced. The table's check refuses a backend it does not know.
func SetInstallationDefault(c Conn, backend string, nowMS int64) (string, error) {
	var previous string
	err := c.Tx(func(tx Conn) error {
		if err := tx.Query("SELECT v FROM installation WHERE k = 'runtime.backend'", nil, func(r Row) error { previous = r.Text(0); return nil }); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO installation(k, v, source, written_at) VALUES ('runtime.backend', ?, ?, ?)
			ON CONFLICT(k) DO UPDATE SET v = excluded.v, source = excluded.source, written_at = excluded.written_at`, backend, DefaultSet, nowMS)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("store: set the installation default: %w", err)
	}
	return previous, nil
}

// RouteRuntime reads a route store's backend and endpoint; found is false
// before its first execution is recorded.
func RouteRuntime(c Conn) (backend, endpoint string, found bool, err error) {
	err = c.Query("SELECT backend, endpoint_id FROM route_runtime WHERE one = 1", nil, func(r Row) error {
		backend, endpoint, found = r.Text(0), r.Text(1), true
		return nil
	})
	return backend, endpoint, found, err
}

// BindRouteRuntime writes a route store's backend and endpoint once. A second
// call with another backend fails: the route keeps the one it has.
func BindRouteRuntime(c Conn, backend, endpoint string, now int64) error {
	return c.Tx(func(tx Conn) error {
		got, _, found, err := RouteRuntime(tx)
		if err != nil {
			return err
		}
		if found {
			if got != backend {
				return opErr("backend_migration_required", "the route runs on %s, not %s", got, backend)
			}
			return nil
		}
		_, err = tx.Exec("INSERT INTO route_runtime(one, backend, endpoint_id, written_at) VALUES (1, ?, ?, ?)", backend, endpoint, now)
		return err
	})
}
