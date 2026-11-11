package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/sdk/config"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"github.com/arafathusayn/herdsman/sdk/version"
)

// runtimeService is the daemon's RuntimeService: the execution records, the
// installation default and the backend doctor. It starts nothing.
type runtimeService struct {
	d *Daemon
}

// runtimeFailure is the wire form of a runtime refusal: its code travels as a
// core.v1.Error detail, so a caller branches on backend_migration_required or
// backend_unavailable, never on the words.
func runtimeFailure(err error) error {
	code := runtime.CodeOf(err)
	if code == "" {
		return rpc.Errorf(rpc.Internal, "%v", err)
	}
	wire := rpc.FailedPrecondition
	if code == runtime.CodeUnknownBackend {
		wire = rpc.InvalidArgument
	}
	failure := rpc.Errorf(wire, "%s", err.Error())
	if derr := failure.AddDetail(&corev1.Error{Code: code, Message: err.Error()}); derr != nil {
		return rpc.Errorf(rpc.Internal, "cannot encode the failure")
	}
	return failure
}

// tuiRef is the runtime reference of a Herdr TUI agent known by its pane: the
// same pane, never an invented execution. No pane is no reference.
func tuiRef(pane string) *corev1.RuntimeRef {
	if pane == "" {
		return nil
	}
	return &corev1.RuntimeRef{Backend: runtime.BackendTUI, Pane: pane}
}

// installationDefault reads the account's installation default.
func (d *Daemon) installationDefault() (backend, source string, at int64, err error) {
	err = d.ops.Read(func(c store.Conn) error {
		var rerr error
		backend, source, at, rerr = store.InstallationDefault(c)
		return rerr
	})
	return backend, source, at, err
}

// runtimeRegistry is the providers this daemon runs.
func (d *Daemon) runtimeRegistry() runtime.Registry { return route.DefaultRuntime() }

// configBackend is runtime.backend of the config file a run in repo uses: the
// file named, or the one the repository finds. An unknown key in [runtime]
// stops here.
func configBackend(repo, configPath string) (backend, path string, err error) {
	if repo == "" && configPath == "" {
		return "", "", nil
	}
	if configPath != "" && !filepath.IsAbs(configPath) && repo != "" {
		configPath = filepath.Join(repo, configPath)
	}
	result, err := config.Load(config.Options{ConfigPath: configPath, RepoRoot: repo})
	if err != nil {
		return "", "", err
	}
	backend = result.Config.GetRuntime().GetBackend()
	if backend != "" {
		path = result.ProjectPath
		if configPath != "" {
			path = configPath
		}
		if path == "" {
			path = result.UserPath
		}
	}
	return backend, path, nil
}

// selectBackend resolves the backend of one run in the order of the spec's
// section 5. persisted is the backend an existing route started on; empty for
// a new route. Every launch path calls it: up, task launches and the doctor.
func (d *Daemon) selectBackend(repo, configPath, flag, persisted string) (runtime.Selection, error) {
	cfg, cfgPath, err := configBackend(repo, configPath)
	if err != nil {
		return runtime.Selection{}, &runtime.Error{Code: runtime.CodeUnknownBackend, Msg: "the configuration: " + err.Error()}
	}
	installed, _, _, err := d.installationDefault()
	if err != nil {
		return runtime.Selection{}, err
	}
	return runtime.Resolve(runtime.Inputs{Flag: flag, Config: cfg, ConfigPath: cfgPath, Installation: installed, Persisted: persisted})
}

// launchBackend resolves a run's backend and refuses one this build cannot
// run. There is no fallback: the refusal names the Herdr TUI.
func (d *Daemon) launchBackend(repo, configPath, flag, persisted string) (runtime.Selection, error) {
	sel, err := d.selectBackend(repo, configPath, flag, persisted)
	if err != nil {
		return sel, err
	}
	return sel, d.runtimeRegistry().Available(sel.Backend)
}

// taskBackend refuses a task launch in repo on a backend this build cannot
// run. A task has no backend flag; its repository's config file and then the
// installation default decide.
func (d *Daemon) taskBackend(_ context.Context, repo string) error {
	_, err := d.launchBackend(repo, "", "", "")
	return err
}

// recordExecution writes an execution record into the account store, beside
// the journal of the route that launched it.
func (d *Daemon) recordExecution(_ context.Context, e runtime.Execution) error {
	row := executionRow(e)
	return d.ops.Read(func(c store.Conn) error {
		_, err := store.PutExecution(c, row, d.now().UnixMilli())
		return err
	})
}

func jsonOf(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func executionRow(e runtime.Execution) store.Execution {
	row := store.Execution{
		ID: e.ID, SchemaVersion: e.SchemaVersion, Repo: e.Repo, Route: e.Route, Agent: e.Agent, Role: e.Role, TaskID: e.TaskID,
		LaunchIntent: e.LaunchIntent, Generation: e.Generation, Backend: e.Backend, EndpointID: e.EndpointID,
		TerminalAddress: e.TerminalAddress, Pane: e.Pane, InitialCreatedMS: e.InitialCreatedMS, PID: int64(e.Evidence.PID),
		ProcessStart: e.Evidence.Start, ArgvDigest: e.ArgvDigest, Cwd: e.Cwd, HarnessKind: e.HarnessKind,
		HarnessVersion: e.HarnessVersion, PolicyDigest: e.PolicyDigest, LaunchGuard: e.LaunchGuard, DesiredState: string(e.Desired),
		Process: string(e.Observation.Process), Readiness: string(e.Observation.Readiness), Program: string(e.Observation.Program),
		RawProgram: e.Observation.RawProgram, Visibility: string(e.Observation.Visibility), App: string(e.Observation.App),
		Orchestration: string(e.Observation.Orchestration), TaskOutcome: string(e.Observation.TaskOutcome),
		ObservedAt: millis(e.Observation.ObservedAt), LastVerifiedAt: millis(e.LastVerifiedAt),
	}
	if e.Conversation != nil {
		row.NativeConversation = jsonOf(e.Conversation)
	}
	if e.App != nil {
		row.AppBinding = jsonOf(e.App)
	}
	if e.TUI != nil {
		row.TUIBinding = jsonOf(e.TUI)
	}
	return row
}

// executionMessage is the wire form of a stored execution.
func executionMessage(e store.Execution) *daemonv1.Execution {
	out := &daemonv1.Execution{
		Ref: &corev1.RuntimeRef{Backend: e.Backend, ExecutionId: e.ID, Generation: e.Generation, EndpointId: e.EndpointID,
			TerminalAddress: e.TerminalAddress, Pane: e.Pane},
		Route: &corev1.RouteId{Value: e.Route}, Repo: e.Repo, Agent: e.Agent, Role: e.Role, TaskId: e.TaskID,
		LaunchIntentId: e.LaunchIntent, HarnessKind: e.HarnessKind, HarnessVersion: e.HarnessVersion, ObservedPid: e.PID,
		ObservedProcessStart: e.ProcessStart, DesiredState: e.DesiredState, LastVerifiedAt: e.LastVerifiedAt, Created: e.Created,
		Source: "record",
		Observation: &daemonv1.ExecutionObservation{Process: e.Process, Readiness: e.Readiness, Program: e.Program,
			RawProgramStatus: e.RawProgram, Visibility: e.Visibility, App: e.App, Orchestration: e.Orchestration,
			TaskOutcome: e.TaskOutcome, ObservedAt: e.ObservedAt},
	}
	var app runtime.AppBinding
	if e.AppBinding != "" && json.Unmarshal([]byte(e.AppBinding), &app) == nil {
		out.AppBinding = &daemonv1.AppBinding{CoreId: app.CoreID, SpaceId: app.SpaceID, TabId: app.TabID, PaneId: app.PaneID, Visibility: string(app.Visibility)}
	}
	var tui runtime.TUIBinding
	if e.TUIBinding != "" && json.Unmarshal([]byte(e.TUIBinding), &tui) == nil {
		out.TuiBinding = &daemonv1.TuiBinding{Socket: tui.Socket, Pane: tui.Pane}
	}
	return out
}

// latestStarts is the newest journaled start of each route, oldest route
// first; a route whose newest start was deleted is left out.
func (d *Daemon) latestStarts() ([]*ops.Operation, error) {
	all, err := d.ops.List()
	if err != nil {
		return nil, err
	}
	byKey := map[string][]*ops.Operation{}
	var keys []string
	for _, op := range all {
		if op.Kind != route.OperationKind {
			continue
		}
		key := route.UpKey(route.RepoOf(op), route.Describe(op).Route)
		if _, ok := byKey[key]; !ok {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], op)
	}
	var out []*ops.Operation
	for _, key := range keys {
		starts := byKey[key]
		sortStarts(starts)
		out = append(out, starts[len(starts)-1])
	}
	return out, nil
}

// ListExecutions lists the stored execution records and, for a start
// journaled before records existed, each of its agents as a legacy entry.
func (s *runtimeService) ListExecutions(ctx context.Context, req *daemonv1.RuntimeServiceListExecutionsRequest) (*daemonv1.RuntimeServiceListExecutionsResponse, error) {
	name := req.GetRoute().GetValue()
	if name != "" && req.GetRepo() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "a route is named by its repository too")
	}
	var rows []store.Execution
	if err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		rows, err = store.Executions(c, req.GetRepo(), name)
		return err
	}); err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "executions: %v", err))
	}
	out := &daemonv1.RuntimeServiceListExecutionsResponse{}
	recorded := map[string]bool{}
	for _, row := range rows {
		out.Executions = append(out.Executions, executionMessage(row))
		recorded[row.Repo+"\x00"+row.Route+"\x00"+row.Agent] = true
	}
	starts, err := s.d.latestStarts()
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "operations: %v", err))
	}
	for _, op := range starts {
		repo, facts := route.RepoOf(op), route.Describe(op)
		if name != "" && (facts.Route != name || repo != req.GetRepo()) {
			continue
		}
		for _, a := range route.AgentsOf(op) {
			if recorded[repo+"\x00"+facts.Route+"\x00"+a.Name] {
				continue
			}
			out.Executions = append(out.Executions, &daemonv1.Execution{
				Ref:   &corev1.RuntimeRef{Backend: a.Backend, EndpointId: a.Socket},
				Route: &corev1.RouteId{Value: facts.Route}, Repo: repo, Agent: a.Name, Role: a.Role, HarnessKind: a.Kind,
				Source: "legacy",
			})
		}
	}
	return out, nil
}

// GetExecution reports one stored execution.
func (s *runtimeService) GetExecution(ctx context.Context, req *daemonv1.RuntimeServiceGetExecutionRequest) (*daemonv1.RuntimeServiceGetExecutionResponse, error) {
	if req.GetExecutionId() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no execution")
	}
	var row store.Execution
	found := false
	if err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		row, found, err = store.ExecutionByID(c, req.GetExecutionId())
		return err
	}); err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "executions: %v", err))
	}
	if !found {
		return nil, rpc.Errorf(rpc.NotFound, "no execution %s", req.GetExecutionId())
	}
	return &daemonv1.RuntimeServiceGetExecutionResponse{Execution: executionMessage(row)}, nil
}

// GetDefault reports the installation default.
func (s *runtimeService) GetDefault(ctx context.Context, _ *daemonv1.RuntimeServiceGetDefaultRequest) (*daemonv1.RuntimeServiceGetDefaultResponse, error) {
	backend, source, at, err := s.d.installationDefault()
	if err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "the installation default: %v", err))
	}
	return &daemonv1.RuntimeServiceGetDefaultResponse{Backend: backend, Source: source, WrittenAt: at}, nil
}

// SetDefault changes the installation default. Routes that exist keep their
// backend, and a config file's [runtime] backend still wins over it.
func (s *runtimeService) SetDefault(ctx context.Context, req *daemonv1.RuntimeServiceSetDefaultRequest) (*daemonv1.RuntimeServiceSetDefaultResponse, error) {
	backend := req.GetBackend()
	if !runtime.Valid(backend) {
		return nil, runtimeFailure(&runtime.Error{Code: runtime.CodeUnknownBackend, Msg: fmt.Sprintf("%q is not a backend; use one of %v", backend, runtime.Backends)})
	}
	var previous string
	if err := s.d.ops.Read(func(c store.Conn) error {
		var err error
		previous, err = store.SetInstallationDefault(c, backend, s.d.now().UnixMilli())
		return err
	}); err != nil {
		return nil, rpcFailure(ctx, Errf("internal", "the installation default: %v", err))
	}
	return &daemonv1.RuntimeServiceSetDefaultResponse{Backend: backend, Previous: previous}, nil
}

// supportedPlatform is where the core backend is claimed: macOS on Apple
// silicon, until core and the app are shown on Linux.
func supportedPlatform() bool { return goruntime.GOOS == "darwin" && goruntime.GOARCH == "arm64" }

// Doctor reports the selection a run would make and what each backend needs.
// It changes nothing.
func (s *runtimeService) Doctor(ctx context.Context, req *daemonv1.RuntimeServiceDoctorRequest) (*daemonv1.RuntimeServiceDoctorResponse, error) {
	out := &daemonv1.RuntimeServiceDoctorResponse{Version: &corev1.Version{Contract: version.Version, Build: s.d.cfg.Version}}
	check := func(name, state, detail string) {
		out.Checks = append(out.Checks, &daemonv1.RuntimeCheck{Name: name, State: state, Detail: detail})
	}
	if def, err := s.GetDefault(ctx, nil); err == nil {
		out.Installation = def
		check("installation-default", "ok", fmt.Sprintf("%s (%s)", def.GetBackend(), def.GetSource()))
	} else {
		check("installation-default", "fail", err.Error())
	}
	persisted := ""
	if name := req.GetRoute().GetValue(); name != "" && req.GetRepo() != "" {
		starts, err := (&routeService{d: s.d}).starts(route.UpKey(req.GetRepo(), name))
		if err != nil {
			return nil, err
		}
		if len(starts) > 0 {
			persisted = route.BackendOf(starts[len(starts)-1])
		}
	}
	sel, err := s.d.selectBackend(req.GetRepo(), req.GetConfigPath(), req.GetBackend(), persisted)
	if err != nil {
		check("selection", "fail", err.Error())
	} else {
		out.Selection = &daemonv1.RuntimeSelection{Backend: sel.Backend, Source: string(sel.Source), ConfigPath: sel.ConfigPath}
		check("selection", "ok", fmt.Sprintf("%s, from %s", sel.Backend, sel.Source))
	}
	reg := s.d.runtimeRegistry()
	for _, backend := range runtime.Backends {
		report := &daemonv1.BackendReport{Backend: backend, Available: true}
		if err := reg.Available(backend); err != nil {
			report.Available, report.Reason = false, err.Error()
		}
		if backend == runtime.BackendTUI {
			report.Endpoint = s.d.cfg.HerdrSocket
			if report.Available {
				cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, perr := herdr.New(s.d.cfg.HerdrSocket).Check(cctx)
				cancel()
				if perr != nil {
					report.Available, report.Reason = false, "the Herdr TUI server does not answer: "+perr.Error()
				}
			}
		}
		out.Backends = append(out.Backends, report)
	}
	if sel.Backend != "" {
		i := slices.IndexFunc(out.Backends, func(b *daemonv1.BackendReport) bool { return b.GetBackend() == sel.Backend })
		if b := out.Backends[i]; b.GetAvailable() {
			check("backend", "ok", sel.Backend+" can run")
		} else {
			check("backend", "fail", b.GetReason())
		}
	}
	if supportedPlatform() {
		check("platform", "ok", goruntime.GOOS+"/"+goruntime.GOARCH)
	} else {
		check("platform", "warn", fmt.Sprintf("herdr-core is claimed for macOS on Apple silicon only; on %s/%s use backend = %q", goruntime.GOOS, goruntime.GOARCH, runtime.BackendTUI))
	}
	return out, nil
}
