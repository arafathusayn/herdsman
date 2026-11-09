package daemon

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/internal/automations"
	"github.com/arafathusayn/herdsman/internal/policy"
)

const automationsUsage = "automations list|approve <event>|dry-run <event> [--project <path>] [--value <name=value>]..."

// eventValues collects repeated --value name=value pairs.
type eventValues map[string]string

func (v eventValues) String() string { return "" }

func (v eventValues) Set(pair string) error {
	name, value, ok := strings.Cut(pair, "=")
	if !ok || name == "" {
		return fmt.Errorf("expected name=value, got %q", pair)
	}
	v[name] = value
	return nil
}

// automationsCmd is `herdsman daemon automations ...`. It works on the
// repository's files and the user's approvals, so it needs no running daemon.
func (a *AdminEnv) automationsCmd(args []string) int {
	if len(args) == 0 {
		return a.usage(automationsUsage)
	}
	verb, rest, event := args[0], args[1:], ""
	if verb != "list" && verb != "approve" && verb != "dry-run" {
		return a.usage(automationsUsage)
	}
	if verb != "list" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return a.usage(automationsUsage)
		}
		event, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("automations "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("project", "", "repository path")
	values := eventValues{}
	if verb == "dry-run" {
		fs.Var(values, "value", "an event value")
	}
	if fs.Parse(rest) != nil || fs.NArg() != 0 {
		return a.usage(automationsUsage)
	}
	if *project == "" {
		*project = enclosingRepository()
	}
	if *project == "" {
		return a.fail(errors.New("automations: not inside a repository; name one with --project"))
	}
	var err error
	switch verb {
	case "list":
		err = a.automationsList(*project)
	case "approve":
		err = a.automationsApprove(*project, event)
	default:
		err = a.automationsDryRun(*project, event, values)
	}
	if err != nil {
		return a.fail(err)
	}
	return 0
}

// enclosingRepository is the nearest folder above the current one that holds
// .git, or "".
func enclosingRepository() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func automationsOpen(project string) (automations.Config, *automations.Approvals, error) {
	environ := os.Environ()
	config, _, err := automations.LoadProject(project, environ)
	if err != nil {
		return automations.Config{}, nil, err
	}
	store, err := automations.DefaultApprovals(environ)
	return config, store, err
}

// automationRow is one action as list prints it.
//
// JSON fields: event, approved, kind, body, checksum.
type automationRow struct {
	Event    string `json:"event"`
	Approved bool   `json:"approved"`
	Kind     string `json:"kind"`
	Body     string `json:"body"`
	Checksum string `json:"checksum"`
}

func (a *AdminEnv) automationsList(project string) error {
	config, store, err := automationsOpen(project)
	if err != nil {
		return err
	}
	rows, lines := []automationRow{}, []string{}
	for _, rule := range config.Rules {
		approved, err := store.Approved(project, rule)
		if err != nil {
			return err
		}
		state := "unapproved"
		if approved {
			state = "approved"
		}
		rows = append(rows, automationRow{rule.Event, approved, string(rule.Kind), rule.Body, automations.Checksum(rule)})
		lines = append(lines, fmt.Sprintf("%s %s %s %s", rule.Event, state, rule.Kind, rule.Body))
	}
	a.out(map[string]any{"enabled": config.Enabled, "actions": rows}, strings.Join(lines, "\n"))
	return nil
}

func (a *AdminEnv) automationsApprove(project, event string) error {
	config, store, err := automationsOpen(project)
	if err != nil {
		return err
	}
	for _, rule := range config.Rules {
		if rule.Event != event {
			continue
		}
		if err := store.Approve(project, rule); err != nil {
			return err
		}
		checksum := automations.Checksum(rule)
		a.out(automationRow{rule.Event, true, string(rule.Kind), rule.Body, checksum},
			fmt.Sprintf("approved %s %s for %s: %s %s", event, checksum, project, rule.Kind, rule.Body))
		return nil
	}
	return fmt.Errorf("automations: no action for event %q in %s", event, project)
}

func (a *AdminEnv) automationsDryRun(project, event string, values map[string]string) error {
	environ := os.Environ()
	store, err := automations.DefaultApprovals(environ)
	if err != nil {
		return err
	}
	engine := &automations.Engine{Approvals: store, Stderr: a.Stderr, DryRun: true,
		Source: func(project string) (automations.Config, policy.Policy, error) {
			return automations.LoadProject(project, environ)
		}}
	outcomes := engine.Fire(context.Background(), automations.Event{Key: "dry-run", Kind: event, Project: project, Values: values})
	if len(outcomes) == 0 {
		return fmt.Errorf("automations: no enabled action for event %q in %s", event, project)
	}
	lines := []string{}
	failed := false
	for _, out := range outcomes {
		line := fmt.Sprintf("%s %s %s", out.Event, out.Status, out.Rendered)
		if out.Error != "" {
			line += " (" + out.Error + ")"
		}
		lines = append(lines, line)
		failed = failed || out.Status != automations.StatusWouldRun
	}
	a.out(map[string]any{"outcomes": outcomes}, strings.Join(lines, "\n"))
	if failed {
		return fmt.Errorf("automations: the action for %q would not run", event)
	}
	return nil
}
