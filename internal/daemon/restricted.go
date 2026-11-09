package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/profile"
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

// taskShape is how the repository's profile governs a new task.
type taskShape struct {
	policy    policy.Policy
	protected []string
	ownClone  bool
}

// shapeOf reads the profile. An unreadable configuration stops the task, since
// a task that ran looser than its configuration asks could not be undone.
func (r *repository) shapeOf() (taskShape, error) {
	governing, protected, err := repoPolicy(filepath.Dir(r.common))
	if err != nil {
		return taskShape{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return taskShape{}, err
	}
	protected = slices.Concat(protected, []string{r.common}, policyFiles(home))
	return taskShape{policy: governing, protected: protected, ownClone: profile.ShapeFor(governing) == profile.ShapeOwnClone}, nil
}

// admit parses an agent command under the profile before anything is made. It
// works on the parsed words, because the launch line is shell text: a second
// command, a prelude or an environment could each undo the sandbox.
func (t taskShape) admit(line string) ([]string, error) {
	if !t.ownClone || line == "" {
		return nil, nil
	}
	spec, err := launch.Compile(line, launch.Options{})
	if err != nil {
		return nil, err
	}
	if len(spec.Prelude) > 0 || len(spec.Env) > 0 {
		return nil, errors.New("profile restricted: the agent command may hold no prelude and no environment")
	}
	if err := profile.Admit(t.policy, filepath.Base(spec.Command[0])); err != nil {
		return nil, err
	}
	if err := profile.CheckHarnessArgs(t.policy, spec.Command[1:]); err != nil {
		return nil, err
	}
	return spec.Command, nil
}

// launchLine starts the admitted command inside the task's own clone. Roots are
// resolved here because the clone they name did not exist before.
func (t taskShape) launchLine(command []string, path string, integrator bool) (string, error) {
	role := profile.RoleImplementer
	if integrator {
		role = profile.RoleIntegrator
	}
	roots, err := profile.ResolveRoots(profile.RootRequest{Policy: t.policy, Role: role, TaskRoot: path, Protected: t.protected})
	if err != nil {
		return "", err
	}
	sandbox, err := profile.SandboxArgs(filepath.Base(command[0]), roots)
	if err != nil {
		return "", err
	}
	words := make([]string, 0, len(command)+len(sandbox))
	for _, word := range slices.Concat(command[:1], sandbox, command[1:]) {
		words = append(words, launchQuote(word))
	}
	return "cd " + launchQuote(path) + " && " + strings.Join(words, " "), nil
}

func launchQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cloneTask makes the task's own repository from the commit start names, or
// from the branch itself when the branch already exists.
func (r *repository) cloneTask(ctx context.Context, branch, path, start string) error {
	if start == "" {
		start = branch
	}
	return profile.Clone(ctx, profile.CloneOptions{Source: r.main, Dest: path, Base: start, Branch: branch})
}
