package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/launch"
)

// up tells a wait for a person from a failure by the reason's code.
func TestUpTellsAPersonWaitFromAFailure(t *testing.T) {
	for reason, want := range map[string]bool{
		"step 6 failed (needs_attention: submit this start line in the app)":     true,
		"step 4 failed (waiting_for_visibility: open the app on that core once)": true,
		"step 4 failed (visibility_missing: the app does not show terminal t1)":  true,
		"step 2 failed (launch_failed: the wrapper did not register)":            false,
		"the pair is left pending: up runs without a controlling terminal":       false,
	} {
		if got := upPersonWait(reason); got != want {
			t.Errorf("%q: %v", reason, got)
		}
	}
}

// The launch verb is core's to run; without its socket and intent it is a
// usage error with the wrapper's own exit code.
func TestLaunchWithoutItsArgumentsIsAUsageError(t *testing.T) {
	var stderr bytes.Buffer
	if code := runLaunch([]string{"--socket", "/nowhere"}, nil, nil, &stderr); code != launch.ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: herdsman launch") {
		t.Errorf("stderr %q", stderr.String())
	}
}
