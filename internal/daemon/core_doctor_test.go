package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
	"github.com/arafathusayn/herdsman/internal/runtime"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// doctorChecks runs the runtime doctor for the harbor route of repo and maps
// each check's name to its state and detail.
func doctorChecks(t *testing.T, h *harness, repo string) map[string]string {
	t.Helper()
	doc, err := (&runtimeService{d: h.d}).Doctor(context.Background(), &daemonv1.RuntimeServiceDoctorRequest{Repo: repo, Route: &corev1.RouteId{Value: "harbor"}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range doc.GetChecks() {
		out[c.GetName()] = c.GetState() + ": " + c.GetDetail()
	}
	return out
}

// startHarbor starts the harbor route on core with the app open, so the
// binding is verified and the executions are recorded.
func startHarbor(t *testing.T) (*harness, string, func(program string, a hctest.ExecAnswer)) {
	t.Helper()
	h, w := newCoreHarness(t)
	c := routeClient(t, h)
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	w.OpenApp()
	if _, err := c.Route.Up(context.Background(), coreUp(repo)); err != nil {
		t.Fatal(err)
	}
	waitHalt(t, c, repo)
	return h, repo, w.Core.SetExec
}

// Spec section 16: the runtime doctor names where the backend choice came
// from, the binding and when it was verified, where a launch's environment
// comes from with the markers in the daemon's and core's environments, the
// inventory rule with the foreign agents, the roots core reads, whether the
// app supports this machine, each harness's level and the tested versions.
func TestRuntimeDoctorReportsEverySectionSixteenItem(t *testing.T) {
	h, repo, setExec := startHarbor(t)
	setExec("/usr/bin/env", hctest.ExecAnswer{Stdout: "PATH=/usr/bin\nCLAUDECODE=1\nHOME=/h\n"})
	checks := doctorChecks(t, h, repo)
	for name, words := range map[string][]string{
		"selection":          {"ok: herdr-core, from route"},
		"core-endpoint":      {"ok: "},
		"core-version":       {"is not the tested version", "tested: herdr-core " + hc.TestedVersion + " at protocol generation 1, herdr-app 0.1.15"},
		"app-binding":        {"ok: ", "verified 20"},
		"launch-environment": {"ok: ", "from the daemon's environment", "HERDR_PANE_ID", "no core launch passes them"},
		"core-environment":   {"warn: ", "CLAUDECODE", "core was started under an agent"},
		"core-repository":    {"ok: core reads " + repo},
		"inventory":          {"inventory rule route", "0 foreign agents on the core"},
		"harness-claude":     {"ok: ", "unattended"},
		"harness-codex":      {"warn: ", "need a person"},
		"app-support":        {},
	} {
		got, ok := checks[name]
		if !ok {
			t.Errorf("no %s check in %v", name, checks)
			continue
		}
		for _, w := range words {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", name, got, w)
			}
		}
	}
}

// Gate "Privacy grants": a repository root that core cannot read fails the
// doctor, through core's own machine.exec, with the grant to make.
func TestRuntimeDoctorReportsARootCoreCannotRead(t *testing.T) {
	h, repo, setExec := startHarbor(t)
	setExec("/bin/ls", hctest.ExecAnswer{Code: 1, Stderr: "ls: " + repo + ": Operation not permitted\n"})
	got := doctorChecks(t, h, repo)["core-repository"]
	if !strings.HasPrefix(got, "fail: ") || !strings.Contains(got, "root_unreadable") || !strings.Contains(got, "Privacy & Security") || !strings.Contains(got, repo) {
		t.Fatalf("core-repository: %q", got)
	}
	setExec("/usr/bin/env", hctest.ExecAnswer{Stdout: "PATH=/usr/bin\n"})
	if got := doctorChecks(t, h, repo)["core-environment"]; !strings.HasPrefix(got, "ok: ") {
		t.Fatalf("core-environment: %q", got)
	}
}

// Gate "Platform": only macOS on Apple silicon is claimed; anywhere else the
// doctor says why and names the TUI line, and fails when the run would use
// core.
func TestRuntimeDoctorClaimsOnlyAppleSilicon(t *testing.T) {
	oldOS, oldArch := platformOS, platformArch
	t.Cleanup(func() { platformOS, platformArch = oldOS, oldArch })
	platformOS, platformArch = "darwin", "arm64"
	if state, detail := appSupport(runtime.BackendCore); state != "ok" || !strings.Contains(detail, "darwin/arm64") {
		t.Fatalf("%s %s", state, detail)
	}
	for _, m := range [][2]string{{"linux", "amd64"}, {"darwin", "amd64"}, {"linux", "arm64"}} {
		platformOS, platformArch = m[0], m[1]
		state, detail := appSupport(runtime.BackendCore)
		if state != "fail" || !strings.Contains(detail, `backend = "herdr-tui"`) || !strings.Contains(detail, m[0]+"/"+m[1]) {
			t.Errorf("%v core: %s %s", m, state, detail)
		}
		if state, _ := appSupport(runtime.BackendTUI); state != "warn" {
			t.Errorf("%v tui: %s", m, state)
		}
	}
}
