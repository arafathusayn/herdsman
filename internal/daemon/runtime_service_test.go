package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// hasCheck reports a doctor check of that name and state whose detail holds
// the words.
func hasCheck(doc *daemonv1.RuntimeServiceDoctorResponse, name, state, words string) bool {
	for _, c := range doc.GetChecks() {
		if c.GetName() == name && c.GetState() == state && strings.Contains(c.GetDetail(), words) {
			return true
		}
	}
	return false
}

// refusalCode is the core.v1.Error code a refused call carries.

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatalf("not an rpc error: %v", err)
	}
	var detail corev1.Error
	if !e.Detail(&detail) {
		t.Fatalf("no detail on %v", err)
	}
	return detail.GetCode()
}

// legacyStart journals a route start the way a binary before backends did:
// no step names a backend.
func legacyStart(t *testing.T, h *harness, repo string) *ops.Operation {
	t.Helper()
	spec := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	folder := filepath.Join(repo, ".herdsman", "harbor-2026-11-09")
	base := map[string]any{"key": route.UpKey(repo, "harbor"), "route": "harbor", "repo": repo, "folder": folder, "workspace": "proj-harbor", "socket": h.srv.Socket}
	agent := map[string]any{"name": "proj-harbor-orchestrator", "kind": "codex", "label": "orchestrator"}
	for k, v := range base {
		agent[k] = v
	}
	op, err := h.d.ops.Submit(context.Background(), ops.Spec{
		Key: route.UpKey(repo, "harbor"), Kind: route.OperationKind, Plan: "route: harbor\n",
		Steps: []ops.StepSpec{
			{Name: "create the route folder", Effect: "route.folder", Key: "folder:" + folder, Spec: spec(base), Retry: ops.RetryIdempotent},
			{Name: "start proj-harbor-orchestrator", Effect: "route.agent", Key: "agent:proj-harbor-orchestrator", Spec: spec(agent), Retry: ops.RetryNever},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// Gate "Upgrade", new side, under the experiment fallback of spec section 5: a
// new account starts on the Herdr TUI, core is refused without the marker
// (naming it), and with experimental = true in [runtime.core] the account
// plans a core start, with no fallback to the TUI; the flag picks the TUI for
// one run.
func TestNewInstallationSelectsCoreOnlyAsTheExperiment(t *testing.T) {
	h := newAccountHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	def, err := c.Runtime.GetDefault(ctx, &daemonv1.RuntimeServiceGetDefaultRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if def.GetBackend() != runtime.BackendTUI || def.GetSource() != "new-store" {
		t.Fatalf("%+v", def)
	}
	repo := filepath.Join(h.ph, "proj")
	coreReq := upRequest(repo)
	coreReq.OrchestratorCommand = "codex --no-daemon"
	coreReq.Backend = runtime.BackendCore
	_, err = c.Route.Up(ctx, coreReq)
	if codeOf(err) != rpc.FailedPrecondition || refusalCode(t, err) != runtime.CodeBackendExperimental || !strings.Contains(err.Error(), "experimental = true in [runtime.core]") {
		t.Fatalf("core without the marker: %v", err)
	}
	doc, err := c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{Repo: repo, Backend: runtime.BackendCore})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetBackend() != "" || !hasCheck(doc, "selection", "fail", "experimental = true") {
		t.Fatalf("doctor without the marker: %+v", doc)
	}
	// Opted in: core is planned, and the run says once that it is an
	// experiment.
	optInCore(t, h)
	coreReq.Backend = ""
	planned, err := c.Route.Up(ctx, coreReq)
	if err != nil || planned.GetStart().GetBackend() != runtime.BackendCore || !strings.Contains(planned.GetStart().GetPlan(), "backend: herdr-core") {
		t.Fatalf("%v %v", planned, err)
	}
	doc, err = c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetExperiment() != runtime.ExperimentNotice || !hasCheck(doc, "experiment", "warn", runtime.ExperimentLabel) {
		t.Fatalf("the experiment is not said: %+v", doc)
	}

	// The flag selects the TUI for this run.
	req := upRequest(repo)
	req.Backend = runtime.BackendTUI
	res, err := c.Route.Up(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStart().GetBackend() != runtime.BackendTUI || res.GetStart().GetState() != "planned" {
		t.Fatalf("%+v", res.GetStart())
	}
	// A misspelled flag never falls through to a default.
	req.Backend = "herdr-tiu"
	if _, err := c.Route.Up(ctx, req); codeOf(err) != rpc.InvalidArgument || refusalCode(t, err) != runtime.CodeUnknownBackend {
		t.Fatalf("%v", err)
	}
}

// Gate "Migration": rollback is the config line on this binary, and the file
// wins over the installation default; an unknown [runtime] key stops the run.
func TestConfigFileSelectsTheBackendAndUnknownRuntimeKeyStops(t *testing.T) {
	h := newAccountHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.dir, "rollback.toml")
	if err := os.WriteFile(file, []byte("[runtime]\nbackend = \"herdr-tui\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := upRequest(repo)
	req.ConfigPath = file
	res, err := c.Route.Up(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStart().GetBackend() != runtime.BackendTUI {
		t.Fatalf("%+v", res.GetStart())
	}
	doc, err := c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{Repo: repo, ConfigPath: file})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetSource() != "config" || doc.GetSelection().GetConfigPath() != file {
		t.Fatalf("%+v", doc.GetSelection())
	}

	if err := os.WriteFile(file, []byte("[runtime]\nbakend = \"herdr-tui\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(ctx, req); err == nil || !strings.Contains(err.Error(), "runtime.bakend") {
		t.Fatalf("an unknown [runtime] key did not stop the start: %v", err)
	}
}

// Gate "Migration", old side, and backend_migration_required: a route started
// before backends existed stays a TUI route whatever the installation default
// says, and a flag that names another backend is refused.
func TestExistingTUIRouteKeepsItsBackend(t *testing.T) {
	h := newAccountHarness(t)
	optInCore(t, h) // the config selects herdr-core for new routes
	c := routeClient(t, h)

	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	legacyStart(t, h, repo)

	res, err := c.Route.Up(ctx, upRequest(repo))
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStart().GetBackend() != runtime.BackendTUI {
		t.Fatalf("an old route moved to %q", res.GetStart().GetBackend())
	}
	got, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetStart().GetBackend() != runtime.BackendTUI {
		t.Fatalf("%+v", got.GetStart())
	}

	req := upRequest(repo)
	req.Backend = runtime.BackendCore
	_, err = c.Route.Up(ctx, req)
	if codeOf(err) != rpc.FailedPrecondition || refusalCode(t, err) != runtime.CodeBackendMigrationRequired {
		t.Fatalf("%v", err)
	}
	req.Backend = runtime.BackendTUI
	if _, err := c.Route.Up(ctx, req); err != nil {
		t.Fatalf("the route's own backend was refused: %v", err)
	}
	doc, err := c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{Repo: repo, Route: &corev1.RouteId{Value: "harbor"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetBackend() != runtime.BackendTUI || doc.GetSelection().GetSource() != "route" {
		t.Fatalf("%+v", doc.GetSelection())
	}
}

func TestExecutionsListRecordsAndLegacyAgents(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	legacyStart(t, h, repo)

	list, err := c.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetExecutions()) != 1 {
		t.Fatalf("%v", list)
	}
	legacy := list.GetExecutions()[0]
	if legacy.GetSource() != "legacy" || legacy.GetRef().GetBackend() != runtime.BackendTUI || legacy.GetAgent() != "proj-harbor-orchestrator" || legacy.GetRole() != "orchestrator" {
		t.Fatalf("%+v", legacy)
	}

	now := time.Unix(1_800_000_000, 0)
	e := runtime.Execution{
		SchemaVersion: runtime.SchemaVersion, ID: runtime.NewExecutionID(), Repo: repo, Route: "harbor", Agent: "proj-harbor-orchestrator",
		Role: "orchestrator", LaunchIntent: "up:" + repo + ":harbor:proj-harbor-orchestrator:w1:p2", Backend: runtime.BackendTUI,
		EndpointID: h.srv.Socket, Pane: "w1:p2", HarnessKind: "codex", Desired: runtime.DesiredRunning,
		Observation: runtime.Observation{Process: runtime.ProcessLive, Readiness: runtime.ReadinessReady, Program: runtime.ProgramIdle,
			RawProgram: "idle", Visibility: runtime.VisibilityVerified, Orchestration: runtime.OrchestrationActive, ObservedAt: now},
		TUI: &runtime.TUIBinding{Socket: h.srv.Socket, Pane: "w1:p2"},
	}
	if err := h.d.recordExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	list, err = c.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetExecutions()) != 1 || list.GetExecutions()[0].GetSource() != "record" {
		t.Fatalf("the recorded agent is listed twice or not at all: %v", list)
	}
	rec := list.GetExecutions()[0]
	if rec.GetRef().GetExecutionId() != e.ID || rec.GetRef().GetGeneration() != 1 || rec.GetRef().GetPane() != "w1:p2" ||
		rec.GetObservation().GetReadiness() != "ready" || rec.GetTuiBinding().GetPane() != "w1:p2" {
		t.Fatalf("%+v", rec)
	}
	one, err := c.Runtime.GetExecution(ctx, &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: e.ID})
	if err != nil || one.GetExecution().GetAgent() != "proj-harbor-orchestrator" {
		t.Fatalf("%v %v", one, err)
	}
	if _, err := c.Runtime.GetExecution(ctx, &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: runtime.NewExecutionID()}); codeOf(err) != rpc.NotFound {
		t.Fatalf("%v", err)
	}
	if _, err := c.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{Route: &corev1.RouteId{Value: "harbor"}}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("%v", err)
	}
}

// An execution's experiment label and its native conversation, with its
// provenance, reach the wire as the record holds them.
func TestExecutionCarriesItsExperimentAndConversation(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	e := runtime.Execution{
		SchemaVersion: runtime.SchemaVersion, ID: runtime.NewExecutionID(), Repo: repo, Route: "harbor", Agent: "proj-harbor-orchestrator",
		Role: "orchestrator", LaunchIntent: "up:" + repo + ":harbor:proj-harbor-orchestrator:x", Backend: runtime.BackendCore,
		EndpointID: "core-1", TerminalAddress: "t-1", HarnessKind: "claude", Desired: runtime.DesiredRunning, Experiment: runtime.ExperimentLabel,
		Conversation: &runtime.Conversation{Value: "0b7f3c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f", Provenance: "claude-code:project-session-file"},
		Observation:  liveObservation(),
	}
	if err := h.d.recordExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	one, err := c.Runtime.GetExecution(ctx, &daemonv1.RuntimeServiceGetExecutionRequest{ExecutionId: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := one.GetExecution()
	if got.GetExperiment() != runtime.ExperimentLabel || got.GetNativeConversation().GetValue() != e.Conversation.Value ||
		got.GetNativeConversation().GetProvenance() != e.Conversation.Provenance {
		t.Fatalf("%+v", got)
	}
}

// The tracer of the runtime contract: a start on the TUI goes through the
// provider and leaves an execution record, and a second look at the same agent
// is the same execution.
func TestStartOnTheTUIRecordsItsExecution(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	ctx := context.Background()
	list, err := c.Runtime.ListExecutions(ctx, &daemonv1.RuntimeServiceListExecutionsRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.GetExecutions()) != 1 {
		t.Fatalf("%v", list)
	}
	e := list.GetExecutions()[0]
	if e.GetSource() != "record" || e.GetRef().GetBackend() != runtime.BackendTUI || e.GetRef().GetPane() == "" ||
		len(e.GetRef().GetExecutionId()) != 36 || e.GetRef().GetGeneration() != 1 || e.GetHarnessKind() != "codex" ||
		e.GetRole() != "orchestrator" || e.GetDesiredState() != "running" || e.GetTuiBinding().GetSocket() != h.srv.Socket {
		t.Fatalf("%+v", e)
	}
	start, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: &corev1.RouteId{Value: "harbor"}, Repo: repo})
	if err != nil || start.GetStart().GetBackend() != runtime.BackendTUI {
		t.Fatalf("%v %v", start, err)
	}
}

func TestSetDefaultAndDoctor(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	if _, err := c.Runtime.SetDefault(ctx, &daemonv1.RuntimeServiceSetDefaultRequest{Backend: "tui"}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("%v", err)
	}
	set, err := c.Runtime.SetDefault(ctx, &daemonv1.RuntimeServiceSetDefaultRequest{Backend: runtime.BackendCore})
	if err != nil || set.GetPrevious() != runtime.BackendTUI {
		t.Fatalf("%v %v", set, err)
	}
	// Core as the installation default is still the experiment: without the
	// marker the selection fails and names it, and a task launch is refused
	// the same way.
	doc, err := c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetBackend() != "" || !hasCheck(doc, "selection", "fail", "experimental = true in [runtime.core]") {
		t.Fatalf("%+v", doc)
	}
	if err := h.d.taskBackend(ctx, ""); runtime.CodeOf(err) != runtime.CodeBackendExperimental {
		t.Fatalf("%v", err)
	}
	marker := filepath.Join(h.dir, "core.toml")
	if err := os.WriteFile(marker, []byte("[runtime.core]\nexperimental = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err = c.Runtime.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{ConfigPath: marker})
	if err != nil {
		t.Fatal(err)
	}
	if doc.GetSelection().GetBackend() != runtime.BackendCore || doc.GetSelection().GetSource() != "installation" || doc.GetInstallation().GetSource() != "set" {
		t.Fatalf("%+v", doc)
	}

	states := map[string]string{}
	for _, check := range doc.GetChecks() {
		states[check.GetName()] = check.GetState()
	}
	if states["backend"] != "fail" || states["selection"] != "ok" {
		t.Fatalf("%v", doc.GetChecks())
	}
	for _, b := range doc.GetBackends() {
		switch b.GetBackend() {
		case runtime.BackendCore:
			// The harness's core socket serves nothing.
			if b.GetAvailable() || !strings.Contains(b.GetReason(), "does not answer") {
				t.Fatalf("%+v", b)
			}
		case runtime.BackendTUI:
			if !b.GetAvailable() || b.GetEndpoint() != h.srv.Socket {
				t.Fatalf("%+v", b)
			}
		}
	}
	// A task that launches an agent resolves as up does: on core, with the
	// daemon's core runtime, it is admitted and runs on core.
	optInCore(t, h)
	coreRepo := filepath.Join(h.ph, "proj")
	if err := h.d.taskBackend(ctx, coreRepo); err != nil || !h.d.taskOnCore(ctx, coreRepo) {
		t.Fatalf("a core task launch: %v", err)
	}
	h.setDefaultBackend(runtime.BackendTUI)
	if err := h.d.taskBackend(ctx, ""); err != nil {
		t.Fatal(err)
	}
}
