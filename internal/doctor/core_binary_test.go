package doctor

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// [runtime.core] binary is the hc program the doctor runs: a configured one
// must be there; without one the installed names are looked up.
func TestCoreBinaryIsTheConfiguredOne(t *testing.T) {
	look := func(found map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if p, ok := found[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		}
	}
	got, err := coreBinary("/opt/hc", look(map[string]string{"/opt/hc": "/opt/hc", "hc": "/usr/bin/hc"}))
	if err != nil || got != "/opt/hc" {
		t.Fatalf("configured: %q %v", got, err)
	}
	if _, err := coreBinary("/opt/missing", look(map[string]string{"hc": "/usr/bin/hc"})); !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "[runtime.core] binary") {
		t.Fatalf("a configured binary that is not there: %v", err)
	}
	if got, err := coreBinary("", look(map[string]string{"herdr-core": "/usr/bin/herdr-core"})); err != nil || got != "/usr/bin/herdr-core" {
		t.Fatalf("installed: %q %v", got, err)
	}
	if got, err := coreBinary("", look(nil)); err != nil || got != "" {
		t.Fatalf("none installed: %q %v", got, err)
	}
}

// The hc program runs against the resolved socket: an agent's HC_SOCKET,
// HC_SESSION and HC_TERMINAL_ID never reach it.
func TestCoreCommandPinsTheResolvedSocket(t *testing.T) {
	cmd := coreCommand(context.Background(), "/opt/hc", "/run/herdr-core/work.sock",
		[]string{"PATH=/usr/bin", "HC_SOCKET=/agent.sock", "HC_SESSION=agent", "HC_TERMINAL_ID=t9"}, "server", "status", "--json")
	want := []string{"PATH=/usr/bin", "HC_SOCKET=/run/herdr-core/work.sock"}
	if !slices.Equal(cmd.Env, want) || !slices.Equal(cmd.Args, []string{"/opt/hc", "server", "status", "--json"}) {
		t.Fatalf("env %v args %v", cmd.Env, cmd.Args)
	}
}
