package yaml

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// ErrNotEditable marks a valid document that has no block mapping at its top,
// which the editor cannot change line by line.
var ErrNotEditable = errors.New("the document has no block mapping at its top")

// Document is a parsed file that can be edited with its comments and order
// kept. Every edit is checked by reading the result back and comparing it with
// the values the edit should produce, so an edit cannot change anything else.
type Document struct {
	lines   []string
	newline string
	final   bool // the file ends with a newline
	root    *tree.Node
}

// Parse reads a document for editing.
func Parse(data []byte) (*Document, error) {
	root, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if root.Kind == tree.Null {
		root = tree.NewTable()
	}
	if root.Kind != tree.Table {
		return nil, ErrNotEditable
	}
	text := string(data)
	doc := &Document{newline: "\n", root: root, final: strings.HasSuffix(text, "\n") || text == ""}
	if strings.Contains(text, "\r\n") {
		doc.newline = "\r\n"
	}
	if body := strings.TrimSuffix(text, "\n"); text != "" {
		for line := range strings.SplitSeq(body, "\n") {
			doc.lines = append(doc.lines, strings.TrimSuffix(line, "\r"))
		}
	}
	if _, _, _, err := doc.bounds(); err != nil {
		return nil, err
	}
	return doc, nil
}

// Tree returns the document's values; the caller must not change them.
func (d *Document) Tree() *tree.Node { return d.root }

// Encode writes the document back.
func (d *Document) Encode() []byte {
	out := strings.Join(d.lines, d.newline)
	if d.final && len(d.lines) > 0 {
		out += d.newline
	}
	return []byte(out)
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isTrivia(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || trimmed[0] == '#'
}

func isSequenceEntry(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	return trimmed == "-" || strings.HasPrefix(trimmed, "- ")
}

// bounds finds the lines of the top mapping and its indentation.
func (d *Document) bounds() (start, end, indent int, err error) {
	end = len(d.lines)
	for i, line := range d.lines {
		if strings.HasPrefix(line, "---") && (len(line) == 3 || line[3] == ' ') {
			if value, _ := splitComment(line[3:]); value != "" {
				return 0, 0, 0, ErrNotEditable
			}
			start = i + 1
			continue
		}
		if line == "..." || strings.HasPrefix(line, "... ") {
			end = i
			break
		}
	}
	for i := start; i < end; i++ {
		if isTrivia(d.lines[i]) {
			continue
		}
		indent = indentOf(d.lines[i])
		if _, _, ok := parseKey(d.lines[i][indent:]); !ok || isSequenceEntry(d.lines[i]) {
			return 0, 0, 0, ErrNotEditable
		}
		break
	}
	return start, end, indent, nil
}

// parseKey reads the key at the start of s, returning its text and the offset
// of its colon.
func parseKey(s string) (key string, colon int, ok bool) {
	raw, colon := "", -1
	if s[0] == '"' || s[0] == '\'' {
		closing := closingQuote(s)
		if closing < 0 {
			return "", 0, false
		}
		rest := strings.TrimLeft(s[closing+1:], " \t")
		if !strings.HasPrefix(rest, ":") {
			return "", 0, false
		}
		raw, colon = s[:closing+1], len(s)-len(rest)
	} else {
		for i := range len(s) {
			if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
				raw, colon = strings.TrimRight(s[:i], " \t"), i
				break
			}
		}
		if colon < 0 {
			return "", 0, false
		}
	}
	decoded, err := Decode([]byte(raw + ": 0\n"))
	if err != nil || decoded.Kind != tree.Table || decoded.Len() != 1 {
		return "", 0, false
	}
	return decoded.Keys()[0], colon, true
}

// closingQuote returns the index of the quote ending the scalar opened at s[0].
func closingQuote(s string) int {
	opener := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case opener == '"' && s[i] == '\\':
			i++
		case s[i] == opener && opener == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == opener:
			return i
		}
	}
	return -1
}

// splitComment cuts the text after a colon into its value and its comment,
// the comment keeping the spaces before it.
func splitComment(rest string) (value, comment string) {
	var open byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case open == '"' && c == '\\':
			i++
		case open != 0:
			if c == open {
				open = 0
			}
		case c == '"' || c == '\'':
			open = c
		case c == '#' && (i == 0 || rest[i-1] == ' ' || rest[i-1] == '\t'):
			valueEnd := len(strings.TrimRight(rest[:i], " \t"))
			return strings.TrimSpace(rest[:i]), rest[valueEnd:]
		}
	}
	return strings.TrimSpace(rest), ""
}

// entry is one key of a block mapping. Its lines run from line up to, not
// including, end, which stops at the last line of content so that comments
// after a value stay with whatever follows.
type entry struct {
	key           string
	line, end     int
	indent, colon int
	blockScalar   bool
}

func (d *Document) value(e entry) (string, string) { return splitComment(d.lines[e.line][e.colon+1:]) }

func (d *Document) scan(start, end, indent int) []entry {
	var out []entry
	for i := start; i < end; i++ {
		line := d.lines[i]
		if n := len(out); n > 0 && out[n-1].blockScalar && strings.TrimSpace(line) != "" && indentOf(line) > indent {
			out[n-1].end = i + 1
			continue
		}
		if isTrivia(line) {
			continue
		}
		if indentOf(line) == indent && !isSequenceEntry(line) {
			if key, colon, ok := parseKey(line[indent:]); ok {
				e := entry{key: key, line: i, end: i + 1, indent: indent, colon: indent + colon}
				value, _ := d.value(e)
				e.blockScalar = strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">")
				out = append(out, e)
				continue
			}
		}
		if n := len(out); n > 0 {
			out[n-1].end = i + 1
		}
	}
	return out
}

// child finds the block mapping inside an entry, if it holds one.
func (d *Document) child(e entry) (start, end, indent int, ok bool) {
	if value, _ := d.value(e); value != "" {
		return 0, 0, 0, false
	}
	for i := e.line + 1; i < e.end; i++ {
		if isTrivia(d.lines[i]) {
			continue
		}
		indent = indentOf(d.lines[i])
		if _, _, keyed := parseKey(d.lines[i][indent:]); !keyed || isSequenceEntry(d.lines[i]) {
			return 0, 0, 0, false
		}
		return e.line + 1, e.end, indent, true
	}
	return 0, 0, 0, false
}

// site is where a path leads: found at its last segment, stopped at a value
// that cannot be walked line by line, or missing below the mapping it reached.
type site struct {
	depth              int
	at                 entry
	found, inline      bool
	start, end, indent int
	siblings           []entry
}

func (d *Document) locate(path []string) (site, error) {
	start, end, indent, err := d.bounds()
	if err != nil {
		return site{}, err
	}
	for depth, key := range path {
		siblings := d.scan(start, end, indent)
		index := slices.IndexFunc(siblings, func(e entry) bool { return e.key == key })
		if index < 0 {
			return site{depth: depth, start: start, end: end, indent: indent, siblings: siblings}, nil
		}
		at := siblings[index]
		if depth == len(path)-1 {
			return site{depth: depth, at: at, found: true}, nil
		}
		var ok bool
		if start, end, indent, ok = d.child(at); !ok {
			return site{depth: depth, at: at, inline: true}, nil
		}
	}
	return site{}, errors.New("yaml: empty path")
}

func (d *Document) splice(from, to int, replacement ...string) {
	d.lines = slices.Concat(d.lines[:from], replacement, d.lines[to:])
}

// rendered is value as the text after a key at indent: the remainder of the
// key's line, then any further lines.
func rendered(value *tree.Node, indent int) (string, []string) {
	var b strings.Builder
	writeValue(&b, value, indent)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	return lines[0], lines[1:]
}

// replace writes a new value for an entry, keeping the comment on its line.
func (d *Document) replace(e entry, value *tree.Node) {
	_, comment := d.value(e)
	first, more := rendered(value, e.indent)
	head := d.lines[e.line][:e.colon+1] + first + comment
	d.splice(e.line, e.end, append([]string{head}, more...)...)
}

func (d *Document) insert(s site, key string, value *tree.Node) {
	at := s.end
	if n := len(s.siblings); n > 0 {
		at = s.siblings[n-1].end
	}
	first, more := rendered(value, s.indent)
	d.splice(at, at, append([]string{strings.Repeat(" ", s.indent) + keyText(key) + ":" + first}, more...)...)
}

func clone(in *tree.Node) *tree.Node {
	switch in.Kind {
	case tree.Table:
		out := tree.NewTable()
		for _, key := range in.Keys() {
			value, _ := in.Get(key)
			out.Set(key, clone(value))
		}
		return out
	case tree.Array:
		out := tree.NewArray()
		for _, item := range in.Items {
			out.Items = append(out.Items, clone(item))
		}
		return out
	default:
		copied := *in
		return &copied
	}
}

func setIn(root *tree.Node, path []string, value *tree.Node) error {
	node := root
	for _, key := range path[:len(path)-1] {
		next, ok := node.Get(key)
		if !ok {
			next = tree.NewTable()
			node.Set(key, next)
		}
		if next.Kind != tree.Table {
			return fmt.Errorf("yaml: %s holds a %s, not a table", key, next.Kind)
		}
		node = next
	}
	node.Set(path[len(path)-1], value)
	return nil
}

// renameIn changes one key of a table where it stands.
func renameIn(table *tree.Node, from, to string) {
	keys := slices.Clone(table.Keys())
	values := make([]*tree.Node, len(keys))
	for i, key := range keys {
		values[i], _ = table.Get(key)
		table.Delete(key)
	}
	for i, key := range keys {
		if key == from {
			key = to
		}
		table.Set(key, values[i])
	}
}

// nested wraps value in a table for each key of path.
func nested(path []string, value *tree.Node) *tree.Node {
	for i := len(path) - 1; i >= 0; i-- {
		table := tree.NewTable()
		table.Set(path[i], value)
		value = table
	}
	return value
}

// commit proves the edited lines read back as want, restoring saved if not.
func (d *Document) commit(saved []string, want *tree.Node) error {
	got, err := Decode(d.Encode())
	if err == nil && got.Kind == tree.Null {
		got = tree.NewTable()
	}
	if err == nil && !tree.Equal(got, want) {
		err = errors.New("the result would hold different values")
	}
	if err != nil {
		d.lines = saved
		return fmt.Errorf("yaml: cannot edit this document in place: %w", err)
	}
	d.root = want
	return nil
}

// Set writes value at path. An existing key keeps its place and its comment;
// a new key goes after the last key of its table.
func (d *Document) Set(path []string, value *tree.Node) error {
	want := clone(d.root)
	if err := setIn(want, path, value); err != nil {
		return err
	}
	s, err := d.locate(path)
	if err != nil {
		return err
	}
	saved := slices.Clone(d.lines)
	switch {
	case s.found:
		d.replace(s.at, value)
	case s.inline:
		held, _ := want.Lookup(path[:s.depth+1]...)
		d.replace(s.at, held)
	default:
		d.insert(s, path[s.depth], nested(path[s.depth+1:], value))
		d.final = d.final || len(saved) == 0
	}
	return d.commit(saved, want)
}

// Unset removes the key at path and reports whether it was there. A table left
// with no keys is written as {} so that it stays a table.
func (d *Document) Unset(path []string) (bool, error) {
	if _, ok := d.root.Lookup(path...); !ok {
		return false, nil
	}
	want := clone(d.root)
	parent, _ := want.Lookup(path[:len(path)-1]...)
	parent.Delete(path[len(path)-1])
	s, err := d.locate(path)
	if err != nil {
		return false, err
	}
	saved := slices.Clone(d.lines)
	if s.inline {
		held, _ := want.Lookup(path[:s.depth+1]...)
		d.replace(s.at, held)
		return true, d.commit(saved, want)
	}
	if !s.found {
		return false, errors.New("yaml: a key the tree holds was not found in the text")
	}
	d.splice(s.at.line, s.at.end)
	if len(path) > 1 {
		up, err := d.locate(path[:len(path)-1])
		if err != nil {
			return false, err
		}
		if _, _, _, hasChild := d.child(up.at); up.found && !hasChild {
			d.replace(up.at, tree.NewTable())
		}
	}
	return true, d.commit(saved, want)
}

// Rename changes the last key of path to name, leaving its value and comment.
func (d *Document) Rename(path []string, name string) error {
	if _, ok := d.root.Lookup(path...); !ok {
		return fmt.Errorf("yaml: %s is not set", strings.Join(path, "."))
	}
	want := clone(d.root)
	parent, _ := want.Lookup(path[:len(path)-1]...)
	renameIn(parent, path[len(path)-1], name)
	s, err := d.locate(path)
	if err != nil {
		return err
	}
	saved := slices.Clone(d.lines)
	switch {
	case s.found:
		line := d.lines[s.at.line]
		d.lines[s.at.line] = line[:s.at.indent] + keyText(name) + line[s.at.colon:]
	case s.inline:
		held, _ := want.Lookup(path[:s.depth+1]...)
		d.replace(s.at, held)
	default:
		return errors.New("yaml: a key the tree holds was not found in the text")
	}
	return d.commit(saved, want)
}
