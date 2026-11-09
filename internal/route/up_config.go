package route

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/arafathusayn/herdsman/internal/launch"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
)

// ConfigLaunch turns the configuration's [launch] section into a checked spec.
// A file with no such section gives no spec.
func ConfigLaunch(section *configv1.Launch, repo string) (*launch.Spec, error) {
	if section == nil || (len(section.GetCommand()) == 0 && len(section.GetPrelude()) == 0) {
		return nil, nil
	}
	if len(section.GetCommand()) == 0 {
		return nil, errors.New("[launch] has a prelude but no command")
	}
	spec := launch.Spec{Command: section.GetCommand(), Env: section.GetEnv(), Cwd: section.GetCwd()}
	if spec.Cwd == "" {
		spec.Cwd = repo
	} else if !filepath.IsAbs(spec.Cwd) {
		spec.Cwd = filepath.Join(repo, spec.Cwd)
	}
	if text := section.GetPreludeTimeout(); text != "" {
		timeout, err := time.ParseDuration(text)
		if err != nil {
			return nil, fmt.Errorf("[launch] prelude_timeout %q: %w", text, err)
		}
		spec.PreludeTimeout = timeout
	}
	for i, step := range section.GetPrelude() {
		if len(step.GetArgv()) == 0 {
			return nil, fmt.Errorf("[launch] prelude step %d has no argv", i+1)
		}
		out := launch.Step{Command: launch.Command{Env: sortedEnv(step.GetEnv()), Argv: step.GetArgv()}, Once: step.GetOnce()}
		switch step.GetRetry() {
		case configv1.RetryPolicy_RETRY_POLICY_NEVER:
			out.Retry = launch.RetryNever
		case configv1.RetryPolicy_RETRY_POLICY_IDEMPOTENT:
			out.Retry = launch.RetryIdempotent
		}
		if len(step.GetProbe()) > 0 {
			out.Probe = &launch.Command{Argv: step.GetProbe()}
		}
		spec.Prelude = append(spec.Prelude, out)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("[launch]: %w", err)
	}
	return &spec, nil
}

// sortedEnv keeps the printed steps the same from one run to the next: a second
// up compares them with the journaled ones.
func sortedEnv(env map[string]string) []launch.EnvVar {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]launch.EnvVar, 0, len(names))
	for _, name := range names {
		out = append(out, launch.EnvVar{Name: name, Value: env[name]})
	}
	return out
}
