package components

import (
	"fmt"
	"strings"

	"github.com/arafathusayn/herdsman/internal/tui"
)

// PlanViewer shows read-only lines through a window that scrolls.
type PlanViewer struct {
	lines  []string
	top    int
	height int
}

// NewPlanViewer returns a viewer of text, showing height lines at a time.
func NewPlanViewer(text string, height int) PlanViewer {
	return PlanViewer{lines: strings.Split(tui.Clean(text), "\n"), height: max(height, 1)}
}

// SetHeight changes the window and keeps it inside the text.
func (v PlanViewer) SetHeight(height int) PlanViewer {
	v.height = max(height, 1)
	v.top = min(v.top, v.last())
	return v
}

func (v PlanViewer) last() int { return max(len(v.lines)-v.height, 0) }

// Update scrolls with the arrows, the page keys, Home and End.
func (v PlanViewer) Update(key tui.Key) PlanViewer {
	switch key.Type {
	case tui.KeyUp:
		v.top--
	case tui.KeyDown:
		v.top++
	case tui.KeyPageUp:
		v.top -= v.height
	case tui.KeyPageDown:
		v.top += v.height
	case tui.KeyHome:
		v.top = 0
	case tui.KeyEnd:
		v.top = v.last()
	}
	v.top = max(min(v.top, v.last()), 0)
	return v
}

// View draws the window, and the position when the text does not fit.
func (v PlanViewer) View(st tui.Style) string {
	end := min(v.top+v.height, len(v.lines))
	text := strings.Join(v.lines[v.top:end], "\n")
	if len(v.lines) <= v.height {
		return text
	}
	return text + "\n" + st.Dim(fmt.Sprintf("lines %d-%d of %d", v.top+1, end, len(v.lines)))
}

// Entry is one field of a form with its check. A nil Validate accepts anything.
type Entry struct {
	Field    Field
	Validate func(value any) error
}

// check runs the validation and returns its message, or "".
func (e Entry) check() string {
	if e.Validate == nil {
		return ""
	}
	if err := e.Validate(e.Field.Value()); err != nil {
		return err.Error()
	}
	return ""
}

// Outcome is how a form ended.
type Outcome int

// The ways a form can end.
const (
	Editing Outcome = iota
	Submitted
	Canceled
)

// framing is the number of rows a plan screen spends on its title, the viewer's
// position line, the footer and the row the renderer leaves free.
const framing = 5

// Form is a list of fields with validation, and an optional plan shown as the
// last screen. Esc cancels without side effects, Tab and Shift-Tab move, and
// Enter submits.
type Form struct {
	title    string
	style    tui.Style
	entries  []Entry
	errs     []string
	focus    int
	plan     func(values []any) string
	rows     int
	planning bool
	viewer   PlanViewer
	outcome  Outcome
}

// NewForm returns a form over entries.
func NewForm(title string, style tui.Style, entries ...Entry) Form {
	return Form{title: title, style: style, entries: entries, errs: make([]string, len(entries)), rows: 24}
}

// WithPlan adds a last screen, built from the values, that must be confirmed.
func (f Form) WithPlan(plan func(values []any) string) Form {
	f.plan = plan
	return f
}

// Outcome reports how the form ended.
func (f Form) Outcome() Outcome { return f.outcome }

// Values returns each field's value, in order.
func (f Form) Values() []any {
	out := make([]any, len(f.entries))
	for i, entry := range f.entries {
		out[i] = entry.Field.Value()
	}
	return out
}

func (f Form) finish(outcome Outcome) (tui.Model, tui.Cmd) {
	f.outcome = outcome
	return f, tui.Quit
}

// Update handles one message.
func (f Form) Update(msg tui.Msg) (tui.Model, tui.Cmd) {
	if f.outcome != Editing {
		return f, nil
	}
	switch msg := msg.(type) {
	case tui.Resize:
		f.rows = msg.Rows
		if f.planning {
			f.viewer = f.viewer.SetHeight(f.rows - framing)
		}
		return f, nil
	case tui.Paste:
		if !f.planning && len(f.entries) > 0 {
			return f.edit(msg), nil
		}
		return f, nil
	case tui.Key:
		if isCancel(msg) {
			return f.finish(Canceled)
		}
		if f.planning {
			return f.updatePlan(msg)
		}
		return f.updateFields(msg)
	}
	return f, nil
}

func isCancel(key tui.Key) bool {
	return key.Type == tui.KeyEsc || (key.Type == tui.KeyCtrl && key.Rune == 'C')
}

func (f Form) updatePlan(key tui.Key) (tui.Model, tui.Cmd) {
	switch key.Type {
	case tui.KeyEnter:
		return f.finish(Submitted)
	case tui.KeyShiftTab:
		f.planning = false
		return f, nil
	}
	f.viewer = f.viewer.Update(key)
	return f, nil
}

func (f Form) updateFields(key tui.Key) (tui.Model, tui.Cmd) {
	n := len(f.entries)
	switch {
	case key.Type == tui.KeyTab && n > 0:
		f.focus = (f.focus + 1) % n
		return f, nil
	case key.Type == tui.KeyShiftTab && n > 0:
		f.focus = (f.focus + n - 1) % n
		return f, nil
	case key.Type == tui.KeyEnter && (n == 0 || !f.entries[f.focus].Field.TakesEnter()):
		return f.submit()
	}
	if n == 0 {
		return f, nil
	}
	return f.edit(key), nil
}

// edit gives msg to the focused field and validates the result. The entries
// are copied first, so the previous form keeps its own.
func (f Form) edit(msg tui.Msg) Form {
	entries := append([]Entry(nil), f.entries...)
	errs := append([]string(nil), f.errs...)
	entries[f.focus].Field = entries[f.focus].Field.Update(msg)
	errs[f.focus] = entries[f.focus].check()
	f.entries, f.errs = entries, errs
	return f
}

// validated checks every field. It returns the form with their messages and
// the index of the first failure, or -1.
func (f Form) validated() (Form, int) {
	errs := make([]string, len(f.entries))
	first := -1
	for i, entry := range f.entries {
		errs[i] = entry.check()
		if errs[i] != "" && first < 0 {
			first = i
		}
	}
	f.errs = errs
	return f, first
}

func (f Form) submit() (tui.Model, tui.Cmd) {
	f, first := f.validated()
	if first >= 0 {
		f.focus = first
		return f, nil
	}
	if f.plan == nil {
		return f.finish(Submitted)
	}
	f.planning = true
	f.viewer = NewPlanViewer(f.plan(f.Values()), f.rows-framing)
	return f, nil
}

// View draws the current screen.
func (f Form) View() string {
	st := f.style
	var b strings.Builder
	b.WriteString(st.Bold(tui.Clean(f.title)))
	if f.planning {
		b.WriteString("\n\n" + f.viewer.View(st) + "\n")
		b.WriteString(st.Dim("Enter runs  Shift-Tab goes back  Esc cancels"))
		return b.String()
	}
	b.WriteString("\n")
	from, to := 0, 0
	for i, entry := range f.entries {
		focused := i == f.focus
		label := tui.Clean(entry.Field.Label())
		if focused {
			label = st.Fg(tui.Cyan, label)
		}
		b.WriteString("\n")
		if focused {
			from = strings.Count(b.String(), "\n")
		}
		b.WriteString(label + "\n")
		for _, line := range strings.Split(entry.Field.View(st, focused), "\n") {
			b.WriteString("  " + line + "\n")
		}
		if f.errs[i] != "" {
			b.WriteString("  " + st.Fg(tui.Red, "! "+tui.Clean(f.errs[i])) + "\n")
		}
		if focused {
			to = strings.Count(b.String(), "\n")
		}
	}
	b.WriteString("\n" + st.Dim("Enter submits  Tab moves  Esc cancels"))
	return f.window(b.String(), from, to)
}

// window fits a frame to the screen. The footer stays, and the rest scrolls to
// show lines from up to (not including) to, which hold the focused field. A
// field taller than the screen shows its start.
func (f Form) window(frame string, from, to int) string {
	lines := strings.Split(frame, "\n")
	limit := f.rows - 1
	if limit <= 0 || len(lines) <= limit {
		return frame
	}
	room := max(limit-1, 1)
	top := min(max(to-room, 0), from)
	end := min(top+room, len(lines)-1)
	return strings.Join(append(lines[top:end:end], lines[len(lines)-1]), "\n")
}

// RunPlain asks the same form as line prompts. A failed check shows its message
// and asks that field again. It returns the finished form, or tui.ErrCanceled
// when the input ends or the plan is refused.
func (f Form) RunPlain(p *tui.Prompter) (Form, error) {
	entries := append([]Entry(nil), f.entries...)
	f.entries = entries
	if err := p.Say(f.title); err != nil {
		return f, err
	}
	for i := range entries {
		for {
			field, err := entries[i].Field.Ask(p)
			if err != nil {
				return f, err
			}
			entries[i].Field = field
			message := entries[i].check()
			if message == "" {
				break
			}
			if err := p.Say("! " + message); err != nil {
				return f, err
			}
		}
	}
	if f.plan != nil {
		if err := p.Say(f.plan(f.Values())); err != nil {
			return f, err
		}
		confirm, err := NewConfirm("Run this plan?", false).Ask(p)
		if err != nil {
			return f, err
		}
		if confirm.Value() != true {
			f.outcome = Canceled
			return f, tui.ErrCanceled
		}
	}
	f.outcome = Submitted
	return f, nil
}
