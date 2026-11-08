package components

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/tui"
)

var update = flag.Bool("update", false, "rewrite the golden frames")

var (
	color = tui.Style{Color: true}
	plain = tui.Style{}
)

func key(t tui.KeyType) tui.Key { return tui.Key{Type: t} }

func char(r rune) tui.Key { return tui.Key{Type: tui.KeyRune, Rune: r} }

func typeText(f Field, s string) Field {
	for _, r := range s {
		f = f.Update(char(r))
	}
	return f
}

// golden compares a frame with its file. Escape is shown as a visible mark so
// that the files read cleanly in a diff.
func golden(t *testing.T, name, frame string) {
	t.Helper()
	path := filepath.Join("..", "testdata", "frames", name+".golden")
	shown := strings.ReplaceAll(frame, "\x1b", "␛") + "\n"
	if *update {
		if err := os.WriteFile(path, []byte(shown), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if shown != string(want) {
		t.Errorf("frame %s differs\ngot:\n%s\nwant:\n%s", name, shown, want)
	}
}

func TestGoldenFrames(t *testing.T) {
	area := typeText(NewTextArea("Prompt", ""), "one").Update(key(tui.KeyEnter))
	area = typeText(area, "two")
	multi := NewMultiSelect("Tasks", []string{"alpha", "beta", "gamma"}, []string{"beta"})
	long := strings.TrimRight(strings.Repeat("step\n", 8), "\n")

	frames := map[string]string{
		"text-input-focused":   NewTextInput("Name", "route").View(color, true),
		"text-input-blurred":   NewTextInput("Name", "route").View(color, false),
		"text-input-mid":       NewTextInput("Name", "route").Update(key(tui.KeyLeft)).View(color, true),
		"text-area-focused":    area.View(color, true),
		"select-focused":       NewSelect("Harness", []string{"claude", "codex"}, 1).View(color, true),
		"select-focused-plain": NewSelect("Harness", []string{"claude", "codex"}, 1).View(plain, true),
		"select-blurred":       NewSelect("Harness", []string{"claude", "codex"}, 0).View(color, false),
		"multi-select-focused": multi.View(color, true),
		"multi-select-blurred": multi.View(color, false),
		"confirm-yes-focused":  NewConfirm("Web", true).View(color, true),
		"confirm-no-focused":   NewConfirm("Web", false).View(color, true),
		"confirm-no-plain":     NewConfirm("Web", false).View(plain, false),
		"plan-fits":            NewPlanViewer("first\nsecond", 5).View(color),
		"plan-scrolled":        NewPlanViewer(long, 3).Update(key(tui.KeyDown)).View(color),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) { golden(t, name, frame) })
	}
}

func required(v any) error {
	if v == "" {
		return errors.New("required")
	}
	return nil
}

func sampleForm() Form {
	return NewForm("New route", color,
		Entry{Field: NewTextInput("Name", ""), Validate: required},
		Entry{Field: NewSelect("Harness", []string{"claude", "codex"}, 0)},
		Entry{Field: NewConfirm("Web", false)},
	)
}

func send(t *testing.T, f Form, msgs ...tui.Msg) (Form, tui.Cmd) {
	t.Helper()
	var cmd tui.Cmd
	for _, msg := range msgs {
		var m tui.Model
		m, cmd = f.Update(msg)
		f = m.(Form)
	}
	return f, cmd
}

func isQuit(cmd tui.Cmd) bool {
	return cmd != nil && reflect.DeepEqual(cmd(), tui.Quit())
}

func TestFormFrames(t *testing.T) {
	f := sampleForm()
	golden(t, "form-start", f.View())

	invalid, _ := send(t, f, key(tui.KeyEnter))
	golden(t, "form-invalid", invalid.View())

	ready, _ := send(t, f, char('x'), key(tui.KeyTab))
	golden(t, "form-second-field", ready.View())

	planned := sampleForm().WithPlan(func(v []any) string { return "create " + v[0].(string) })
	planned, _ = send(t, planned, char('x'), key(tui.KeyEnter))
	golden(t, "form-plan", planned.View())
}

func TestFormKeepsTheFocusedFieldOnAShortScreen(t *testing.T) {
	f, _ := send(t, sampleForm(), tui.Resize{Cols: 80, Rows: 8})
	golden(t, "form-short-first", f.View())
	f, _ = send(t, f, key(tui.KeyShiftTab))
	golden(t, "form-short-last", f.View())
	if got := strings.Count(f.View(), "\n") + 1; got > 7 {
		t.Errorf("%d lines do not fit 8 rows", got)
	}
	tall := NewForm("T", plain, Entry{Field: NewTextArea("A", strings.Repeat("x\n", 20))})
	tall, _ = send(t, tall, tui.Resize{Cols: 80, Rows: 6})
	if !strings.Contains(tall.View(), "A\n") {
		t.Errorf("a field taller than the screen lost its label: %q", tall.View())
	}
}

func TestFormEditIsValidatedAsTyped(t *testing.T) {
	f, _ := send(t, sampleForm(), char('x'), key(tui.KeyBackspace))
	if !strings.Contains(f.View(), "! required") {
		t.Error("the message is missing after the field was emptied")
	}
	f, _ = send(t, f, char('y'))
	if strings.Contains(f.View(), "! required") {
		t.Error("the message stayed after the field became valid")
	}
}

func TestFormKeepsEarlierValuesUnchanged(t *testing.T) {
	before, _ := send(t, sampleForm(), char('a'))
	after, _ := send(t, before, char('b'))
	if before.Values()[0] != "a" || after.Values()[0] != "ab" {
		t.Errorf("before %v after %v", before.Values(), after.Values())
	}
}

func TestFormSubmit(t *testing.T) {
	f, cmd := send(t, sampleForm(), char('x'), key(tui.KeyEnter))
	if f.Outcome() != Submitted || !isQuit(cmd) {
		t.Errorf("outcome %v", f.Outcome())
	}
	if got := f.Values(); !reflect.DeepEqual(got, []any{"x", "claude", false}) {
		t.Errorf("values %#v", got)
	}
	again, cmd := send(t, f, char('z'))
	if cmd != nil || again.Values()[0] != "x" {
		t.Error("a finished form took input")
	}
}

func TestFormInvalidSubmitFocusesTheFirstFailure(t *testing.T) {
	f, cmd := send(t, sampleForm(), key(tui.KeyTab), key(tui.KeyEnter))
	if cmd != nil || f.Outcome() != Editing || f.focus != 0 {
		t.Errorf("focus %d outcome %v", f.focus, f.Outcome())
	}
}

func TestFormCancel(t *testing.T) {
	for _, k := range []tui.Key{key(tui.KeyEsc), {Type: tui.KeyCtrl, Rune: 'C', Ctrl: true}} {
		f, cmd := send(t, sampleForm(), k)
		if f.Outcome() != Canceled || !isQuit(cmd) {
			t.Errorf("%v: outcome %v", k, f.Outcome())
		}
	}
}

func TestFormFocusWraps(t *testing.T) {
	f, _ := send(t, sampleForm(), key(tui.KeyShiftTab))
	if f.focus != 2 {
		t.Errorf("back from the first: %d", f.focus)
	}
	f, _ = send(t, f, key(tui.KeyTab))
	if f.focus != 0 {
		t.Errorf("forward from the last: %d", f.focus)
	}
}

func TestFormEnterGoesToATextArea(t *testing.T) {
	f := NewForm("T", plain, Entry{Field: NewTextArea("Prompt", "a")})
	f, cmd := send(t, f, key(tui.KeyEnter))
	if cmd != nil || f.Values()[0] != "a\n" {
		t.Errorf("value %q", f.Values()[0])
	}
}

func TestFormPaste(t *testing.T) {
	f, _ := send(t, sampleForm(), tui.Paste{Text: "a\nb\x1b[2J"})
	if f.Values()[0] != "a b[2J" {
		t.Errorf("value %q", f.Values()[0])
	}
}

func TestFormIgnoresOtherMessages(t *testing.T) {
	f, cmd := send(t, sampleForm(), "something else")
	if cmd != nil || f.Outcome() != Editing {
		t.Error("an unrelated message changed the form")
	}
}

func TestEmptyForm(t *testing.T) {
	f := NewForm("T", plain)
	f, _ = send(t, f, key(tui.KeyTab), key(tui.KeyShiftTab), char('x'), tui.Paste{Text: "x"})
	if f.Outcome() != Editing {
		t.Error("an empty form ended early")
	}
	f, cmd := send(t, f, key(tui.KeyEnter))
	if f.Outcome() != Submitted || !isQuit(cmd) {
		t.Error("an empty form did not submit")
	}
}

func TestFormPlanScreen(t *testing.T) {
	base := NewForm("T", plain, Entry{Field: NewTextInput("A", "v")}).
		WithPlan(func(v []any) string { return strings.Repeat("line\n", 40) + v[0].(string) })
	f, cmd := send(t, base, tui.Resize{Cols: 80, Rows: 10}, key(tui.KeyEnter))
	if cmd != nil || !f.planning || f.viewer.height != 5 {
		t.Fatalf("planning %v height %d", f.planning, f.viewer.height)
	}
	f, _ = send(t, f, tui.Paste{Text: "ignored"}, key(tui.KeyDown), tui.Resize{Cols: 80, Rows: 12})
	if f.viewer.top != 1 || f.viewer.height != 7 || f.Values()[0] != "v" {
		t.Errorf("top %d height %d value %v", f.viewer.top, f.viewer.height, f.Values()[0])
	}
	back, _ := send(t, f, key(tui.KeyShiftTab))
	if back.planning {
		t.Error("Shift-Tab did not return to the fields")
	}
	done, cmd := send(t, f, key(tui.KeyEnter))
	if done.Outcome() != Submitted || !isQuit(cmd) {
		t.Error("Enter did not run the plan")
	}
	stop, _ := send(t, f, key(tui.KeyEsc))
	if stop.Outcome() != Canceled {
		t.Error("Esc did not cancel the plan")
	}
}

func TestTextEditing(t *testing.T) {
	var f Field = NewTextInput("A", "ac\nd")
	if f.Value() != "ac d" {
		t.Errorf("%q", f.Value())
	}
	if f.Label() != "A" || f.TakesEnter() {
		t.Error("label or enter")
	}
	f = NewTextInput("A", "ac")
	f = f.Update(key(tui.KeyLeft)).Update(char('b'))
	if f.Value() != "abc" {
		t.Errorf("insert: %q", f.Value())
	}
	f = f.Update(key(tui.KeyHome)).Update(key(tui.KeyBackspace)).Update(key(tui.KeyDelete))
	if f.Value() != "bc" {
		t.Errorf("delete: %q", f.Value())
	}
	f = f.Update(key(tui.KeyEnd)).Update(key(tui.KeyDelete)).Update(key(tui.KeyRight)).Update(key(tui.KeyBackspace))
	if f.Value() != "b" {
		t.Errorf("end: %q", f.Value())
	}
	f = f.Update(tui.Key{Type: tui.KeyRune, Rune: 'z', Alt: true}).Update(key(tui.KeyInsert)).Update("other")
	if f.Value() != "b" {
		t.Errorf("ignored keys changed the text: %q", f.Value())
	}
	f = f.Update(key(tui.KeyLeft)).Update(key(tui.KeyLeft))
	if f.View(plain, true) != "\x1b[7mb\x1b[0m" {
		t.Errorf("cursor: %q", f.View(plain, true))
	}
}

func TestTextAreaMovement(t *testing.T) {
	var f Field = NewTextArea("A", "long line\nab\nlonger")
	if !f.TakesEnter() || f.Label() != "A" {
		t.Error("label or enter")
	}
	cursor := func(f Field) int { return f.(TextArea).buf.pos }
	f = f.Update(key(tui.KeyDown))
	if cursor(f) != 19 {
		t.Errorf("down on the last line moved to %d", cursor(f))
	}
	f = f.Update(key(tui.KeyUp))
	if cursor(f) != 12 {
		t.Errorf("up clamps to the shorter line: %d", cursor(f))
	}
	f = f.Update(key(tui.KeyUp)).Update(key(tui.KeyUp))
	if cursor(f) != 2 {
		t.Errorf("up on the first line stays: %d", cursor(f))
	}
	f = f.Update(key(tui.KeyDown))
	if cursor(f) != 12 {
		t.Errorf("down: %d", cursor(f))
	}
	f = f.Update(key(tui.KeyHome)).Update(tui.Paste{Text: "x\ty\nz"})
	if !strings.Contains(f.Value().(string), "x y\nzab") {
		t.Errorf("%q", f.Value())
	}
	onNewline := NewTextArea("A", "a\nb").Update(key(tui.KeyUp))
	if got := onNewline.View(plain, true); got != "a\x1b[7m \x1b[0m\nb" {
		t.Errorf("cursor at a line end: %q", got)
	}
	if onNewline.Update("other").Value() != "a\nb" {
		t.Error("an unrelated message changed the text")
	}
	if got := NewTextArea("A", "a").View(plain, false); got != "a" {
		t.Errorf("%q", got)
	}
}

func TestSelect(t *testing.T) {
	var f Field = NewSelect("S", []string{"a", "b"}, 0)
	if f.Label() != "S" || f.TakesEnter() {
		t.Error("label or enter")
	}
	f = f.Update(key(tui.KeyUp))
	if f.Value() != "b" {
		t.Errorf("up wraps: %v", f.Value())
	}
	f = f.Update(key(tui.KeyDown)).Update(key(tui.KeyLeft)).Update("other")
	if f.Value() != "a" {
		t.Errorf("down wraps: %v", f.Value())
	}
	if NewSelect("S", nil, 0).Update(key(tui.KeyDown)).Value() != "" {
		t.Error("an empty select has a value")
	}
	if NewSelect("S", []string{"a"}, -1).Value() != "" {
		t.Error("a negative index has a value")
	}
}

func TestMultiSelect(t *testing.T) {
	var f Field = NewMultiSelect("M", []string{"a", "b"}, []string{"b"})
	if f.Label() != "M" || f.TakesEnter() {
		t.Error("label or enter")
	}
	before := f
	f = f.Update(char(' ')).Update(key(tui.KeyDown)).Update(char(' ')).Update("other")
	if got := f.Value().([]string); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("got %v", got)
	}
	if got := before.Value().([]string); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("the earlier value changed: %v", got)
	}
	if got := NewMultiSelect("M", nil, nil).Update(char(' ')).Value().([]string); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestConfirm(t *testing.T) {
	var f Field = NewConfirm("C", false)
	if f.Label() != "C" || f.TakesEnter() {
		t.Error("label or enter")
	}
	steps := []struct {
		msg  tui.Msg
		want bool
	}{
		{char('y'), true}, {char('n'), false}, {char('Y'), true}, {char('N'), false},
		{key(tui.KeyLeft), true}, {key(tui.KeyRight), false}, {char(' '), true},
		{char('q'), true}, {"other", true},
	}
	for _, step := range steps {
		f = f.Update(step.msg)
		if f.Value() != step.want {
			t.Errorf("after %v: %v", step.msg, f.Value())
		}
	}
}

func TestPlanViewerScrolling(t *testing.T) {
	v := NewPlanViewer("a\nb\nc\nd\ne\nf", 2)
	steps := []struct {
		k    tui.KeyType
		want int
	}{
		{tui.KeyUp, 0}, {tui.KeyDown, 1}, {tui.KeyPageDown, 3}, {tui.KeyPageDown, 4},
		{tui.KeyPageUp, 2}, {tui.KeyHome, 0}, {tui.KeyEnd, 4}, {tui.KeyEnter, 4},
	}
	for _, s := range steps {
		v = v.Update(key(s.k))
		if v.top != s.want {
			t.Errorf("key %v: top %d, want %d", s.k, v.top, s.want)
		}
	}
	v = v.SetHeight(5)
	if v.top != 1 {
		t.Errorf("window left the text: %d", v.top)
	}
	if v.SetHeight(0).height != 1 || NewPlanViewer("a", -1).height != 1 {
		t.Error("a window needs one line")
	}
}

func TestViewsCleanData(t *testing.T) {
	bad := "x\x1b[2J"
	frames := []string{
		NewTextInput("L", bad).View(plain, false),
		NewTextArea("L", bad).View(plain, false),
		NewSelect("L", []string{bad}, 0).View(plain, false),
		NewMultiSelect("L", []string{bad}, nil).View(plain, false),
		NewPlanViewer(bad, 3).View(plain),
		NewForm(bad, plain, Entry{Field: NewTextInput(bad, "")}).View(),
	}
	for _, frame := range frames {
		if strings.Contains(frame, "\x1b[2J") {
			t.Errorf("a control sequence came through: %q", frame)
		}
	}
	f := NewForm("T", plain, Entry{Field: NewTextInput("A", ""), Validate: func(any) error { return errors.New(bad) }})
	f, _ = send(t, f, char('x'))
	if strings.Contains(f.View(), "\x1b[2J") {
		t.Error("a validation message came through raw")
	}
	option := NewSelect("L", []string{"a\nb"}, 0).View(plain, false)
	if strings.Contains(option, "\n") {
		t.Errorf("an option broke the layout: %q", option)
	}
}

func prompter(input string) (*tui.Prompter, *strings.Builder) {
	var out strings.Builder
	return tui.NewPrompter(strings.NewReader(input), &out), &out
}

func TestPlainForm(t *testing.T) {
	form := NewForm("New route", plain,
		Entry{Field: NewTextInput("Name", ""), Validate: required},
		Entry{Field: NewTextArea("Prompt", "")},
		Entry{Field: NewSelect("Harness", []string{"claude", "codex"}, 0)},
		Entry{Field: NewMultiSelect("Tasks", []string{"a", "b", "c"}, nil)},
		Entry{Field: NewConfirm("Web", false)},
	).WithPlan(func(v []any) string { return "create " + v[0].(string) })

	p, out := prompter("\nroute\none\ntwo\n.\n9\nx\n2\n1 9\n1,3\nmaybe\ny\nyes\n")
	got, err := form.RunPlain(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"route", "one\ntwo", "codex", []string{"a", "c"}, true}
	if !reflect.DeepEqual(got.Values(), want) {
		t.Errorf("values %#v", got.Values())
	}
	if got.Outcome() != Submitted {
		t.Errorf("outcome %v", got.Outcome())
	}
	for _, text := range []string{"New route", "! required", "1. claude", "number from 1 to 2", "answer y or n", "create route"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q in %q", text, out.String())
		}
	}
	if form.Values()[0] != "" {
		t.Error("the original form changed")
	}
}

func TestPlainFormKeepsDefaultsOnBlankAnswers(t *testing.T) {
	form := NewForm("T", plain,
		Entry{Field: NewTextInput("Name", "kept")},
		Entry{Field: NewTextArea("Prompt", "text")},
		Entry{Field: NewSelect("S", []string{"a", "b"}, 1)},
		Entry{Field: NewMultiSelect("M", []string{"a", "b"}, []string{"b"})},
		Entry{Field: NewConfirm("C", true)},
	)
	p, out := prompter("\n.\n\n\n\n")
	got, err := form.RunPlain(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"kept", "text", "b", []string{"b"}, true}
	if !reflect.DeepEqual(got.Values(), want) {
		t.Errorf("values %#v", got.Values())
	}
	for _, text := range []string{"Name [kept]", "Number [2]", "C [Y/n]"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q in %q", text, out.String())
		}
	}
}

func TestPlainConfirmNo(t *testing.T) {
	p, _ := prompter("no\n")
	f, err := NewConfirm("C", true).Ask(p)
	if err != nil || f.Value() != false {
		t.Errorf("%v %v", f.Value(), err)
	}
}

func TestPlainPlanRefused(t *testing.T) {
	form := NewForm("T", plain).WithPlan(func([]any) string { return "plan" })
	p, _ := prompter("n\n")
	got, err := form.RunPlain(p)
	if !errors.Is(err, tui.ErrCanceled) || got.Outcome() != Canceled {
		t.Errorf("%v %v", got.Outcome(), err)
	}
}

// failAfter fails every write once its allowance is used.
type failAfter struct{ left int }

func (w *failAfter) Write(p []byte) (int, error) {
	if w.left <= 0 {
		return 0, errors.New("write failed")
	}
	w.left--
	return len(p), nil
}

func TestPlainEndOfInputCancels(t *testing.T) {
	fields := []Field{
		NewTextInput("A", ""), NewTextArea("A", ""),
		NewSelect("A", []string{"a"}, 0), NewMultiSelect("A", []string{"a"}, nil), NewConfirm("A", false),
	}
	for _, field := range fields {
		p, _ := prompter("")
		if _, err := field.Ask(p); !errors.Is(err, tui.ErrCanceled) {
			t.Errorf("%T: %v", field, err)
		}
		form := NewForm("T", plain, Entry{Field: field})
		p, _ = prompter("")
		if _, err := form.RunPlain(p); !errors.Is(err, tui.ErrCanceled) {
			t.Errorf("%T in a form: %v", field, err)
		}
	}
	form := NewForm("T", plain).WithPlan(func([]any) string { return "plan" })
	p, _ := prompter("")
	if _, err := form.RunPlain(p); !errors.Is(err, tui.ErrCanceled) {
		t.Errorf("at the plan: %v", err)
	}
}

// TestPlainWriteErrors lets each write through in turn, so every error return
// in the prompts runs once.
func TestPlainWriteErrors(t *testing.T) {
	cases := []struct {
		name  string
		field Field
		input string
		max   int
	}{
		{"select", NewSelect("S", []string{"a", "b"}, 0), "9\n1\n", 5},
		{"multi", NewMultiSelect("M", []string{"a", "b"}, nil), "9\n1\n", 5},
		{"confirm", NewConfirm("C", false), "x\ny\n", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			failures := 0
			for left := range c.max {
				p := tui.NewPrompter(strings.NewReader(c.input), &failAfter{left: left})
				if _, err := c.field.Ask(p); err != nil {
					failures++
				}
			}
			if failures == 0 {
				t.Error("no write failure surfaced")
			}
		})
	}
	bad := func(any) error { return errors.New("no") }
	checked := NewForm("T", plain, Entry{Field: NewTextInput("A", ""), Validate: bad})
	planned := NewForm("T", plain).WithPlan(func([]any) string { return "plan" })
	for _, form := range []Form{checked, planned} {
		for left := range 3 {
			p := tui.NewPrompter(strings.NewReader("x\n"), &failAfter{left: left})
			if _, err := form.RunPlain(p); err == nil {
				t.Errorf("write %d: no error", left)
			}
		}
	}
	p := tui.NewPrompter(strings.NewReader(""), io.Discard)
	if _, err := NewTextInput("A", "").Ask(p); !errors.Is(err, tui.ErrCanceled) {
		t.Errorf("%v", err)
	}
}
