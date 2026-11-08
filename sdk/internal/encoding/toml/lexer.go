package toml

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// Error is a syntax error with its 1-based position. Column counts bytes.
type Error struct {
	Line, Column int
	Msg          string
}

func (e *Error) Error() string {
	return fmt.Sprintf("toml: line %d, column %d: %s", e.Line, e.Column, e.Msg)
}

// scanner reads the tokens of TOML. It reports a syntax error by panicking
// with *Error, which Parse recovers; that keeps the grammar readable.
type scanner struct {
	src string
	pos int
}

func (s *scanner) failAt(pos int, format string, args ...any) {
	before := s.src[:pos]
	panic(&Error{
		Line:   strings.Count(before, "\n") + 1,
		Column: pos - strings.LastIndexByte(before, '\n'),
		Msg:    fmt.Sprintf(format, args...),
	})
}

func (s *scanner) fail(format string, args ...any) { s.failAt(s.pos, format, args...) }

func (s *scanner) eof() bool { return s.pos >= len(s.src) }

// peek returns the next byte, or 0 once the input is exhausted.
func (s *scanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.pos]
}

func (s *scanner) hasPrefix(p string) bool { return strings.HasPrefix(s.src[s.pos:], p) }

func (s *scanner) skipSpace() {
	for s.peek() == ' ' || s.peek() == '\t' {
		s.pos++
	}
}

// newline consumes LF or CRLF. A CR alone is not a newline in TOML.
func (s *scanner) newline() bool {
	switch {
	case s.peek() == '\n':
		s.pos++
	case s.hasPrefix("\r\n"):
		s.pos += 2
	default:
		return false
	}
	return true
}

func isControl(c byte) bool { return (c < 0x20 && c != '\t') || c == 0x7f }

// comment consumes a comment if one starts here, leaving the newline.
func (s *scanner) comment() {
	if s.peek() != '#' {
		return
	}
	for !s.eof() && s.peek() != '\n' && !s.hasPrefix("\r\n") {
		if isControl(s.peek()) {
			s.fail("control character in a comment")
		}
		s.pos++
	}
}

// endOfLine consumes what may follow a statement: space, a comment, a newline.
func (s *scanner) endOfLine() {
	s.skipSpace()
	s.comment()
	if !s.eof() && !s.newline() {
		s.fail("expected the end of the line")
	}
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// key reads a dotted key and returns its parts.
func (s *scanner) key() []string {
	parts := []string{s.keyPart()}
	for {
		mark := s.pos
		s.skipSpace()
		if s.peek() != '.' {
			s.pos = mark
			return parts
		}
		s.pos++
		s.skipSpace()
		parts = append(parts, s.keyPart())
	}
}

func (s *scanner) keyPart() string {
	switch s.peek() {
	case '"':
		s.pos++
		return s.basicString()
	case '\'':
		s.pos++
		return s.literalString()
	}
	start := s.pos
	for isBareKeyChar(s.peek()) {
		s.pos++
	}
	if start == s.pos {
		s.fail("expected a key")
	}
	return s.src[start:s.pos]
}

// stringValue reads any of the four string forms.
func (s *scanner) stringValue() string {
	switch {
	case s.hasPrefix(`"""`):
		s.pos += 3
		return s.multiBasicString()
	case s.hasPrefix(`'''`):
		s.pos += 3
		return s.multiLiteralString()
	case s.peek() == '"':
		s.pos++
		return s.basicString()
	}
	s.pos++
	return s.literalString()
}

// basicString reads up to the closing quote; the opening one is consumed.
func (s *scanner) basicString() string {
	var b strings.Builder
	for {
		switch c := s.peek(); {
		case s.eof():
			s.fail("unterminated string")
		case c == '"':
			s.pos++
			return b.String()
		case c == '\\':
			s.escape(&b)
		case isControl(c):
			s.fail("control character in a string")
		default:
			b.WriteByte(c)
			s.pos++
		}
	}
}

func (s *scanner) literalString() string {
	start := s.pos
	for {
		switch c := s.peek(); {
		case s.eof():
			s.fail("unterminated string")
		case c == '\'':
			s.pos++
			return s.src[start : s.pos-1]
		case isControl(c):
			s.fail("control character in a string")
		default:
			s.pos++
		}
	}
}

// escape reads one escape sequence, with s at the backslash.
func (s *scanner) escape(b *strings.Builder) {
	start := s.pos
	s.pos++
	c := s.peek()
	s.pos++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'u':
		b.WriteRune(s.unicodeEscape(start, 4))
	case 'U':
		b.WriteRune(s.unicodeEscape(start, 8))
	default:
		s.failAt(start, "invalid escape sequence")
	}
}

func (s *scanner) unicodeEscape(start, digits int) rune {
	end := min(s.pos+digits, len(s.src))
	v, err := strconv.ParseUint(s.src[s.pos:end], 16, 32)
	if err != nil || end-s.pos != digits || strings.ContainsAny(s.src[s.pos:end], "+-_") {
		s.failAt(start, "invalid unicode escape")
	}
	if v > utf8.MaxRune || !utf8.ValidRune(rune(v)) {
		s.failAt(start, "escape is not a unicode scalar value")
	}
	s.pos = end
	return rune(v)
}

// closingQuotes handles a run of quote characters inside a multi-line string.
// It writes the quotes that belong to the content and reports whether the run
// closed the string. One or two quotes are content; three close it, and up to
// two more just before them are content too.
func (s *scanner) closingQuotes(b *strings.Builder, q byte) bool {
	n := 0
	for s.pos+n < len(s.src) && s.src[s.pos+n] == q {
		n++
	}
	if n > 5 {
		s.failAt(s.pos+5, "too many quotes at the end of a multi-line string")
	}
	closes := n >= 3
	content := n
	if closes {
		content = n - 3
	}
	for range content {
		b.WriteByte(q)
	}
	s.pos += n
	return closes
}

func (s *scanner) multiBasicString() string {
	s.newline()
	var b strings.Builder
	for {
		switch c := s.peek(); {
		case s.eof():
			s.fail("unterminated multi-line string")
		case c == '"':
			if s.closingQuotes(&b, '"') {
				return b.String()
			}
		case c == '\\':
			if !s.lineContinuation() {
				s.escape(&b)
			}
		case s.newline():
			b.WriteByte('\n')
		case isControl(c):
			s.fail("control character in a multi-line string")
		default:
			b.WriteByte(c)
			s.pos++
		}
	}
}

// lineContinuation consumes a backslash at the end of a line with all the
// white space after it, and reports whether there was one.
func (s *scanner) lineContinuation() bool {
	mark := s.pos
	s.pos++
	s.skipSpace()
	if !s.newline() {
		s.pos = mark
		return false
	}
	for {
		s.skipSpace()
		if !s.newline() {
			return true
		}
	}
}

func (s *scanner) multiLiteralString() string {
	s.newline()
	var b strings.Builder
	for {
		switch c := s.peek(); {
		case s.eof():
			s.fail("unterminated multi-line string")
		case c == '\'':
			if s.closingQuotes(&b, '\'') {
				return b.String()
			}
		case s.newline():
			b.WriteByte('\n')
		case isControl(c):
			s.fail("control character in a multi-line string")
		default:
			b.WriteByte(c)
			s.pos++
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isTokenChar(c byte) bool {
	return isBareKeyChar(c) || c == '+' || c == '.' || c == ':'
}

func (s *scanner) tokenChars() {
	for isTokenChar(s.peek()) {
		s.pos++
	}
}

// scalar reads a boolean, number, date or time.
func (s *scanner) scalar() *tree.Node {
	start := s.pos
	s.tokenChars()
	// A date and a time may be separated by one space.
	if s.pos-start == 10 && s.src[start+4] == '-' && s.peek() == ' ' &&
		s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1]) {
		s.pos++
		s.tokenChars()
	}
	token := s.src[start:s.pos]
	switch token {
	case "true":
		return tree.NewBool(true)
	case "false":
		return tree.NewBool(false)
	}
	if n, msg := parseDatetime(token); n != nil {
		return n
	} else if msg != "" {
		s.failAt(start, "%s", msg)
	}
	n, msg := parseNumber(token)
	if msg != "" {
		s.failAt(start, "%s", msg)
	}
	return n
}
