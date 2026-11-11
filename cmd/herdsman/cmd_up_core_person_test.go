package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/runtime/core"
)

// upCommands are a launch command per qualified harness on core.
var upCommands = map[string]string{"claude": "claude", "codex": "codex --no-daemon"}

// up names, for every adapter level, the steps that will need a person
// before the route starts, and runtime doctor says the same of each harness:
// the same level, the same reasons, ok only for an unattended harness.
func TestUpAndRuntimeDoctorAgreeOnPersonSteps(t *testing.T) {
	w, _, _ := coreDaemon(t)
	w.OpenApp()

	var doctor bytes.Buffer
	if code := runRuntimeDoctor([]string{"--backend", "herdr-core"}, &doctor, &bytes.Buffer{}); code != 0 {
		t.Fatalf("runtime doctor: %d\n%s", code, doctor.String())
	}
	checks := map[string]string{}
	for _, line := range strings.Split(doctor.String(), "\n") {
		state, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		name, detail, ok := strings.Cut(strings.TrimSpace(rest), ": ")
		if ok && strings.HasPrefix(name, "harness-") {
			checks[name] = state + " " + detail
		}
	}

	levels := map[string]bool{}
	for _, kind := range core.Kinds() {
		a, err := core.AdapterFor(kind)
		if err != nil {
			t.Fatal(err)
		}
		cmd, ok := upCommands[kind]
		if !ok {
			t.Fatalf("no launch command for qualified harness %s", kind)
		}
		levels[a.Facts.Level] = true
		if a.Unattended() != (a.Facts.Level == core.LevelUnattended) {
			t.Errorf("%s: qualified %s, but its wake says unattended=%v", kind, a.Facts.Level, a.Unattended())
		}
		var stdout, stderr bytes.Buffer
		runUp([]string{"harbor-" + kind, "--no-tui", "--backend", "herdr-core", "--no-adjutant", "-o", cmd, "-p", "ship it"}, nil, &stdout, &stderr)
		section := upPersonSteps(stdout.String())
		header := "needs a person (" + kind + ", " + a.Facts.Level + "):"
		if !strings.HasPrefix(section, header) {
			t.Fatalf("%s: up named no person steps before the start:\n%s\n%s", kind, stdout.String(), stderr.String())
		}
		var reasons []string
		for _, line := range strings.Split(strings.TrimPrefix(section, header+"\n"), "\n") {
			if reason, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok {
				reasons = append(reasons, reason)
			}
		}
		if len(reasons) != len(a.NamedPersonSteps()) {
			t.Errorf("%s: up named %v, the adapter has %d steps", kind, reasons, len(a.NamedPersonSteps()))
		}
		got := checks["harness-"+kind]
		wantState := "warn"
		if a.Unattended() {
			wantState = "ok"
		}
		if !strings.HasPrefix(got, wantState+" ") || !strings.Contains(got, a.Facts.Level) {
			t.Errorf("%s: doctor says %q; up says %s", kind, got, header)
		}
		for _, r := range reasons {
			if !strings.Contains(got, r+" (") {
				t.Errorf("%s: up names %s, the doctor does not: %q", kind, r, got)
			}
		}
		for _, s := range a.NamedPersonSteps() {
			if !strings.Contains(section, s.Reason+": "+s.When) || !strings.Contains(got, s.Reason+" ("+s.When+")") {
				t.Errorf("%s: step %s is worded differently by up and doctor:\n%s\n%s", kind, s.Reason, section, got)
			}
		}
	}
	for _, level := range []string{core.LevelUnattended, core.LevelAttended} {
		if !levels[level] {
			t.Errorf("no qualified harness is %s; the test covers no run of that level", level)
		}
	}
}
