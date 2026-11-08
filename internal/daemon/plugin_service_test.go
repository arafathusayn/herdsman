package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/plugin"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

var (
	examplePluginOnce sync.Once
	examplePluginPath string
	examplePluginErr  error
)

// exampleColumnPlugin builds one real plugin, once per test run.
func exampleColumnPlugin(t *testing.T) string {
	t.Helper()
	examplePluginOnce.Do(func() {
		dir, err := os.MkdirTemp("", "hpl")
		if err != nil {
			examplePluginErr = err
			return
		}
		examplePluginPath = filepath.Join(dir, "herdsman-plugin-column-example")
		out, err := exec.Command("go", "build", "-o", examplePluginPath, "github.com/arafathusayn/herdsman/examples/plugins/column-example").CombinedOutput()
		if err != nil {
			examplePluginErr = errors.New(err.Error() + string(out))
		}
	})
	if examplePluginErr != nil {
		t.Fatal(examplePluginErr)
	}
	return examplePluginPath
}

func newPluginService(t *testing.T) *PluginService {
	t.Helper()
	dir := t.TempDir()
	registry := plugin.NewRegistry()
	if err := plugin.AddBuiltins(registry, plugin.BuiltinOptions{}); err != nil {
		t.Fatal(err)
	}
	return &PluginService{Registry: registry, Host: &plugin.Host{ConfigDir: filepath.Join(dir, "c"), SearchPath: filepath.Join(dir, "bin"), Timeout: 30 * time.Second}}
}

func wantCode(t *testing.T, err error, code rpc.Code) {
	t.Helper()
	if e, ok := errors.AsType[*rpc.Error](err); !ok || e.Code != code {
		t.Fatalf("want %s: %v", code, err)
	}
}

func approve(t *testing.T, s *PluginService, name string) {
	t.Helper()
	if err := s.Approve(context.Background(), name, strings.NewReader("yes\n"), &strings.Builder{}, true); err != nil {
		t.Fatal(err)
	}
}

func install(t *testing.T, s *PluginService, source string) *daemonv1.PluginStatus {
	t.Helper()
	response, err := s.Install(context.Background(), &daemonv1.PluginServiceInstallRequest{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return response.GetStatus()
}

func TestInstallLeavesAPluginUnapprovedUntilAPersonAccepts(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	status := install(t, s, exampleColumnPlugin(t))
	if status.GetApproved() || status.GetName() != "column-example" || !strings.Contains(status.GetSkipped(), "not approved") {
		t.Fatalf("%v", status)
	}
	_, err := s.Verify(ctx, &daemonv1.PluginServiceVerifyRequest{Name: "column-example"})
	wantCode(t, err, rpc.PermissionDenied)

	approve(t, s, "column-example")
	verified, err := s.Verify(ctx, &daemonv1.PluginServiceVerifyRequest{Name: "column-example"})
	if err != nil || !verified.GetPassed() || len(verified.GetChecks()) < 2 {
		t.Fatalf("%v %v", verified, err)
	}
	described, err := s.Describe(ctx, &daemonv1.PluginServiceDescribeRequest{Name: "column-example"})
	if err != nil || described.GetManifest().GetVersion() != "0.1.0" || !described.GetStatus().GetApproved() {
		t.Fatalf("%v %v", described, err)
	}
	if _, err := s.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: "column-example"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Host.Approvals().Approved("column-example", status.GetChecksum()); ok {
		t.Fatal("the approval outlived the plugin")
	}
	_, err = s.Describe(ctx, &daemonv1.PluginServiceDescribeRequest{Name: "column-example"})
	wantCode(t, err, rpc.NotFound)
}

func TestAChangedExecutableIsNoLongerApproved(t *testing.T) {
	s := newPluginService(t)
	install(t, s, exampleColumnPlugin(t))
	approve(t, s, "column-example")
	if err := os.WriteFile(s.Host.InstalledPath("column-example"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := s.Verify(context.Background(), &daemonv1.PluginServiceVerifyRequest{Name: "column-example"})
	wantCode(t, err, rpc.PermissionDenied)
}

func TestListSkipsAFailedHandshakeByNameAndKeepsTheRest(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	install(t, s, exampleColumnPlugin(t))
	approve(t, s, "column-example")
	broken := filepath.Join(s.Host.SearchPath, "herdsman-plugin-broken")
	os.MkdirAll(s.Host.SearchPath, 0o755)
	os.WriteFile(broken, []byte("#!/bin/sh\necho hello\n"), 0o755)
	approve(t, s, "broken")

	listed, err := s.List(ctx, &daemonv1.PluginServiceListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if listed.GetProtocol() != "herdsman-plugin/1" {
		t.Errorf("%q", listed.GetProtocol())
	}
	byName := map[string]*daemonv1.PluginStatus{}
	for _, status := range listed.GetPlugins() {
		byName[status.GetName()] = status
	}
	if !strings.Contains(byName["broken"].GetSkipped(), "handshake") {
		t.Errorf("broken: %v", byName["broken"])
	}
	if got := byName["column-example"]; got.GetSkipped() != "" || got.GetVersion() != "0.1.0" {
		t.Errorf("column: %v", got)
	}
	if byName["json"].GetPath() != "" || !byName["json"].GetApproved() {
		t.Errorf("json: %v", byName["json"])
	}
	_, err = s.Describe(ctx, &daemonv1.PluginServiceDescribeRequest{Name: "broken"})
	wantCode(t, err, rpc.FailedPrecondition)
}

func TestAnExecutableClaimingAnotherNameCannotReplaceABuiltin(t *testing.T) {
	s := newPluginService(t)
	copied := filepath.Join(t.TempDir(), "herdsman-plugin-branch")
	data, err := os.ReadFile(exampleColumnPlugin(t))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(copied, data, 0o755)
	install(t, s, copied)
	approve(t, s, "branch")
	listed, err := s.List(context.Background(), &daemonv1.PluginServiceListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// The executable says it is column-example, so its handshake fails on the
	// name and the built-in stays listed next to the skipped plugin.
	count := 0
	for _, status := range listed.GetPlugins() {
		if status.GetName() == "branch" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("%v", listed.GetPlugins())
	}
}

func TestBuiltinsDescribeAndVerifyWithoutAProcess(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	described, err := s.Describe(ctx, &daemonv1.PluginServiceDescribeRequest{Name: "json"})
	if err != nil || described.GetManifest().GetVersion() != "builtin" {
		t.Fatalf("%v %v", described, err)
	}
	verified, err := s.Verify(ctx, &daemonv1.PluginServiceVerifyRequest{Name: "json"})
	if err != nil || !verified.GetPassed() {
		t.Fatalf("%v %v", verified, err)
	}
	_, err = s.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: "json"})
	wantCode(t, err, rpc.FailedPrecondition)
}

func TestVerifyReportsAFailingPlugin(t *testing.T) {
	s := newPluginService(t)
	_, err := s.Verify(context.Background(), &daemonv1.PluginServiceVerifyRequest{Name: "absent"})
	wantCode(t, err, rpc.NotFound)
}

func TestInstallRefusesBadSources(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	dir := t.TempDir()
	plain := filepath.Join(dir, "herdsman-plugin-plain")
	os.WriteFile(plain, nil, 0o644)
	bad := filepath.Join(dir, "Not Valid")
	os.WriteFile(bad, nil, 0o755)
	for source, code := range map[string]rpc.Code{
		"https://example.com/p":      rpc.InvalidArgument,
		"absent":                     rpc.NotFound,
		"relative/path":              rpc.InvalidArgument,
		plain:                        rpc.InvalidArgument,
		bad:                          rpc.InvalidArgument,
		filepath.Join(dir, "absent"): rpc.NotFound,
	} {
		_, err := s.Install(ctx, &daemonv1.PluginServiceInstallRequest{Source: source})
		wantCode(t, err, code)
	}
}

func TestInstallByNameCopiesFromThePath(t *testing.T) {
	s := newPluginService(t)
	os.MkdirAll(s.Host.SearchPath, 0o755)
	onPath := filepath.Join(s.Host.SearchPath, "herdsman-plugin-tool")
	os.WriteFile(onPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	status := install(t, s, "tool")
	if status.GetPath() != s.Host.InstalledPath("tool") {
		t.Fatalf("%v", status)
	}
}

func TestRemoveRefusals(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	_, err := s.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: "../escape"})
	wantCode(t, err, rpc.InvalidArgument)
	_, err = s.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: "absent"})
	wantCode(t, err, rpc.NotFound)
	os.MkdirAll(s.Host.SearchPath, 0o755)
	os.WriteFile(filepath.Join(s.Host.SearchPath, "herdsman-plugin-onpath"), []byte("#!/bin/sh\n"), 0o755)
	_, err = s.Remove(ctx, &daemonv1.PluginServiceRemoveRequest{Name: "onpath"})
	wantCode(t, err, rpc.FailedPrecondition)
}

func TestApproveNeedsATerminalAndAYes(t *testing.T) {
	s := newPluginService(t)
	ctx := context.Background()
	os.MkdirAll(s.Host.SearchPath, 0o755)
	os.WriteFile(filepath.Join(s.Host.SearchPath, "herdsman-plugin-tool"), []byte("#!/bin/sh\n"), 0o755)
	if err := s.Approve(ctx, "tool", strings.NewReader("yes\n"), &strings.Builder{}, false); err == nil {
		t.Fatal("approved without a terminal")
	}
	if err := s.Approve(ctx, "tool", strings.NewReader("no\n"), &strings.Builder{}, true); err == nil {
		t.Fatal("approved on no")
	}
	if err := s.Approve(ctx, "absent", strings.NewReader("yes\n"), &strings.Builder{}, true); err == nil {
		t.Fatal("approved an absent plugin")
	}
	found, _, _ := s.Host.Find("tool")
	if ok, _ := s.Host.Approvals().Approved("tool", found.Checksum); ok {
		t.Fatal("recorded an approval")
	}
}

func TestPluginServiceServesItsMethodsOverRPC(t *testing.T) {
	s := newPluginService(t)
	server := httptest.NewServer(rpc.NewMux(s.Procedures()...))
	defer server.Close()
	client := daemonv1.NewPluginServiceClient(&http.Client{Transport: &rpc.Transport{}}, server.URL)
	listed, err := client.List(context.Background(), &daemonv1.PluginServiceListRequest{})
	if err != nil || len(listed.GetPlugins()) == 0 {
		t.Fatalf("%v %v", listed, err)
	}
	_, err = client.Describe(context.Background(), &daemonv1.PluginServiceDescribeRequest{Name: "absent"})
	wantCode(t, err, rpc.NotFound)
}

func TestNewPluginServiceHasTheBuiltins(t *testing.T) {
	s, err := NewPluginService()
	if err != nil || len(s.Registry.Builtins()) == 0 {
		t.Fatalf("%v %v", s, err)
	}
}
