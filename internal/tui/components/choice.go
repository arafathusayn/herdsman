package components

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/tui"
)

// isSpace reports the key that toggles a choice.
func isSpace(key tui.Key) bool { return key.Type == tui.KeyRune && key.Rune == ' ' }

// move steps a cursor through n items and wraps at both ends.
func move(pos, n int, key tui.Key) int {
	if n == 0 {
		return pos
	}
	switch key.Type {
	case tui.KeyUp:
		return (pos + n - 1) % n
	case tui.KeyDown:
		return (pos + 1) % n
	}
	return pos
}

// listLine draws one option. A focused field marks its current option.
func listLine(st tui.Style, focused, current bool, mark, option string) string {
	option = tui.Clean(strings.ReplaceAll(option, "\n", " "))
	switch {
	case current && focused:
		return st.Fg(tui.Cyan, "> "+mark+option)
	case current:
		return "* " + mark + option
	}
	return "  " + mark + option
}

// pickNumber turns a one-based answer into an index below n.
func pickNumber(answer string, n int) (int, error) {
	i, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || i < 1 || i > n {
		return 0, fmt.Errorf("answer with a number from 1 to %d", n)
	}
	return i - 1, nil
}

// sayOptions lists the options by number.
func sayOptions(p *tui.Prompter, options []string) error {
	for i, option := range options {
		if err := p.Say(fmt.Sprintf("  %d. %s", i+1, option)); err != nil {
			return err
		}
	}
	return nil
}

// Select picks one option.
type Select struct {
	label   string
	options []string
	pos     int
}

// NewSelect returns a field with the option at index selected.
func NewSelect(label string, options []string, index int) Select {
	return Select{label: label, options: options, pos: index}
}

// Label names the field.
func (f Select) Label() string { return f.label }

// Value is the chosen option, or "" when there are none.
func (f Select) Value() any {
	if f.pos < 0 || f.pos >= len(f.options) {
		return ""
	}
	return f.options[f.pos]
}

// TakesEnter is false: Enter submits the form.
func (f Select) TakesEnter() bool { return false }

// Update moves the choice with the arrow keys.
func (f Select) Update(msg tui.Msg) Field {
	if key, ok := msg.(tui.Key); ok {
		f.pos = move(f.pos, len(f.options), key)
	}
	return f
}

// View draws one line per option.
func (f Select) View(st tui.Style, focused bool) string {
	lines := make([]string, len(f.options))
	for i, option := range f.options {
		lines[i] = listLine(st, focused, i == f.pos, "", option)
	}
	return strings.Join(lines, "\n")
}

// Ask reads the number of an option. A blank answer keeps the current one, and
// an answer out of range asks again.
func (f Select) Ask(p *tui.Prompter) (Field, error) {
	if err := p.Say(f.label); err != nil {
		return f, err
	}
	if err := sayOptions(p, f.options); err != nil {
		return f, err
	}
	for {
		line, err := p.Line(fmt.Sprintf("Number [%d]", f.pos+1))
		if err != nil {
			return f, err
		}
		if line == "" {
			return f, nil
		}
		i, err := pickNumber(line, len(f.options))
		if err != nil {
			if err := p.Say(err.Error()); err != nil {
				return f, err
			}
			continue
		}
		f.pos = i
		return f, nil
	}
}

// MultiSelect picks any number of options.
type MultiSelect struct {
	label   string
	options []string
	chosen  []bool
	pos     int
}

// NewMultiSelect returns a field with the named options chosen.
func NewMultiSelect(label string, options, chosen []string) MultiSelect {
	marks := make([]bool, len(options))
	for i, option := range options {
		marks[i] = slices.Contains(chosen, option)
	}
	return MultiSelect{label: label, options: options, chosen: marks}
}

// Label names the field.
func (f MultiSelect) Label() string { return f.label }

// Value is the chosen options as a []string, in list order.
func (f MultiSelect) Value() any {
	out := []string{}
	for i, option := range f.options {
		if f.chosen[i] {
			out = append(out, option)
		}
	}
	return out
}

// TakesEnter is false: Enter submits the form.
func (f MultiSelect) TakesEnter() bool { return false }

// Update moves with the arrow keys and toggles with Space.
func (f MultiSelect) Update(msg tui.Msg) Field {
	key, ok := msg.(tui.Key)
	if !ok {
		return f
	}
	if isSpace(key) && f.pos < len(f.chosen) {
		f.chosen = slices.Clone(f.chosen)
		f.chosen[f.pos] = !f.chosen[f.pos]
		return f
	}
	f.pos = move(f.pos, len(f.options), key)
	return f
}

// View draws one line per option, each with its check box.
func (f MultiSelect) View(st tui.Style, focused bool) string {
	lines := make([]string, len(f.options))
	for i, option := range f.options {
		mark := "[ ] "
		if f.chosen[i] {
			mark = "[x] "
		}
		lines[i] = listLine(st, focused, i == f.pos, mark, option)
	}
	return strings.Join(lines, "\n")
}

// Ask reads option numbers separated by spaces or commas. A blank answer keeps
// the current choice, and a number out of range asks again.
func (f MultiSelect) Ask(p *tui.Prompter) (Field, error) {
	if err := p.Say(f.label); err != nil {
		return f, err
	}
	if err := sayOptions(p, f.options); err != nil {
		return f, err
	}
	for {
		line, err := p.Line("Numbers, separated by spaces")
		if err != nil {
			return f, err
		}
		if strings.TrimSpace(line) == "" {
			return f, nil
		}
		marks, err := parsePicks(line, len(f.options))
		if err != nil {
			if err := p.Say(err.Error()); err != nil {
				return f, err
			}
			continue
		}
		f.chosen = marks
		return f, nil
	}
}

// parsePicks turns a list of one-based numbers into marks.
func parsePicks(line string, n int) ([]bool, error) {
	marks := make([]bool, n)
	for _, part := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' }) {
		i, err := pickNumber(part, n)
		if err != nil {
			return nil, err
		}
		marks[i] = true
	}
	return marks, nil
}

// Confirm answers yes or no.
type Confirm struct {
	label string
	yes   bool
}

// NewConfirm returns a field with the given answer.
func NewConfirm(label string, yes bool) Confirm { return Confirm{label: label, yes: yes} }

// Label names the field.
func (f Confirm) Label() string { return f.label }

// Value is the answer as a bool.
func (f Confirm) Value() any { return f.yes }

// TakesEnter is false: Enter submits the form.
func (f Confirm) TakesEnter() bool { return false }

// Update takes y, n, Space and the left and right arrows.
func (f Confirm) Update(msg tui.Msg) Field {
	key, ok := msg.(tui.Key)
	if !ok {
		return f
	}
	switch {
	case key.Type == tui.KeyLeft, key.Type == tui.KeyRune && (key.Rune == 'y' || key.Rune == 'Y'):
		f.yes = true
	case key.Type == tui.KeyRight, key.Type == tui.KeyRune && (key.Rune == 'n' || key.Rune == 'N'):
		f.yes = false
	case isSpace(key):
		f.yes = !f.yes
	}
	return f
}

// View draws both answers with the current one marked.
func (f Confirm) View(st tui.Style, focused bool) string {
	yes, no := "  Yes  ", "[ No ]"
	if f.yes {
		yes, no = "[ Yes ]", "  No "
	}
	if focused {
		if f.yes {
			yes = st.Fg(tui.Cyan, yes)
		} else {
			no = st.Fg(tui.Cyan, no)
		}
	}
	return yes + " " + no
}

// Ask reads y or n. A blank answer keeps the current one, and anything else
// asks again.
func (f Confirm) Ask(p *tui.Prompter) (Field, error) {
	hint := "y/N"
	if f.yes {
		hint = "Y/n"
	}
	for {
		line, err := p.Line(f.label + " [" + hint + "]")
		if err != nil {
			return f, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return f, nil
		case "y", "yes":
			f.yes = true
			return f, nil
		case "n", "no":
			f.yes = false
			return f, nil
		}
		if err := p.Say("answer y or n"); err != nil {
			return f, err
		}
	}
}
