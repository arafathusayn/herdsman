package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// fakeRuntime answers the runtime verbs as a daemon would.
type fakeRuntime struct {
	backend, source string
	doctor          *daemonv1.RuntimeServiceDoctorRequest
	list            *daemonv1.RuntimeServiceListExecutionsRequest
}

func (f *fakeRuntime) ListExecutions(_ context.Context, req *daemonv1.RuntimeServiceListExecutionsRequest) (*daemonv1.RuntimeServiceListExecutionsResponse, error) {
	f.list = req
	return &daemonv1.RuntimeServiceListExecutionsResponse{Executions: []*daemonv1.Execution{
		{Ref: &corev1.RuntimeRef{Backend: "herdr-tui", ExecutionId: "11111111-1111-4111-8111-111111111111", Generation: 1, Pane: "w1:p2"},
			Route: &corev1.RouteId{Value: "harbor"}, Agent: "proj-harbor-orchestrator", Role: "orchestrator", Source: "record",
			Observation: &daemonv1.ExecutionObservation{Process: "live", Readiness: "ready", Program: "idle"}},
		{Ref: &corev1.RuntimeRef{Backend: "herdr-tui"}, Route: &corev1.RouteId{Value: "old"}, Agent: "proj-old-orchestrator", Source: "legacy"},
	}}, nil
}

func (f *fakeRuntime) GetExecution(context.Context, *daemonv1.RuntimeServiceGetExecutionRequest) (*daemonv1.RuntimeServiceGetExecutionResponse, error) {
	return nil, rpc.Errorf(rpc.NotFound, "no execution")
}

func (f *fakeRuntime) Doctor(_ context.Context, req *daemonv1.RuntimeServiceDoctorRequest) (*daemonv1.RuntimeServiceDoctorResponse, error) {
	f.doctor = req
	state := "ok"
	if f.backend == "herdr-core" {
		state = "fail"
	}
	return &daemonv1.RuntimeServiceDoctorResponse{
		Selection:    &daemonv1.RuntimeSelection{Backend: f.backend, Source: "installation"},
		Installation: &daemonv1.RuntimeServiceGetDefaultResponse{Backend: f.backend, Source: f.source},
		Backends: []*daemonv1.BackendReport{
			{Backend: "herdr-core", Reason: `herdr-core is not available in this build; set backend = "herdr-tui"`},
			{Backend: "herdr-tui", Available: true},
		},
		Checks: []*daemonv1.RuntimeCheck{{Name: "backend", State: state, Detail: f.backend}},
	}, nil
}

func (f *fakeRuntime) GetDefault(context.Context, *daemonv1.RuntimeServiceGetDefaultRequest) (*daemonv1.RuntimeServiceGetDefaultResponse, error) {
	return &daemonv1.RuntimeServiceGetDefaultResponse{Backend: f.backend, Source: f.source, WrittenAt: 1_800_000_000_000}, nil
}

func (f *fakeRuntime) SetDefault(_ context.Context, req *daemonv1.RuntimeServiceSetDefaultRequest) (*daemonv1.RuntimeServiceSetDefaultResponse, error) {
	if req.GetBackend() != "herdr-core" && req.GetBackend() != "herdr-tui" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "%q is not a backend", req.GetBackend())
	}
	prev := f.backend
	f.backend, f.source = req.GetBackend(), "set"
	return &daemonv1.RuntimeServiceSetDefaultResponse{Backend: f.backend, Previous: prev}, nil
}

func withFakeRuntime(t *testing.T, f *fakeRuntime) {
	t.Helper()
	oldDial, oldRepo := dialRuntime, runtimeRepo
	dialRuntime = func() (daemonv1.RuntimeService, func(), error) { return f, func() {}, nil }
	runtimeRepo = func() string { return "/srv/repo" }
	t.Cleanup(func() { dialRuntime, runtimeRepo = oldDial, oldRepo })
}

func runRuntimeArgs(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runRuntime(args, nil, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRuntimeDefaultPrintsAndSets(t *testing.T) {
	f := &fakeRuntime{backend: "herdr-tui", source: "existing-store"}
	withFakeRuntime(t, f)
	code, out, _ := runRuntimeArgs("default")
	if code != 0 || !strings.HasPrefix(out, "herdr-tui (kept by the upgrade of an existing installation") {
		t.Fatalf("%d %q", code, out)
	}
	code, out, errOut := runRuntimeArgs("default", "herdr-core")
	if code != 0 || !strings.Contains(out, "herdr-core (was herdr-tui)") || !strings.Contains(errOut, "not available in this build") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if code, _, _ := runRuntimeArgs("default", "tui"); code != 1 {
		t.Fatalf("an unknown backend: exit %d", code)
	}
	if code, _, _ := runRuntimeArgs("default", "a", "b"); code != 2 {
		t.Fatalf("two values: exit %d", code)
	}
	code, out, _ = runRuntimeArgs("default", "--json")
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["backend"] != "herdr-core" || got["source"] != "set" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestRuntimeDoctorExitsOnAFailedCheck(t *testing.T) {
	f := &fakeRuntime{backend: "herdr-core", source: "new-store"}
	withFakeRuntime(t, f)
	code, out, _ := runRuntimeArgs("doctor", "--backend", "herdr-tui", "--route", "harbor")
	if code != 1 || !strings.Contains(out, "backend: herdr-core (from installation)") || !strings.Contains(out, `set backend = "herdr-tui"`) {
		t.Fatalf("%d %q", code, out)
	}
	if f.doctor.GetBackend() != "herdr-tui" || f.doctor.GetRoute().GetValue() != "harbor" || f.doctor.GetRepo() != "/srv/repo" {
		t.Fatalf("%+v", f.doctor)
	}
	f.backend = "herdr-tui"
	if code, _, _ := runRuntimeArgs("doctor", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if code, _, _ := runRuntimeArgs("doctor", "extra"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestRuntimeLsListsRecordsAndLegacyAgents(t *testing.T) {
	f := &fakeRuntime{backend: "herdr-tui"}
	withFakeRuntime(t, f)
	code, out, _ := runRuntimeArgs("ls", "--route", "harbor")
	if code != 0 || !strings.Contains(out, "11111111-1111-4111-8111-111111111111 (gen 1)") || !strings.Contains(out, "legacy") {
		t.Fatalf("%d %q", code, out)
	}
	if f.list.GetRoute().GetValue() != "harbor" || f.list.GetRepo() != "/srv/repo" {
		t.Fatalf("%+v", f.list)
	}
	if code, _, _ := runRuntimeArgs("nothing"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestUpSendsTheBackendFlag(t *testing.T) {
	var stderr bytes.Buffer
	f, ok := parseUp([]string{"harbor", "-p", "ship", "-a", "claude", "-o", "codex", "--backend", "herdr-tui"}, &stderr)
	if !ok || f.backend != "herdr-tui" {
		t.Fatalf("%+v %s", f, stderr.String())
	}
}
