package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/arafathusayn/herdsman/internal/policy"
)

// HarnessCodex is the one harness whose folder sandbox herdsman can rely on.
const HarnessCodex = "codex"

// Admit refuses to start a harness under a profile it cannot be held to.
func Admit(p policy.Policy, harness string) error {
	if p.EffectiveProfile() == policy.ProfileRestricted && harness != HarnessCodex {
		return fmt.Errorf("profile restricted cannot confine harness %q: only %s has a folder sandbox", harness, HarnessCodex)
	}
	return nil
}

// SandboxConfig is the harness configuration that confines writes to roots.
// Codex keeps a .git folder read-only even inside a writable root, so each
// root's .git is granted by name or a task could not commit.
//
// Codex also leaves the temporary folders writable, which compilers need;
// the guarantee is about the repository, its siblings and the configuration.
func SandboxConfig(harness string, r Roots) ([]string, error) {
	if harness != HarnessCodex {
		return nil, fmt.Errorf("harness %q has no folder sandbox", harness)
	}
	var quoted []string
	for _, root := range writable(r) {
		if strings.ContainsFunc(root, unicode.IsControl) {
			return nil, fmt.Errorf("sandbox root %q holds a control character", root)
		}
		quoted = append(quoted, strconv.Quote(root))
	}
	return []string{
		"-c", `sandbox_mode="workspace-write"`,
		"-c", "sandbox_workspace_write.writable_roots=[" + strings.Join(quoted, ",") + "]",
	}, nil
}

// SandboxArgs is the command line that starts the harness inside r.Primary.
func SandboxArgs(harness string, r Roots) ([]string, error) {
	config, err := SandboxConfig(harness, r)
	if err != nil {
		return nil, err
	}
	if strings.ContainsFunc(r.Primary, unicode.IsControl) {
		return nil, fmt.Errorf("sandbox root %q holds a control character", r.Primary)
	}
	return append([]string{"--cd", r.Primary}, config...), nil
}

// writable lists what the sandbox must grant beyond its working folder, whose
// files it already allows.
func writable(r Roots) []string {
	out := gitFolder(nil, r.Primary)
	for _, root := range r.Extra {
		out = gitFolder(append(out, root), root)
	}
	return out
}

func gitFolder(out []string, root string) []string {
	dot := filepath.Join(root, ".git")
	if info, err := os.Stat(dot); err == nil && info.IsDir() {
		return append(out, dot)
	}
	return out
}

// CheckHarnessArgs refuses arguments that would change or lift the sandbox,
// which the harness would honour over the configuration given before them.
func CheckHarnessArgs(p policy.Policy, args []string) error {
	if p.EffectiveProfile() != policy.ProfileRestricted {
		return nil
	}
	refuse := func(arg string) error {
		return fmt.Errorf("profile restricted sets the sandbox itself, so %q is refused", arg)
	}
	for i, arg := range args {
		name, value, joined := strings.Cut(arg, "=")
		switch {
		case arg == "--dangerously-bypass-approvals-and-sandbox", arg == "--yolo",
			name == "--sandbox", name == "--add-dir", name == "--cd",
			strings.HasPrefix(arg, "-s"), strings.HasPrefix(arg, "-C"):
			return refuse(arg)
		case arg == "-c" || arg == "--config":
			if i+1 == len(args) || sandboxKey(args[i+1]) {
				return refuse(arg)
			}
		case name == "--config" && joined:
			if sandboxKey(value) {
				return refuse(arg)
			}
		case strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--"):
			if sandboxKey(arg[2:]) {
				return refuse(arg)
			}
		}
	}
	return nil
}

func sandboxKey(setting string) bool {
	key, _, _ := strings.Cut(setting, "=")
	key = normalizedConfigKey(key)
	return strings.HasPrefix(key, "sandbox") || strings.HasPrefix(key, "permissions") ||
		key == "default_permissions" || key == "cd" || key == "cwd"
}
