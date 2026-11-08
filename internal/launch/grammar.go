package launch

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Options carries what the grammar needs from the environment.
type Options struct {
	// Home replaces a leading ~/ and must be absolute when one is used.
	Home string
}

// RefusedError says why a launch line is outside the grammar.
type RefusedError struct {
	Line   string
	Offset int
	Reason string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("launch command refused at byte %d: %s", e.Offset, e.Reason)
}

// Hint prints a structured [launch] section the user can start from, since a
// refused line has no compiled spec of its own.
func (e *RefusedError) Hint() string {
	return "[launch]\n" +
		`prelude = [{ command = ["sh", "-c", ` + tomlString(e.Line) + `], retry = "never" }]` + "\n" +
		`command = ["<agent executable>"]` + "\n"
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range strings.ToValidUTF8(s, "�") {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Compile turns the convenience grammar into a spec: simple commands joined
// by &&, each a sequence of words with POSIX quoting and K=V or env K=V
// prefixes. The last command is the agent; each one before it is a prelude
// step that must not repeat. Anything a shell would expand or chain another
// way is refused.
func Compile(line string, opts Options) (Spec, error) {
	if err := scan(line); err != nil {
		return Spec{}, err
	}
	lex := lexer{line: line, home: opts.Home}
	segments, err := lex.segments()
	if err != nil {
		return Spec{}, err
	}
	commands := make([]Command, len(segments))
	for i, segment := range segments {
		commands[i], err = lex.command(segment)
		if err != nil {
			return Spec{}, err
		}
	}
	last := len(commands) - 1
	spec := Spec{Command: commands[last].Argv}
	for _, command := range commands[:last] {
		spec.Prelude = append(spec.Prelude, Step{Command: command})
	}
	for _, e := range commands[last].Env {
		if spec.Env == nil {
			spec.Env = map[string]string{}
		}
		spec.Env[e.Name] = e.Value
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, &RefusedError{Line: line, Reason: err.Error()}
	}
	return spec, nil
}

// scan refuses bytes that have no place anywhere in a line. A tab is allowed
// here as a word separator; inside a quoted word Validate refuses it.
func scan(line string) error {
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		if r == utf8.RuneError && size == 1 {
			return &RefusedError{Line: line, Offset: i, Reason: "not valid UTF-8"}
		}
		if r != '\t' && unicode.IsControl(r) {
			return &RefusedError{Line: line, Offset: i, Reason: "control character"}
		}
		i += size
	}
	return nil
}

type word struct {
	text   string
	offset int
	// assign is the index of the = of a K=V word, and 0 for any other word.
	assign int
}

type lexer struct {
	line string
	home string
	i    int
}

func (l *lexer) refuse(offset int, reason string) error {
	return &RefusedError{Line: l.line, Offset: offset, Reason: reason}
}

func (l *lexer) segments() ([][]word, error) {
	var segments [][]word
	var current []word
	for {
		for l.i < len(l.line) && (l.line[l.i] == ' ' || l.line[l.i] == '\t') {
			l.i++
		}
		if l.i >= len(l.line) {
			if len(current) == 0 {
				return nil, l.refuse(l.i, "empty command")
			}
			return append(segments, current), nil
		}
		if l.line[l.i] == '&' {
			if !strings.HasPrefix(l.line[l.i:], "&&") {
				return nil, l.refuse(l.i, "`&` is not allowed; only && joins commands")
			}
			if len(current) == 0 {
				return nil, l.refuse(l.i, "empty command before &&")
			}
			segments = append(segments, current)
			current = nil
			l.i += 2
			continue
		}
		w, err := l.word()
		if err != nil {
			return nil, err
		}
		current = append(current, w)
	}
}

// word reads one word. Each branch either consumes input or returns.
func (l *lexer) word() (word, error) {
	var b strings.Builder
	start := l.i
	firstQuoted := -1
scan:
	for l.i < len(l.line) {
		c := l.line[l.i]
		switch c {
		case ' ', '\t', '&':
			break scan
		case '\'', '"':
			if firstQuoted < 0 {
				firstQuoted = b.Len()
			}
			var err error
			if c == '\'' {
				err = l.single(&b)
			} else {
				err = l.double(&b)
			}
			if err != nil {
				return word{}, err
			}
		case '~':
			if err := l.tilde(&b, start); err != nil {
				return word{}, err
			}
		default:
			if reason := metaReason(c); reason != "" {
				return word{}, l.refuse(l.i, reason)
			}
			b.WriteByte(c)
			l.i++
		}
	}
	return newWord(b.String(), start, firstQuoted), nil
}

func newWord(text string, offset, firstQuoted int) word {
	w := word{text: text, offset: offset}
	eq := strings.IndexByte(text, '=')
	if eq > 0 && validName(text[:eq]) && (firstQuoted < 0 || eq < firstQuoted) {
		w.assign = eq
	}
	return w
}

func metaReason(c byte) string {
	switch c {
	case '$':
		return "`$` expansion is not allowed"
	case '`':
		return "backtick command substitution is not allowed"
	case '\\':
		return "backslash outside quotes is not allowed"
	case '|', ';', '(', ')', '<', '>', '*', '?', '[', ']', '{', '}', '#', '!':
		return fmt.Sprintf("`%c` is not allowed", c)
	}
	return ""
}

func (l *lexer) single(b *strings.Builder) error {
	open := l.i
	end := strings.IndexByte(l.line[open+1:], '\'')
	if end < 0 {
		return l.refuse(open, "unterminated single quote")
	}
	b.WriteString(l.line[open+1 : open+1+end])
	l.i = open + end + 2
	return nil
}

func (l *lexer) double(b *strings.Builder) error {
	open := l.i
	for l.i++; l.i < len(l.line); l.i++ {
		c := l.line[l.i]
		switch c {
		case '"':
			l.i++
			return nil
		case '\\':
			if l.i+1 < len(l.line) && strings.IndexByte("\"\\$`", l.line[l.i+1]) >= 0 {
				l.i++
				b.WriteByte(l.line[l.i])
				continue
			}
			b.WriteByte(c)
		case '$', '`':
			return l.refuse(l.i, metaReason(c))
		default:
			b.WriteByte(c)
		}
	}
	return l.refuse(open, "unterminated double quote")
}

// tilde handles an unquoted ~. A leading ~/ becomes the home folder. A tilde
// the shell would expand any other way is refused, and one inside a word,
// such as HEAD~1, is an ordinary character.
func (l *lexer) tilde(b *strings.Builder, start int) error {
	if l.i == start {
		if !strings.HasPrefix(l.line[l.i:], "~/") {
			return l.refuse(l.i, "tilde is only allowed as a leading ~/")
		}
		if !filepath.IsAbs(l.home) {
			return l.refuse(l.i, "a leading ~/ needs an absolute home folder")
		}
		b.WriteString(l.home)
		l.i++
		return nil
	}
	if prev := l.line[l.i-1]; prev == '=' || prev == ':' {
		return l.refuse(l.i, "tilde is only allowed as a leading ~/")
	}
	b.WriteByte('~')
	l.i++
	return nil
}

// command strips the K=V and env K=V prefixes off one simple command.
func (l *lexer) command(segment []word) (Command, error) {
	var c Command
	i := 0
	for i < len(segment) {
		if w := segment[i]; w.assign > 0 {
			c.Env = append(c.Env, EnvVar{Name: w.text[:w.assign], Value: w.text[w.assign+1:]})
			i++
			continue
		}
		if segment[i].text == "env" && i+1 < len(segment) && segment[i+1].assign > 0 {
			i++
			continue
		}
		break
	}
	if i == len(segment) {
		return Command{}, l.refuse(segment[0].offset, "no program after the assignments")
	}
	for _, w := range segment[i:] {
		c.Argv = append(c.Argv, w.text)
	}
	return c, nil
}
