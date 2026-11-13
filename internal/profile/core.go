package profile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// CoreSandboxConfig grants a restricted Codex worker its resolved roots and
// exactly the worker socket. The proxy has no allowed network destinations.
// Each launch uses a fresh profile name: Codex merges permission tables, so
// a fixed name could inherit broader grants from a lower config layer.
func CoreSandboxConfig(harness string, r Roots, socket string) ([]string, error) {
	if harness != HarnessCodex {
		return nil, fmt.Errorf("harness %q has no qualified core sandbox", harness)
	}
	if !filepath.IsAbs(socket) || strings.ContainsFunc(socket, unicode.IsControl) {
		return nil, errors.New("core sandbox needs an absolute worker socket path without control characters")
	}
	roots := append([]string{r.Primary}, writable(r)...)
	entries := []string{`":root"="read"`, `":tmpdir"="write"`, `":slash_tmp"="write"`}
	for _, root := range roots {
		if !filepath.IsAbs(root) || strings.ContainsFunc(root, unicode.IsControl) {
			return nil, fmt.Errorf("invalid core sandbox root %q", root)
		}
		entries = append(entries, strconv.Quote(root)+`="write"`)
		// Keep Codex's configuration and credentials read-only inside each root.
		for _, name := range []string{".agents", ".codex", ".aws"} {
			entries = append(entries, strconv.Quote(filepath.Join(root, name))+`="read"`)
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name := "herdsman-" + hex.EncodeToString(nonce[:])
	config := `permissions={` + name + `={filesystem={` + strings.Join(entries, ",") + `},network={enabled=true,domains={},unix_sockets={` + strconv.Quote(socket) + `="allow"},dangerously_allow_all_unix_sockets=false,allow_upstream_proxy=false,allow_local_binding=false}}}`
	return []string{"-c", `default_permissions=` + strconv.Quote(name), "-c", `features.network_proxy=true`, "-c", config}, nil
}

// CheckCoreHarnessArgs covers switches that can replace the permission
// profile, relocate the process, or disable the required socket proxy.
func CheckCoreHarnessArgs(args []string) error {
	if err := CheckHarnessArgs(policy.Policy{Profile: policy.ProfileRestricted}, args); err != nil {
		return err
	}
	for i, arg := range args {
		key, value, joined := strings.Cut(arg, "=")
		switch {
		case key == "--profile" || key == "-p" || strings.HasPrefix(arg, "-p") && !strings.HasPrefix(arg, "--"),
			key == "--remote", key == "--remote-auth-token-env", key == "--worktree", key == "--full-auto", key == "--approve-for-me":
			return fmt.Errorf("restricted core worker refuses %q", arg)
		case key == "--enable" || key == "--disable":
			if !joined && i+1 < len(args) {
				value = args[i+1]
			}
			if value == "network_proxy" || strings.Contains(value, ",") {
				return fmt.Errorf("restricted core worker owns the network_proxy feature")
			}
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) && coreSandboxKey(args[i+1]) {
				return fmt.Errorf("restricted core worker refuses %q", args[i+1])
			}
		case key == "--config" && joined:
			if coreSandboxKey(value) {
				return fmt.Errorf("restricted core worker refuses %q", arg)
			}
		case strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--"):
			if coreSandboxKey(arg[2:]) {
				return fmt.Errorf("restricted core worker refuses %q", arg)
			}
		}
	}
	return nil
}

func coreSandboxKey(setting string) bool {
	key, _, _ := strings.Cut(setting, "=")
	key = normalizedConfigKey(key)
	return key == "features" || strings.HasPrefix(key, "features.network_proxy") || strings.HasPrefix(key, "experimental_network") || strings.HasPrefix(key, "profiles")
}

func normalizedConfigKey(key string) string {
	// Escaped TOML keys are unnecessary in a harness override. Treat them
	// as a sandbox key rather than trying to implement TOML escaping here.
	if strings.Contains(key, `\`) {
		return "sandbox"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '\'' || r == '"' {
			return -1
		}
		return r
	}, key)
}

// CheckCoreSandbox verifies the selected permissions profile using
// Codex's resolved diagnostics, not a partial parser of its config layers.
// No model is run. Only the explicitly qualified CLI's redacted JSON fields
// are accepted; malformed or missing evidence fails closed.
func CheckCoreSandbox(ctx context.Context, binary, cwd string, env, config []string) error {
	expected := ""
	for _, arg := range config {
		if value, ok := strings.CutPrefix(arg, "default_permissions="); ok {
			expected, _ = strconv.Unquote(value)
		}
	}
	if !strings.HasPrefix(expected, "herdsman-") || len(expected) != len("herdsman-")+32 {
		return errors.New("invalid generated core permissions profile")
	}
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env, version.Dir = env, cwd
	raw, err := version.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "codex-cli 0.164.0" {
		return errors.New("restricted core workers require qualified Codex 0.164.0")
	}
	cmd := exec.CommandContext(ctx, binary, append([]string{"doctor", "--json"}, config...)...)
	cmd.Env, cmd.Dir = env, cwd
	raw, runErr := cmd.Output()
	var doc struct {
		Schema  int    `json:"schemaVersion"`
		Version string `json:"codexVersion"`
		Checks  map[string]struct {
			Status  string         `json:"status"`
			Details map[string]any `json:"details"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("cannot verify Codex permissions profile: %v", errors.Join(runErr, err))
	}
	paths, proxy, helpers := doc.Checks["sandbox.filesystem_paths"], doc.Checks["network.env"], doc.Checks["sandbox.helpers"]
	if doc.Schema != 1 || doc.Version != "0.164.0" || paths.Status != "ok" || paths.Details["permission profile"] != expected || proxy.Details["managed proxy"] != "configured" || helpers.Details["filesystem sandbox"] != "restricted" {
		return errors.New("codex did not select the restricted herdsman permissions profile and network proxy; inspect its resolved permission configuration")
	}
	return nil
}

// CheckCoreLaunch verifies both the user's overrides and Codex's resolved
// sandbox before the launch wrapper registers or executes the harness.
func CheckCoreLaunch(ctx context.Context, binary, cwd string, env, config, args []string) error {
	if err := CheckCoreHarnessArgs(args); err != nil {
		return err
	}
	return CheckCoreSandbox(ctx, binary, cwd, env, config)
}
