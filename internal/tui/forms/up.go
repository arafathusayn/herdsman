package forms

import (
	"errors"
	"fmt"
	"os"

	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/routename"
	"github.com/arafathusayn/herdsman/internal/tui/components"
)

// noAdjutant is the adjutant answer that starts a route without one.
const noAdjutant = "none"

// UpDefaults prefills the form that starts a route.
type UpDefaults struct {
	Common
	// Harnesses are the programs found on this machine.
	Harnesses []string
	// Config is the discovered configuration file, if any.
	Config string
}

func routeName(v any) error {
	if !routename.Valid(text(v)) {
		return errors.New("use letters, digits, dots, dashes and underscores")
	}
	return nil
}

// launchLine holds a command to the rules the flags use.
func launchLine(v any) error {
	line := text(v)
	if line == "" {
		return errors.New("a command is required")
	}
	if _, err := launch.Compile(line, launch.Options{}); err != nil {
		return err
	}
	return nil
}

func adjutantLine(v any) error {
	if text(v) == noAdjutant {
		return nil
	}
	return launchLine(v)
}

func configFile(v any) error {
	path := text(v)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no such file: %s", path)
	}
	return nil
}

func upArgs(v []any) []string {
	args := []string{"up", text(v[0]), "-p", text(v[1]), "-o", text(v[3])}
	if adjutant := text(v[2]); adjutant == noAdjutant {
		args = append(args, "--no-adjutant")
	} else {
		args = append(args, "-a", adjutant)
	}
	if config := text(v[4]); config != "" {
		args = append(args, "-c", config)
	}
	if on(v[5]) {
		args = append(args, "--web")
	}
	return append(args, "--no-tui")
}

// Up is the form that starts a route. Its last screen is the dry run.
func Up(d UpDefaults) Spec {
	second := 0
	if len(d.Harnesses) > 1 {
		second = 1
	}
	adjutants := append(append([]string{}, d.Harnesses...), noAdjutant)
	first := 0
	if len(d.Harnesses) == 0 {
		first = -1
	}
	form := components.NewForm("Start a route", d.Style,
		components.Entry{Field: components.NewTextInput("Route name", ""), Validate: routeName},
		components.Entry{Field: components.NewTextArea("Prompt", ""), Validate: required("a prompt is required")},
		components.Entry{Field: newCommandField("Adjutant command, or none", adjutants, first), Validate: adjutantLine},
		components.Entry{Field: newCommandField("Orchestrator command", d.Harnesses, second), Validate: launchLine},
		components.Entry{Field: components.NewTextInput("Config file", d.Config), Validate: configFile},
		components.Entry{Field: components.NewConfirm("Start the web app", false)},
	).WithPlan(func(v []any) string { return d.plan(append(upArgs(v), "--dry-run")) })
	return Spec{Form: form, Args: upArgs}
}
