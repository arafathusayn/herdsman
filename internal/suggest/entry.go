// Package suggest is the catalog behind `herdsman suggest`: the entry format,
// the sources in precedence, the deterministic ranking with its reasons, the
// bundled catalog and the pinned remote catalogs.
//
// Every entry is untrusted text, and a remote one is third-party text. The
// package only parses, ranks and renders; it never runs a recipe.
package suggest

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The limits a parser of hostile input needs. They only ever grow.
const (
	// MaxEntryBytes is the largest entry file.
	MaxEntryBytes = 64 << 10
	// MaxListItems is the longest list in the front matter.
	MaxListItems = 64
	// maxDepth is how deep maps may nest.
	maxDepth = 4
)

// Kind says what an entry suggests.
type Kind string

// The kinds of entry.
const (
	KindTip    Kind = "tip"
	KindMCP    Kind = "mcp"
	KindSkill  Kind = "skill"
	KindPlugin Kind = "plugin"
	KindMod    Kind = "mod"
	KindRepo   Kind = "repo"
)

// AppliesTo limits an entry to some machines. An empty list means any.
type AppliesTo struct {
	Harnesses  []string `json:"harnesses,omitempty"`
	Ecosystems []string `json:"ecosystems,omitempty"`
	Platforms  []string `json:"platforms,omitempty"`
}

// Install is an entry's recipe: exactly one of a shell command, an MCP config
// snippet or a skill repository, and the files it would touch.
type Install struct {
	Command   string   `json:"command,omitempty"`
	MCP       string   `json:"mcp,omitempty"`
	SkillRepo string   `json:"skill_repo,omitempty"`
	Touches   []string `json:"touches,omitempty"`
}

// Entry is one suggestion. The body is its Markdown.
type Entry struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags"`
	URL       string    `json:"url,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	AppliesTo AppliesTo `json:"applies_to"`
	Install   *Install  `json:"install,omitempty"`
	Since     string    `json:"since"`
	Body      string    `json:"body"`
	Source    SourceRef `json:"source"`
}

// ParseEntry reads an entry file: front matter between `---` lines, then the
// Markdown body. It checks the shape only; Validate checks the content. Keys
// it does not know are skipped, so a newer catalog still loads.
//
// The front matter is a small subset of YAML: `key: value`, flow lists
// (`[a, b]`), nested maps by indentation and `|` block text. Anything else,
// anchors and custom tags included, is refused.
func ParseEntry(data []byte) (Entry, error) {
	text, err := cleanText(data)
	if err != nil {
		return Entry{}, err
	}
	head, body, err := splitFrontMatter(text)
	if err != nil {
		return Entry{}, err
	}
	p := parser{lines: strings.Split(head, "\n")}
	fields, err := p.block(0, 0)
	if err != nil {
		return Entry{}, err
	}
	d := decoder{}
	e := Entry{
		ID:    d.text(fields, "id"),
		Kind:  Kind(d.text(fields, "kind")),
		Title: d.text(fields, "title"),
		Tags:  d.list(fields, "tags"),
		URL:   d.text(fields, "url"),
		Repo:  d.text(fields, "repo"),
		Since: d.text(fields, "since"),
		Body:  strings.TrimSpace(body),
	}
	if m, ok := d.children(fields, "applies_to"); ok {
		e.AppliesTo = AppliesTo{Harnesses: d.list(m, "harnesses"), Ecosystems: d.list(m, "ecosystems"), Platforms: d.list(m, "platforms")}
	}
	if m, ok := d.children(fields, "install"); ok {
		e.Install = &Install{Command: d.text(m, "command"), MCP: d.text(m, "mcp"), SkillRepo: d.text(m, "skill_repo"), Touches: d.list(m, "touches")}
	}
	return e, d.err
}

// cleanText refuses what must never reach a terminal or a diff: invalid UTF-8,
// control characters and bidirectional overrides. Refusing them for the whole
// file also covers comments and keys that no field carries.
func cleanText(data []byte) (string, error) {
	if len(data) > MaxEntryBytes {
		return "", fmt.Errorf("entry is larger than %d bytes", MaxEntryBytes)
	}
	if !utf8.Valid(data) {
		return "", errors.New("entry is not valid UTF-8")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, r := range text {
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || isBidi(r)) {
			return "", fmt.Errorf("entry contains the control character %U", r)
		}
	}
	return text, nil
}

func isBidi(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

func splitFrontMatter(text string) (head, body string, err error) {
	rest, found := strings.CutPrefix(text, "---\n")
	if !found {
		return "", "", errors.New("entry must start with a --- front matter line")
	}
	if tail, ok := strings.CutPrefix(rest, "---"); ok && (tail == "" || tail[0] == '\n') {
		return "", tail, nil
	}
	for offset := 0; ; {
		i := strings.Index(rest[offset:], "\n---")
		if i < 0 {
			return "", "", errors.New("front matter has no closing --- line")
		}
		end := offset + i
		tail := rest[end+len("\n---"):]
		if tail == "" || tail[0] == '\n' {
			return rest[:end], tail, nil
		}
		offset = end + 1
	}
}

// node is one parsed value: a scalar, a list or a map.
type node struct {
	scalar   string
	list     []string
	children map[string]node
	isList   bool
}

type parser struct {
	lines []string
	pos   int
}

var keyPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// peek skips blank and comment lines and returns the next line's indent.
func (p *parser) peek() (indent int, ok bool, err error) {
	for ; p.pos < len(p.lines); p.pos++ {
		line := p.lines[p.pos]
		trimmed := strings.TrimLeft(line, " ")
		if strings.TrimSpace(trimmed) == "" || trimmed[0] == '#' {
			continue
		}
		if trimmed[0] == '\t' {
			return 0, false, fmt.Errorf("line %d: a tab indents the front matter", p.pos+2)
		}
		return len(line) - len(trimmed), true, nil
	}
	return 0, false, nil
}

func (p *parser) block(indent, depth int) (map[string]node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: front matter is nested too deep", p.pos+2)
	}
	out := map[string]node{}
	for {
		got, ok, err := p.peek()
		if err != nil {
			return nil, err
		}
		if !ok || got < indent {
			return out, nil
		}
		if got > indent {
			return nil, fmt.Errorf("line %d: unexpected indent", p.pos+2)
		}
		number := p.pos + 2
		key, rest, found := strings.Cut(strings.TrimSpace(p.lines[p.pos]), ":")
		if !found || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: expected a key and a value", number)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", number, key)
		}
		p.pos++
		value, err := p.value(strings.TrimSpace(rest), indent, depth)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", number, err)
		}
		out[key] = value
	}
}

func (p *parser) value(rest string, indent, depth int) (node, error) {
	switch {
	case rest == "|":
		return node{scalar: p.blockText(indent)}, nil
	case rest == "" || rest[0] == '#':
		next, ok, err := p.peek()
		if err != nil {
			return node{}, err
		}
		if !ok || next <= indent {
			return node{}, nil
		}
		children, err := p.block(next, depth+1)
		return node{children: children}, err
	case rest[0] == '[':
		list, err := parseFlowList(rest)
		return node{list: list, isList: true}, err
	}
	s, err := parseScalar(rest)
	return node{scalar: s}, err
}

// blockText reads the lines of a `|` value: everything indented deeper than
// its key, with the first line's indent removed.
func (p *parser) blockText(indent int) string {
	var lines []string
	strip := -1
	for ; p.pos < len(p.lines); p.pos++ {
		line := p.lines[p.pos]
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" {
			lines = append(lines, "")
			continue
		}
		got := len(line) - len(trimmed)
		if got <= indent {
			break
		}
		if strip < 0 {
			strip = got
		}
		lines = append(lines, line[min(strip, got):])
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func parseFlowList(rest string) ([]string, error) {
	var raw []string
	var quote byte
	start := 1
	end := -1
scan:
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == ',':
			raw = append(raw, rest[start:i])
			start = i + 1
		case c == ']':
			end = i
			break scan
		}
	}
	if end < 0 {
		return nil, errors.New("unterminated list; a list stays on one line")
	}
	if last := rest[start:end]; len(raw) > 0 || strings.TrimSpace(last) != "" {
		raw = append(raw, last)
	}
	if len(raw) > MaxListItems {
		return nil, fmt.Errorf("list has more than %d items", MaxListItems)
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, errors.New("list has an empty item")
		}
		s, err := parseScalar(item)
		if err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if err := checkTrailing(rest[end+1:]); err != nil {
		return nil, err
	}
	return items, nil
}

// checkTrailing allows only a comment after a closed value.
func checkTrailing(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest != "" && rest[0] != '#' {
		return fmt.Errorf("unexpected text after the value: %q", rest)
	}
	return nil
}

func parseScalar(s string) (string, error) {
	switch s[0] {
	case '"':
		return parseDoubleQuoted(s)
	case '\'':
		return parseSingleQuoted(s)
	case '&', '*', '!', '{', '[', '>', '%', '@', '`':
		return "", fmt.Errorf("a value starting with %q is not supported; quote it", s[0])
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if strings.Contains(s, ": ") {
		return "", errors.New("a value containing a colon and a space needs a quote")
	}
	return s, nil
}

func parseDoubleQuoted(s string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), checkTrailing(s[i+1:])
		case '\\':
			i++
			if i == len(s) {
				return "", errors.New("unterminated quoted value")
			}
			switch s[i] {
			case '"', '\\':
				b.WriteByte(s[i])
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", fmt.Errorf("unknown escape \\%c", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", errors.New("unterminated quoted value")
}

func parseSingleQuoted(s string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return b.String(), checkTrailing(s[i+1:])
	}
	return "", errors.New("unterminated quoted value")
}

// decoder reads typed fields and keeps the first error.
type decoder struct{ err error }

func (d *decoder) fail(key, want string) {
	if d.err == nil {
		d.err = fmt.Errorf("%s must be %s", key, want)
	}
}

func (d *decoder) text(m map[string]node, key string) string {
	n := m[key]
	if n.isList || n.children != nil {
		d.fail(key, "a single value")
	}
	return n.scalar
}

func (d *decoder) list(m map[string]node, key string) []string {
	n := m[key]
	if n.scalar != "" || n.children != nil {
		d.fail(key, "a list like [a, b]")
	}
	return n.list
}

func (d *decoder) children(m map[string]node, key string) (map[string]node, bool) {
	n := m[key]
	if n.isList || n.scalar != "" {
		d.fail(key, "a map")
	}
	return n.children, n.children != nil
}
