// Package plugin runs the plugin conformance scenarios against each example
// plugin, built from the workspace and started as a real process.
package plugin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
)

var examples = []string{"codec-kdl", "detector-example", "backend-gitea", "generator-example", "column-example"}

func build(t *testing.T, name string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "herdsman-plugin-"+name)
	out, err := exec.Command("go", "build", "-o", binary, "github.com/arafathusayn/herdsman/examples/plugins/"+name).CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return binary
}

func TestEveryExamplePassesConformance(t *testing.T) {
	for _, name := range examples {
		t.Run(name, func(t *testing.T) {
			process, err := sdkplugin.Host{Name: name, Timeout: 30 * time.Second}.Start(context.Background(), build(t, name))
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			checks := sdkplugin.Verify(context.Background(), process.Manifest, process.Clients)
			if len(checks) < 2 {
				t.Fatalf("only %d checks ran", len(checks))
			}
			for _, check := range checks {
				if !check.Passed {
					t.Errorf("%s: %s", check.Name, check.Detail)
				}
			}
		})
	}
}

func TestAProcessThatIsNoPluginFailsItsHandshake(t *testing.T) {
	script := filepath.Join(t.TempDir(), "not-a-plugin")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := sdkplugin.Host{Timeout: 10 * time.Second}.Start(context.Background(), script)
	if _, ok := errors.AsType[*sdkplugin.HandshakeError](err); !ok {
		t.Fatalf("%v", err)
	}
}
