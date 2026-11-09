package doctor

import (
	"fmt"

	"github.com/arafathusayn/herdsman/internal/secrets"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// The profile and harness this report names. They are spelled here because
// doctor is reachable from CLI verbs, which may not import the policy engine.
const (
	profileRestricted   = "restricted"
	profileTrustedLocal = "trusted-local"
	harnessCodex        = "codex"
)

// checkEnforcement says what is really enforced on this machine. A restriction
// the profile needs and the machine cannot provide is a failure, because that
// profile exists only where its restrictions hold.
func checkEnforcement(p Probes, add adder) {
	checkSecretRefs(p, add)
	if p.Profile == nil {
		add(Warn, "enforcement", "profile unknown: this build does not read the policy file, so restricted cannot be confirmed")
		return
	}
	active, err := p.Profile()
	if err != nil {
		add(Fail, "enforcement", "policy unreadable: "+err.Error())
		return
	}
	if active != "" && active != profileRestricted && active != profileTrustedLocal {
		add(Fail, "enforcement", fmt.Sprintf("unknown profile %q: nothing can enforce it", active))
		return
	}
	if active != profileRestricted {
		if active == "" {
			active = profileTrustedLocal
		}
		add(OK, "enforcement", fmt.Sprintf("profile %s: only operations that pass through herdsman are governed", active))
		add(OK, "enforcement", "no sandbox is applied to agents under this profile")
		return
	}
	add(OK, "enforcement", "profile restricted: --pair manual is required")
	requireTool(p, add, harnessCodex, "folder sandbox limits each task to its own root")
	requireTool(p, add, "git", "each task gets its own clone, sharing no objects")
	if p.GOOS == "darwin" {
		add(Warn, "enforcement", "macOS has no sandbox for other harnesses: only codex is admitted")
	} else {
		add(Warn, "enforcement", "Landlock is not applied: only codex is admitted")
	}
}

func requireTool(p Probes, add adder, tool, promise string) {
	if _, err := p.LookPath(tool); err != nil {
		add(Fail, "enforcement", fmt.Sprintf("%s not found: cannot provide what restricted needs (%s)", tool, promise))
		return
	}
	add(OK, "enforcement", tool+": "+promise)
}

// ConfiguredProfile reads the profile from the configuration of repoRoot, ""
// when no file sets one.
func ConfiguredProfile(repoRoot string) func() (string, error) {
	return func() (string, error) {
		result, err := config.Load(config.Options{RepoRoot: repoRoot})
		if err != nil {
			return "", err
		}
		return result.Config.GetPolicy().GetProfile(), nil
	}
}

// checkSecretRefs validates the reference list without reading any value, so
// that a report can never prompt for or contain a secret.
func checkSecretRefs(p Probes, add adder) {
	refs, err := secrets.ParseRefs(p.Getenv(secrets.RefsVariable))
	if err != nil {
		add(Fail, "enforcement", err.Error())
		return
	}
	if len(refs) == 0 {
		return
	}
	if _, err := p.LookPath(secrets.ToolName); err != nil {
		add(Fail, "enforcement", secrets.ToolName+" not found: the listed secrets cannot be read")
		return
	}
	add(OK, "enforcement", fmt.Sprintf("%d secrets come from the keychain and are redacted from logs and tails", len(refs)))
}
