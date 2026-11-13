package core

import (
	"slices"
	"testing"
)

func TestProcessArgumentsPreserveSandboxSettings(t *testing.T) {
	rows, err := parseProcesses([]byte(" 123 100 /opt/tools/codex -c default_permissions=herdsman-fixture -c features.network_proxy=true --no-daemon\n"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("process table: %v %v", rows, err)
	}
	p := rows[0]
	if p.PID != 123 || p.PPID != 100 || p.Name() != "codex" || !slices.Contains(p.Argv, "--no-daemon") || !slices.Contains(p.Argv, "features.network_proxy=true") {
		t.Fatalf("configuration arguments truncated the process command: %+v", p)
	}
	for _, raw := range []string{"123\n", "123 bad-parent /bin/sh\n"} {
		if _, err := parseProcesses([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed process row %q", raw)
		}
	}
}
