package daemon

import (
	"errors"
	"os"

	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// repoPolicy reads what governs a route from the repository's configuration.
// The daemon reads it itself, so a caller cannot claim a looser profile. It
// also names the files a sandbox root must never hold.
func repoPolicy(repo string) (policy.Policy, []string, error) {
	result, err := config.Load(config.Options{RepoRoot: repo})
	if err != nil {
		return policy.Policy{}, nil, err
	}
	section := result.Config.GetPolicy()
	parsed, err := policy.Parse(policy.Section{Profile: section.GetProfile(), Roots: section.GetRoots()})
	if err != nil {
		return policy.Policy{}, nil, err
	}
	protected := []string{}
	for _, path := range []string{result.ProjectPath, result.UserPath, config.UserDir(os.Environ())} {
		if path != "" {
			protected = append(protected, path)
		}
	}
	return parsed, protected, nil
}

// refuseLinkedWorktree stops a task that would share the main repository's git
// folder, which the restricted profile exists to prevent.
func refuseLinkedWorktree(repo string) error {
	parsed, _, err := repoPolicy(repo)
	if err != nil {
		return err
	}
	if parsed.EffectiveProfile() == policy.ProfileRestricted {
		return errors.New("profile restricted: a task needs its own clone, which herdsman new does not make yet")
	}
	return nil
}
