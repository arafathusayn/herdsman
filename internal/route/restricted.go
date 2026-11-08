package route

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/profile"
	"github.com/arafathusayn/herdsman/internal/secrets"
)

// paneEnv is the environment for a new pane. Secret values are read here, as
// the pane starts, so that they never pass through the journal.
func paneEnv(deps Deps, d stepData) (map[string]string, error) {
	if d.SecretRefs == "" {
		return d.Env, nil
	}
	refs, err := secrets.ParseRefs(d.SecretRefs)
	if err != nil {
		return nil, err
	}
	keychain := deps.Keychain
	if keychain == nil {
		keychain = secrets.System()
	}
	set, err := secrets.Load(keychain, refs)
	if err != nil {
		return nil, err
	}
	env := maps.Clone(d.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, kv := range set.Environ(nil) {
		name, value, _ := strings.Cut(kv, "=")
		env[name] = value
	}
	return env, nil
}

// confinedArgs is the harness command line, with the sandbox in front of it
// when the step has one.
func confinedArgs(d stepData) ([]string, error) {
	if d.Sandbox == nil {
		return d.Args, nil
	}
	roots, err := profile.ResolveRoots(profile.RootRequest{
		Policy: policy.Policy{Profile: policy.ProfileRestricted}, Role: profile.RoleImplementer,
		TaskRoot: d.Sandbox.Root, Grants: d.Sandbox.Grants, Protected: d.Sandbox.Protected,
	})
	if err != nil {
		return nil, err
	}
	args, err := profile.SandboxArgs(d.Kind, roots)
	if err != nil {
		return nil, err
	}
	return append(args, d.Args...), nil
}

func cloneExists(d stepData) (bool, error) {
	_, err := os.Stat(filepath.Join(d.Clone, ".git"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func applyClone(ctx context.Context, _ Deps, _ *herdr.Client, d stepData) error {
	exists, err := cloneExists(d)
	if err != nil || exists {
		return err
	}
	return profile.Clone(ctx, profile.CloneOptions{Source: d.Repo, Dest: d.Clone, Branch: d.Branch})
}

func probeClone(_ context.Context, _ Deps, _ *herdr.Client, d stepData) (ops.Verdict, string, error) {
	exists, err := cloneExists(d)
	switch {
	case err != nil:
		return ops.Unknown, "", err
	case exists:
		return ops.Matches, "", nil
	}
	return ops.Absent, "", nil
}
