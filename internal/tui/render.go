package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Color is one of the 16 ANSI colors.
type Color int

// The colors the components use.
const (
	Red    Color = 1
	Green  Color = 2
	Yellow Color = 3
	Cyan   Color = 6
)

// Style applies terminal attributes. With Color off, text keeps its weight and
// reverse video but gets no color, which is what NO_COLOR asks for.
type Style struct {
	Color bool
}

// NewStyle reads the environment through getenv. A non-empty NO_COLOR or a dumb
// terminal turns color off.
func NewStyle(getenv func(string) string) Style {
	return Style{Color: getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"}
}

func sgr(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }

// Fg paints s in a color.
func (st Style) Fg(c Color, s string) string {
	if !st.Color {
		return s
	}
	return sgr(strconv.Itoa(30+int(c)), s)
}

// Bold makes s bold.
func (st Style) Bold(s string) string { return sgr("1", s) }

// Dim makes s faint.
func (st Style) Dim(s string) string { return sgr("2", s) }

// Reverse swaps the foreground and background of s.
func (st Style) Reverse(s string) string { return sgr("7", s) }

// Clean removes control characters, so that a value from a config file, a task
// title or a paste cannot move the cursor or restyle the terminal. Newlines
// stay; a tab becomes a space.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// runeWidth is the number of cells a character takes.
func runeWidth(r rune) int {
	switch {
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

// sequenceLen is the length of the style sequence at the start of s, or 0.
func sequenceLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1
		}
	}
	return len(s)
}

// Width is the number of cells s takes, not counting style sequences.
func Width(s string) int {
	total := 0
	for i := 0; i < len(s); {
		if n := sequenceLen(s[i:]); n > 0 {
			i += n
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		total += runeWidth(r)
		i += n
	}
	return total
}

// Truncate cuts s to at most cols cells. Style sequences do not count, and a
// cut inside styled text resets the style.
func Truncate(s string, cols int) string {
	if Width(s) <= cols {
		return s
	}
	var b strings.Builder
	used := 0
	styled := false
	for i := 0; i < len(s); {
		if n := sequenceLen(s[i:]); n > 0 {
			seq := s[i : i+n]
			b.WriteString(seq)
			styled = seq != "\x1b[0m"
			i += n
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if used+runeWidth(r) > cols {
			break
		}
		used += runeWidth(r)
		b.WriteString(s[i : i+n])
		i += n
	}
	if styled {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// renderer paints frames in the normal screen, so that a form scrolls with the
// terminal and what was typed stays visible afterwards. Only changed lines are
// written. The cursor rests on the frame's last line.
type renderer struct {
	w          io.Writer
	cols, rows int
	prev       []string
}

// layout splits a frame into lines that each fit one terminal row. A frame
// taller than the screen keeps its last lines, because the cursor cannot move
// above the top of the screen to repaint the others.
func (r *renderer) layout(frame string) []string {
	lines := strings.Split(strings.ReplaceAll(frame, "\r", ""), "\n")
	for i, line := range lines {
		lines[i] = Truncate(line, r.cols)
	}
	if limit := r.rows - 1; limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// paint draws frame over the previous one.
func (r *renderer) paint(frame string) error {
	next := r.layout(frame)
	var b strings.Builder
	if len(r.prev) > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", len(r.prev)-1)
	}
	b.WriteByte('\r')
	for i, line := range next {
		if i > 0 {
			b.WriteString("\r\n")
		}
		if r.prev == nil || i >= len(r.prev) || r.prev[i] != line {
			b.WriteString(line)
			b.WriteString("\x1b[K")
		}
	}
	if len(next) < len(r.prev) {
		// The rows below exist already, so this step down cannot scroll. It
		// keeps the erase from touching the frame's last line.
		b.WriteString("\r\n\x1b[J\x1b[1A")
	}
	r.prev = next
	_, err := io.WriteString(r.w, b.String())
	return err
}

// resize adopts a new size. The old frame may have been rewrapped by the
// terminal, so it is erased using the height it has at the new width, and the
// next paint draws everything.
func (r *renderer) resize(cols, rows int) error {
	height := 0
	for _, line := range r.prev {
		height += max(1, (Width(line)+cols-1)/max(cols, 1))
	}
	r.cols, r.rows = cols, rows
	if rows > 0 {
		height = min(height, rows)
	}
	var b strings.Builder
	if height > 1 {
		fmt.Fprintf(&b, "\x1b[%dA", height-1)
	}
	b.WriteString("\r\x1b[J")
	r.prev = nil
	_, err := io.WriteString(r.w, b.String())
	return err
}
