// Package components holds the values a form is built from: text fields,
// choices, a plan viewer, and the form that joins them.
package components

import (
	"strings"

	"github.com/arafathusayn/herdsman/internal/tui"
)

// Field is one input of a form. Fields are values: Update returns the changed
// copy and leaves the receiver as it was.
type Field interface {
	// Label names the field.
	Label() string
	// Update applies a key or a paste.
	Update(tui.Msg) Field
	// View draws the field, with its cursor when focused.
	View(st tui.Style, focused bool) string
	// Value is the current answer: a string, a []string or a bool.
	Value() any
	// TakesEnter reports whether Enter edits this field instead of submitting.
	TakesEnter() bool
	// Ask gets the answer through line prompts. The returned Field holds it.
	Ask(p *tui.Prompter) (Field, error)
}

// buffer is editable text and a cursor. Every edit builds a new slice, so a
// copied buffer never shares changes with the original.
type buffer struct {
	text []rune
	pos  int
}

func newBuffer(s string) buffer {
	text := []rune(tui.Clean(s))
	return buffer{text: text, pos: len(text)}
}

func (b buffer) insert(s string) buffer {
	add := []rune(s)
	text := make([]rune, 0, len(b.text)+len(add))
	text = append(append(append(text, b.text[:b.pos]...), add...), b.text[b.pos:]...)
	return buffer{text: text, pos: b.pos + len(add)}
}

func (b buffer) remove(from, to int) buffer {
	text := make([]rune, 0, len(b.text)-(to-from))
	text = append(append(text, b.text[:from]...), b.text[to:]...)
	return buffer{text: text, pos: from}
}

// lineStart and lineEnd bound the line that holds position p.
func (b buffer) lineStart(p int) int {
	for p > 0 && b.text[p-1] != '\n' {
		p--
	}
	return p
}

func (b buffer) lineEnd(p int) int {
	for p < len(b.text) && b.text[p] != '\n' {
		p++
	}
	return p
}

// vertical moves the cursor to the line above or below, at the same column or
// the end of a shorter line.
func (b buffer) vertical(down bool) buffer {
	start := b.lineStart(b.pos)
	col := b.pos - start
	if down {
		end := b.lineEnd(b.pos)
		if end == len(b.text) {
			return b
		}
		start = end + 1
	} else {
		if start == 0 {
			return b
		}
		start = b.lineStart(start - 1)
	}
	b.pos = min(start+col, b.lineEnd(start))
	return b
}

// edit applies the keys every text field shares. The result is false for a key
// that is not an edit.
func (b buffer) edit(key tui.Key) (buffer, bool) {
	switch key.Type {
	case tui.KeyRune:
		if key.Alt {
			return b, false
		}
		return b.insert(string(key.Rune)), true
	case tui.KeyBackspace:
		if b.pos > 0 {
			return b.remove(b.pos-1, b.pos), true
		}
	case tui.KeyDelete:
		if b.pos < len(b.text) {
			return b.remove(b.pos, b.pos+1), true
		}
	case tui.KeyLeft:
		b.pos = max(b.pos-1, 0)
	case tui.KeyRight:
		b.pos = min(b.pos+1, len(b.text))
	case tui.KeyHome:
		b.pos = b.lineStart(b.pos)
	case tui.KeyEnd:
		b.pos = b.lineEnd(b.pos)
	default:
		return b, false
	}
	return b, true
}

// render draws the text, with the cell under the cursor reversed when focused.
func (b buffer) render(st tui.Style, focused bool) string {
	if !focused {
		return string(b.text)
	}
	cell := " "
	rest := b.text[b.pos:]
	if len(rest) > 0 && rest[0] != '\n' {
		cell, rest = string(rest[0]), rest[1:]
	}
	return string(b.text[:b.pos]) + st.Reverse(cell) + string(rest)
}

// TextInput is a single line of text.
type TextInput struct {
	label string
	buf   buffer
}

// NewTextInput returns a field prefilled with value.
func NewTextInput(label, value string) TextInput {
	return TextInput{label: label, buf: newBuffer(strings.ReplaceAll(value, "\n", " "))}
}

// Label names the field.
func (f TextInput) Label() string { return f.label }

// Value is the text.
func (f TextInput) Value() any { return string(f.buf.text) }

// TakesEnter is false: Enter submits the form.
func (f TextInput) TakesEnter() bool { return false }

// Update applies a key or a paste. Pasted newlines become spaces.
func (f TextInput) Update(msg tui.Msg) Field {
	switch msg := msg.(type) {
	case tui.Key:
		f.buf, _ = f.buf.edit(msg)
	case tui.Paste:
		f.buf = f.buf.insert(strings.ReplaceAll(tui.Clean(msg.Text), "\n", " "))
	}
	return f
}

// View draws the line.
func (f TextInput) View(st tui.Style, focused bool) string { return f.buf.render(st, focused) }

// Ask reads one line. A blank answer keeps the prefilled value.
func (f TextInput) Ask(p *tui.Prompter) (Field, error) {
	prompt := f.label
	if len(f.buf.text) > 0 {
		prompt += " [" + string(f.buf.text) + "]"
	}
	line, err := p.Line(prompt)
	if err != nil {
		return f, err
	}
	if line != "" {
		f.buf = newBuffer(strings.ReplaceAll(line, "\n", " "))
	}
	return f, nil
}

// TextArea is several lines of text. Enter starts a new line, and Tab leaves
// the field.
type TextArea struct {
	label string
	buf   buffer
}

// NewTextArea returns a field prefilled with value.
func NewTextArea(label, value string) TextArea {
	return TextArea{label: label, buf: newBuffer(value)}
}

// Label names the field.
func (f TextArea) Label() string { return f.label }

// Value is the text, with lines joined by newlines.
func (f TextArea) Value() any { return string(f.buf.text) }

// TakesEnter is true: Enter starts a new line.
func (f TextArea) TakesEnter() bool { return true }

// Update applies a key or a paste.
func (f TextArea) Update(msg tui.Msg) Field {
	switch msg := msg.(type) {
	case tui.Key:
		switch msg.Type {
		case tui.KeyEnter:
			f.buf = f.buf.insert("\n")
		case tui.KeyUp:
			f.buf = f.buf.vertical(false)
		case tui.KeyDown:
			f.buf = f.buf.vertical(true)
		default:
			f.buf, _ = f.buf.edit(msg)
		}
	case tui.Paste:
		f.buf = f.buf.insert(tui.Clean(msg.Text))
	}
	return f
}

// View draws the lines.
func (f TextArea) View(st tui.Style, focused bool) string { return f.buf.render(st, focused) }

// Ask reads a block of lines. An empty block keeps the prefilled value.
func (f TextArea) Ask(p *tui.Prompter) (Field, error) {
	text, err := p.Block(f.label)
	if err != nil {
		return f, err
	}
	if text != "" {
		f.buf = newBuffer(text)
	}
	return f, nil
}
