package profile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/policy"
)

func TestCoreSandboxOwnsPermissionsAndSocketProxy(t *testing.T) {
	args, err := CoreSandboxConfig("codex", Roots{Primary: "/work/task", Extra: []string{"/work/report"}}, "/run/worker.sock")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(args, "\n")
	for _, want := range []string{`default_permissions="herdsman-`, `features.network_proxy=true`, `"/run/worker.sock"="allow"`, `"/work/task"="write"`, `"/work/report"="write"`, `domains={}`, `dangerously_allow_all_unix_sockets=false`} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{"relative", "/run/worker\nsock"} {
		if _, err := CoreSandboxConfig("codex", Roots{Primary: "/work"}, bad); err == nil {
			t.Fatal("accepted invalid socket")
		}
	}
	if _, err := CoreSandboxConfig("claude", Roots{Primary: "/work"}, "/run/worker.sock"); err == nil {
		t.Fatal("accepted unqualified harness")
	}
	for _, args := range [][]string{
		{"--disable", "network_proxy"}, {"--disable=network_proxy"}, {"--enable=other,network_proxy"},
		{"-c", `features={network_proxy=false}`}, {"-c", `features."network_proxy"=false`},
		{"--config", `"sandbox_mode"="danger-full-access"`}, {"-c", `'permissions'.other.filesystem={}`},
		{"-c", `"\u0073andbox_mode"="danger-full-access"`}, {"--profile", "unrestricted"}, {"-punrestricted"}, {"--remote=unix://other"}, {"--worktree"},
	} {
		if err := CheckCoreHarnessArgs(args); err == nil {
			t.Errorf("accepted override %q", args)
		}
	}
	if err := CheckCoreHarnessArgs([]string{"--no-daemon", "--model", "fixture", "-c", "model_reasoning_effort=high"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckHarnessArgs(policy.Policy{Profile: policy.ProfileRestricted}, []string{"-c", `"sandbox_mode"="danger-full-access"`}); err == nil {
		t.Fatal("quoted TOML key bypassed restricted profile")
	}
}

func TestCoreSandboxRejectsLegacyPrecedenceAndMissingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		good       bool
	}{
		{"qualified", `{"schemaVersion":1,"codexVersion":"0.164.0","checks":{"updates.status":{"details":{"version cache":[]}},"sandbox.filesystem_paths":{"status":"ok","details":{"permission profile":"herdsman-0123456789abcdef0123456789abcdef"}},"network.env":{"details":{"managed proxy":"configured"}},"sandbox.helpers":{"details":{"filesystem sandbox":"restricted"}}}}`, true},
		{"legacy", `{"schemaVersion":1,"codexVersion":"0.164.0","checks":{"updates.status":{"details":{"version cache":[]}},"sandbox.filesystem_paths":{"status":"ok","details":{"permission profile":"legacy"}},"network.env":{"details":{"managed proxy":"configured"}},"sandbox.helpers":{"details":{"filesystem sandbox":"restricted"}}}}`, false},
		{"missing", "{}", false},
		{"malformed", "not-json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "codex")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.164.0'; exit; fi\ncat <<'RESULT'\n" + tc.body + "\nRESULT\nexit 1\n"
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := CheckCoreSandbox(context.Background(), binary, dir, []string{"PATH=/bin:/usr/bin"}, []string{"-c", `default_permissions="herdsman-0123456789abcdef0123456789abcdef"`})
			if (err == nil) != tc.good {
				t.Fatalf("good=%v error=%v", tc.good, err)
			}
		})
	}
}
