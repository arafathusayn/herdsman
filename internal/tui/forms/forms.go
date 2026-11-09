// Package forms holds the form each command opens when it is run on a terminal
// with no arguments. A form only gathers answers: it turns them into the same
// arguments a script would pass, and the command itself does the work.
package forms

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/tui"
	"github.com/arafathusayn/herdsman/internal/tui/components"
)

// Common is what every form needs from its caller.
type Common struct {
	Style tui.Style
	// Plan returns the text of the last screen for a command line, read from
	// the command itself. Nil shows the command line.
	Plan func(argv []string) string
	// Consent returns the arguments that tell the command the shown plan was
	// approved. Nil, or no arguments, leaves the command to ask for itself.
	Consent func() []string
}

func (c Common) consent() []string {
	if c.Consent == nil {
		return nil
	}
	return c.Consent()
}

func (c Common) plan(argv []string) string {
	if c.Plan != nil {
		return c.Plan(argv)
	}
	return "Runs:\n  " + display(argv)
}

// display writes a command line for reading, quoting what needs it.
func display(argv []string) string {
	parts := []string{"herdsman"}
	for _, arg := range argv {
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"\\$") {
			arg = strconv.Quote(arg)
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

// Spec is one command's form and the way from its answers to a command line.
type Spec struct {
	Form components.Form
	// Args returns the command line, starting with the verb, for the values
	// the form ended with.
	Args func(values []any) []string
}

// Open shows the form on a terminal, or as line prompts when plain is set. It
// returns the command line to run, and false when the person canceled.
func Open(ctx context.Context, spec Spec, in, out *os.File, plain bool) ([]string, bool, error) {
	if plain {
		form, err := spec.Form.RunPlain(tui.NewPrompter(in, out))
		if errors.Is(err, tui.ErrCanceled) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return spec.Args(form.Values()), true, nil
	}
	changes, stop := tui.WatchResize()
	defer stop()
	final, err := tui.Run(ctx, spec.Form, tui.Options{In: tui.NewInput(in), Out: out, Term: tui.NewTerminal(in), Resize: changes})
	if err != nil {
		return nil, false, err
	}
	form := final.(components.Form)
	if form.Outcome() != components.Submitted {
		return nil, false, nil
	}
	return spec.Args(form.Values()), true, nil
}

// text reads a field's value as a trimmed string.
func text(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// on reads a field's value as a yes or no.
func on(v any) bool {
	b, _ := v.(bool)
	return b
}

// commaList splits a comma-separated answer and drops the blanks.
func commaList(v any) []string {
	var out []string
	for _, part := range strings.Split(text(v), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// required refuses a blank answer with the given message.
func required(message string) func(any) error {
	return func(v any) error {
		if text(v) == "" {
			return errors.New(message)
		}
		return nil
	}
}

// seconds accepts a blank answer or a whole number of seconds.
func seconds(v any) error {
	s := text(v)
	if s == "" {
		return nil
	}
	if n, err := strconv.Atoi(s); err != nil || n < 0 {
		return errors.New("a number of seconds")
	}
	return nil
}

// commandField is a line of free text whose Up and Down keys fill in the
// choices that were found on this machine.
type commandField struct {
	label   string
	input   components.Field
	choices []string
	pos     int
}

func newCommandField(label string, choices []string, index int) commandField {
	value := ""
	if index >= 0 && index < len(choices) {
		value = choices[index]
	}
	return commandField{label: label, input: components.NewTextInput(label, value), choices: choices, pos: index}
}

// Label names the field.
func (f commandField) Label() string { return f.label }

// Value is the text.
func (f commandField) Value() any { return f.input.Value() }

// TakesEnter is false: Enter submits the form.
func (f commandField) TakesEnter() bool { return false }

// Update cycles the choices with the arrows and otherwise edits the text.
func (f commandField) Update(msg tui.Msg) components.Field {
	key, isKey := msg.(tui.Key)
	if n := len(f.choices); isKey && n > 0 && (key.Type == tui.KeyUp || key.Type == tui.KeyDown) {
		step := 1
		if key.Type == tui.KeyUp {
			step = n - 1
		}
		f.pos = (f.pos + step) % n
		f.input = components.NewTextInput(f.label, f.choices[f.pos])
		return f
	}
	f.input = f.input.Update(msg)
	return f
}

func (f commandField) found() string { return "Found: " + strings.Join(f.choices, ", ") }

// View draws the line and, when focused, what the arrows offer.
func (f commandField) View(st tui.Style, focused bool) string {
	view := f.input.View(st, focused)
	if focused && len(f.choices) > 0 {
		view += "\n" + st.Dim(tui.Clean(f.found())+" (Up and Down)")
	}
	return view
}

// Ask lists the choices and reads one line.
func (f commandField) Ask(p *tui.Prompter) (components.Field, error) {
	if len(f.choices) > 0 {
		if err := p.Say(f.found()); err != nil {
			return f, err
		}
	}
	input, err := f.input.Ask(p)
	if err != nil {
		return f, err
	}
	f.input = input
	return f, nil
}

// TargetDefaults prefills a form that acts on a task's worktree.
type TargetDefaults struct {
	Common
	// Targets are the tasks that can be chosen.
	Targets []string
}

// targetField offers the tasks and then the given selector flags.
func targetField(label string, targets []string, flags ...string) components.Entry {
	options := append(append([]string{}, targets...), flags...)
	return components.Entry{Field: components.NewSelect(label, options, 0)}
}

func appendGrace(args []string, v any) []string {
	if s := text(v); s != "" {
		return append(args, "--grace", s)
	}
	return args
}
