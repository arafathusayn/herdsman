package automations

import (
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/sdk/config"
)

// LoadProject reads the [automations] and the [policy] deny list in force for
// a repository: its own file merged with the user's, as every other command
// reads them.
func LoadProject(project string, environ []string) (Config, policy.Policy, error) {
	result, err := config.Load(config.Options{RepoRoot: project, Environ: environ})
	if err != nil {
		return Config{}, policy.Policy{}, err
	}
	parsed, err := FromProto(result.Config.GetAutomations())
	if err != nil {
		return Config{}, policy.Policy{}, err
	}
	return parsed, policy.Policy{Deny: result.Config.GetPolicy().GetDeny()}, nil
}
