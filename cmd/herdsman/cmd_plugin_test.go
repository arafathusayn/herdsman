package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/plugin"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// wirePlugins does what main.go does, over temporary folders. The test files
// of this package are not client code, so they may name the server.
func wirePlugins(t *testing.T) *daemon.PluginService {
	t.Helper()
	dir := t.TempDir()
	registry := plugin.NewRegistry()
	if err := plugin.AddBuiltins(registry, plugin.BuiltinOptions{}); err != nil {
		t.Fatal(err)
	}
	service := &daemon.PluginService{Registry: registry, Host: &plugin.Host{ConfigDir: filepath.Join(dir, "c"), SearchPath: filepath.Join(dir, "bin"), Timeout: 30 * time.Second}}
	oldService, oldApprove, oldTerminal, oldExec := pluginService, pluginApprove, pluginIsTerminal, pluginExec
	t.Cleanup(func() {
		pluginService, pluginApprove, pluginIsTerminal, pluginExec = oldService, oldApprove, oldTerminal, oldExec
	})
	pluginService = service
	pluginExec = service.Run
	pluginApprove = func(ctx context.Context, name string, in io.Reader, out io.Writer, isTerminal bool) error {
		return service.Approve(ctx, name, in, out, isTerminal)
	}
	pluginIsTerminal = func(any) bool { return true }
	return service
}

func buildExample(t *testing.T, name string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "herdsman-plugin-"+name)
	out, err := exec.Command("go", "build", "-o", binary, "github.com/arafathusayn/herdsman/examples/plugins/"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return binary
}

func pluginCommand(stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runPlugin(append([]string{"--experimental-plugins"}, args...), strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestPluginVerifyPassesForEveryExample(t *testing.T) {
	wirePlugins(t)
	for _, name := range []string{"codec-kdl", "detector-example", "backend-gitea", "generator-example", "column-example"} {
		t.Run(name, func(t *testing.T) {
			if code, out, errOut := pluginCommand("yes\n", "install", buildExample(t, name)); code != 0 || !strings.Contains(out, "approved") {
				t.Fatalf("install: %d %s %s", code, out, errOut)
			}
			code, out, errOut := pluginCommand("", "verify", name)
			if code != 0 || strings.Contains(out, "FAIL") || !strings.Contains(out, "pass") {
				t.Fatalf("verify: %d %s %s", code, out, errOut)
			}
		})
	}
}

func TestPluginListSkipsAFailedHandshakeByName(t *testing.T) {
	service := wirePlugins(t)
	os.MkdirAll(service.Host.SearchPath, 0o755)
	broken := filepath.Join(service.Host.SearchPath, "herdsman-plugin-broken")
	os.WriteFile(broken, []byte("#!/bin/sh\necho hello\n"), 0o755)
	if err := service.Approve(context.Background(), "broken", strings.NewReader("yes\n"), io.Discard, true); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := pluginCommand("", "list")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	var line string
	for _, candidate := range strings.Split(out, "\n") {
		if strings.HasPrefix(candidate, "broken") {
			line = candidate
		}
	}
	if !strings.Contains(line, "skipped:") || !strings.Contains(out, "built-in") {
		t.Fatalf("%s", out)
	}
	if strings.Count(errOut, "experimental") != 1 {
		t.Fatalf("the notice must print once: %q", errOut)
	}
}

func TestPluginNeedsTheExperimentalFlag(t *testing.T) {
	wirePlugins(t)
	var stdout, stderr bytes.Buffer
	if code := runPlugin([]string{"list"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--experimental-plugins") {
		t.Fatalf("%d %s", code, stderr.String())
	}
}

func TestPluginUsageErrors(t *testing.T) {
	wirePlugins(t)
	for _, args := range [][]string{{}, {"nope"}, {"describe"}, {"list", "extra"}, {"list", "--bad"}} {
		if code, _, _ := pluginCommand("", args...); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestPluginWithoutAHostSaysSo(t *testing.T) {
	old := pluginService
	pluginService = nil
	defer func() { pluginService = old }()
	if code, _, errOut := pluginCommand("", "list"); code != 1 || !strings.Contains(errOut, "no plugin host") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestPluginInstallDeclinedStaysUnapproved(t *testing.T) {
	wirePlugins(t)
	code, out, errOut := pluginCommand("no\n", "install", buildExample(t, "column-example"))
	if code != 0 || !strings.Contains(out, "unapproved") || !strings.Contains(errOut, "stays unapproved") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if code, _, errOut := pluginCommand("", "verify", "column-example"); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("%d %s", code, errOut)
	}
	if code, _, errOut := pluginCommand("", "run", "column-example"); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestPluginVerbsWithJSONAndRemove(t *testing.T) {
	wirePlugins(t)
	if code, out, errOut := pluginCommand("yes\n", "install", "--json", buildExample(t, "column-example")); code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	for _, args := range [][]string{{"list", "--json"}, {"describe", "column-example", "--json"}, {"verify", "column-example", "--json"}} {
		code, out, errOut := pluginCommand("", args...)
		if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(out, "herdsman-plugin/1") {
			t.Fatalf("%v: %d %s %s", args, code, out, errOut)
		}
	}
	if code, out, _ := pluginCommand("", "describe", "column-example"); code != 0 || !strings.Contains(out, "example list column") {
		t.Fatalf("%d %s", code, out)
	}
	if code, out, _ := pluginCommand("", "remove", "column-example", "--json"); code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestPluginRemoveAndFailuresAsText(t *testing.T) {
	wirePlugins(t)
	if code, _, _ := pluginCommand("yes\n", "install", buildExample(t, "column-example")); code != 0 {
		t.Fatal(code)
	}
	if code, out, _ := pluginCommand("", "remove", "column-example"); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("%d %s", code, out)
	}
	for _, args := range [][]string{{"describe", "absent"}, {"remove", "absent"}, {"verify", "absent"}, {"run", "absent"}, {"run", "json"}, {"install", "/absent/file"}} {
		if code, _, _ := pluginCommand("", args...); code != 1 {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestPluginRunPrintsTheHandshake(t *testing.T) {
	wirePlugins(t)
	if code, _, errOut := pluginCommand("yes\n", "install", buildExample(t, "column-example")); code != 0 {
		t.Fatal(errOut)
	}
	// An empty stdin closes at once, which is the plugin's signal to stop.
	code, out, errOut := pluginCommand("", "run", "column-example")
	if code != 0 || !strings.HasPrefix(out, "herdsman-plugin/1 ") {
		t.Fatalf("%d %q %s", code, out, errOut)
	}
}

func TestPluginVerifyReportsAFailure(t *testing.T) {
	service := wirePlugins(t)
	failing := &failingVerify{PluginService: service}
	pluginService = failing
	if code, out, _ := pluginCommand("", "verify", "x"); code != 1 || !strings.Contains(out, "FAIL  scenario: why") {
		t.Fatalf("%d %s", code, out)
	}
	if code, out, _ := pluginCommand("", "verify", "x", "--json"); code != 1 || !json.Valid([]byte(out)) {
		t.Fatalf("%d %s", code, out)
	}
}

type failingVerify struct{ *daemon.PluginService }

func (f *failingVerify) Verify(context.Context, *daemonv1.PluginServiceVerifyRequest) (*daemonv1.PluginServiceVerifyResponse, error) {
	return &daemonv1.PluginServiceVerifyResponse{Checks: []*daemonv1.VerifyCheck{{Name: "scenario", Detail: "why"}}}, nil
}

func TestPluginListReportsAFailure(t *testing.T) {
	service := wirePlugins(t)
	os.MkdirAll(filepath.Dir(service.Host.PluginsDir()), 0o700)
	os.WriteFile(service.Host.PluginsDir(), nil, 0o600)
	if code, _, errOut := pluginCommand("", "list"); code != 1 || errOut == "" {
		t.Fatalf("%d %s", code, errOut)
	}
}
