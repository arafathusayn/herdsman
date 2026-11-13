package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/wtree"
	"github.com/arafathusayn/herdsman/sdk/config"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
)

// setupHook names the configured install command in a hook list, which needs
// the same approval as a hook.
const setupHook = "setup"

// jsonHooks reads the hooks of a herdsman.config.json beside the repository.
// That file may give a command a timeout, which the shared schema cannot say,
// so a hooks table there is taken as written.
func jsonHooks(folder string) (map[string]any, bool, error) {
	path := filepath.Join(folder, "herdsman.config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var file struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return file.Hooks, true, nil
}

func namedCommands(named *configv1.NamedCommands) map[string]any {
	commands := make(map[string]any, len(named.GetCommands()))
	for name, command := range named.GetCommands() {
		commands[name] = command
	}
	return commands
}

// hookValue turns a loaded hook into the shape hooks.Parse reads.
func hookValue(spec *configv1.HookSpec) any {
	switch form := spec.GetForm().(type) {
	case *configv1.HookSpec_Command:
		return form.Command
	case *configv1.HookSpec_Named:
		return namedCommands(form.Named)
	case *configv1.HookSpec_Pipeline:
		stages := make([]any, 0, len(form.Pipeline.GetStages()))
		for _, stage := range form.Pipeline.GetStages() {
			if named := stage.GetNamed(); named != nil {
				stages = append(stages, namedCommands(named))
			} else {
				stages = append(stages, stage.GetCommand())
			}
		}
		return stages
	}
	return nil
}

func loadedHooks(section *configv1.Hooks) map[string]any {
	specs := map[string]*configv1.HookSpec{
		"pre-switch": section.GetPreSwitch(), "post-switch": section.GetPostSwitch(),
		"pre-start": section.GetPreStart(), "post-start": section.GetPostStart(),
		"pre-commit": section.GetPreCommit(), "post-commit": section.GetPostCommit(),
		"pre-merge": section.GetPreMerge(), "post-merge": section.GetPostMerge(),
		"pre-teardown": section.GetPreTeardown(), "post-teardown": section.GetPostTeardown(),
		"pre-clean": section.GetPreClean(), "post-clean": section.GetPostClean(),
	}
	raw := map[string]any{}
	for name, spec := range specs {
		if value := hookValue(spec); value != nil {
			raw[name] = value
		}
	}
	return raw
}

// projectKey is the repository's host and path, which selects the overrides of
// the user's file.
func (r *repository) projectKey(ctx context.Context) string {
	out, err := r.git.Run(ctx, r.main, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return remoteAddress(string(out.Stdout))
}

// loadSettings reads what the verbs need from the layered configuration. A file
// that cannot be read stops the command: running without the hooks someone
// wrote would be worse than refusing.
func (s *WorktreeService) loadSettings(ctx context.Context, r *repository) error {
	// Two files side by side are refused here because the fallback below would
	// otherwise let the JSON one win without a word.
	if _, err := config.FileIn(filepath.Dir(r.main)); err != nil {
		return err
	}
	written, hasJSON, err := jsonHooks(filepath.Dir(r.main))
	if err != nil {
		return err
	}
	r.naming = wtree.DefaultNaming()
	r.ports = wtree.PortBlock
	loaded, err := config.Load(config.Options{RepoRoot: r.main, UserDir: s.ConfigDir, ProjectKey: r.projectKey(ctx), Environ: s.caller})
	if err != nil {
		var strict *config.StrictError
		if !hasJSON || errors.As(err, &strict) {
			return err
		}
		// This file predates the shared schema and may not fit it: a timeout
		// table, or values only the commit verb reads. It keeps working alone.
		r.hooks, err = hooks.Parse(written)
		return err
	}
	raw := written
	if raw == nil {
		raw = loadedHooks(loaded.Config.GetHooks())
	}
	if r.hooks, err = hooks.Parse(raw); err != nil {
		return err
	}
	if path := loaded.Config.GetWorktree().GetPath(); path != "" {
		r.naming.WorktreePath = path
	}
	if name := loaded.Config.GetBranch().GetName(); name != "" {
		r.naming.BranchName = name
	}
	setup := loaded.Config.GetSetup()
	r.envFiles, r.shared = setup.GetEnv(), setup.GetShare()
	if setup.Ports != nil {
		r.ports = int(setup.GetPorts())
	}
	if r.ports < 0 || r.ports > wtree.PortBlock {
		return fmt.Errorf("setup.ports is %d; a task's block holds at most %d", r.ports, wtree.PortBlock)
	}
	// A command only detected from the repository's files comes back as a
	// default. setupPlan offers it; only one a person wrote is a hook to approve.
	install := strings.TrimSpace(loaded.Config.GetSetup().GetInstall())
	if install != "" && loaded.Sources["setup.install"] != config.SourceDefault {
		if r.hooks == nil {
			r.hooks = hooks.Config{}
		}
		r.hooks[setupHook] = []hooks.Step{{{Name: setupHook, Template: install}}}
	}
	return nil
}

// setupPlan is the configured install command, or what the worktree's files
// suggest when none is configured.
func (s *WorktreeService) setupPlan(r *repository, vars wtree.Vars, detected wtree.Plan) (hooks.Plan, []string, error) {
	if len(r.hooks[setupHook]) > 0 {
		plan, err := s.plan(r, setupHook, vars)
		if err != nil {
			return nil, nil, err
		}
		return plan, []string{plan[0][0].Rendered}, nil
	}
	var plan hooks.Plan
	for _, install := range detected.Install {
		plan = append(plan, []hooks.Planned{{Command: hooks.Command{Name: setupHook, Template: install}, Rendered: install}})
	}
	return plan, detected.Install, nil
}
