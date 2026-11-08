// Package hooks reads, approves and runs the commands a repository attaches to
// the points of a worktree's life.
package hooks

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

// Types are the hook points, in the order a worktree meets them.
var Types = []string{
	"pre-switch", "post-switch", "pre-start", "post-start", "pre-commit", "post-commit",
	"pre-merge", "post-merge", "pre-teardown", "post-teardown", "pre-clean", "post-clean",
}

// Command is one template to run. Timeout zero means no limit.
type Command struct {
	Name     string
	Template string
	Timeout  time.Duration
}

// Step is a set of commands that run together.
type Step []Command

// Config holds the steps of each hook point; steps run in order.
type Config map[string][]Step

// Planned is a command with its template expanded.
type Planned struct {
	Command
	Rendered string
}

// Plan is what a hook would run, step by step.
type Plan [][]Planned

// Parse reads the decoded hooks table. A hook is a string (one command), a map
// (named commands that run together) or a list (steps in order, each a string
// or a map).
func Parse(table map[string]any) (Config, error) {
	config := make(Config, len(table))
	for key, value := range table {
		hook := strings.ReplaceAll(key, "_", "-")
		if !slices.Contains(Types, hook) {
			return nil, fmt.Errorf("hooks: unknown hook %q", key)
		}
		steps, err := parseHook(hook, value)
		if err != nil {
			return nil, fmt.Errorf("hooks: %s: %w", hook, err)
		}
		config[hook] = steps
	}
	return config, nil
}

func parseHook(hook string, value any) ([]Step, error) {
	list, isList := value.([]any)
	if !isList {
		step, err := parseStep(hook, value)
		if err != nil {
			return nil, err
		}
		return []Step{step}, nil
	}
	if len(list) == 0 {
		return nil, errors.New("empty list of steps")
	}
	steps := make([]Step, 0, len(list))
	for _, item := range list {
		step, err := parseStep(hook, item)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func parseStep(hook string, value any) (Step, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, errors.New("empty command")
		}
		return Step{{Name: hook, Template: v}}, nil
	case map[string]any:
		if len(v) == 0 {
			return nil, errors.New("empty table of commands")
		}
		step := make(Step, 0, len(v))
		for _, name := range slices.Sorted(maps.Keys(v)) {
			command, err := parseCommand(name, v[name])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			step = append(step, command)
		}
		return step, nil
	}
	return nil, fmt.Errorf("a step is a string or a table, not %T", value)
}

func parseCommand(name string, value any) (Command, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return Command{}, errors.New("empty command")
		}
		return Command{Name: name, Template: v}, nil
	case map[string]any:
		return parseCommandTable(name, v)
	}
	return Command{}, fmt.Errorf("a command is a string or a table, not %T", value)
}

func parseCommandTable(name string, table map[string]any) (Command, error) {
	for key := range table {
		if key != "command" && key != "timeout" {
			return Command{}, fmt.Errorf("unknown field %q", key)
		}
	}
	text, ok := table["command"].(string)
	if !ok || text == "" {
		return Command{}, errors.New("command must be a non-empty string")
	}
	command := Command{Name: name, Template: text}
	raw, present := table["timeout"]
	if !present {
		return command, nil
	}
	spelled, ok := raw.(string)
	if !ok {
		return Command{}, errors.New(`timeout must be a duration string such as "90s"`)
	}
	timeout, err := time.ParseDuration(spelled)
	if err != nil {
		return Command{}, fmt.Errorf("timeout: %w", err)
	}
	if timeout <= 0 {
		return Command{}, errors.New("timeout must be positive")
	}
	command.Timeout = timeout
	return command, nil
}

// Plan expands the templates of one hook. Each command also sees hook_type and
// its own hook_name.
func (c Config) Plan(hook string, vars wtree.Vars) (Plan, error) {
	plan := make(Plan, 0, len(c[hook]))
	for _, step := range c[hook] {
		planned := make([]Planned, 0, len(step))
		for _, command := range step {
			scoped := vars
			scoped.Values = maps.Clone(vars.Values)
			if scoped.Values == nil {
				scoped.Values = map[string]string{}
			}
			scoped.Values["hook_type"] = hook
			scoped.Values["hook_name"] = command.Name
			rendered, err := wtree.Render(hook+" "+command.Name, command.Template, scoped)
			if err != nil {
				return nil, fmt.Errorf("hooks: %s %s: %w", hook, command.Name, err)
			}
			planned = append(planned, Planned{Command: command, Rendered: rendered})
		}
		plan = append(plan, planned)
	}
	return plan, nil
}
