package toml

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// EncodeError says why a value cannot be written as TOML.
type EncodeError struct{ Msg string }

func (e *EncodeError) Error() string { return "toml: " + e.Msg }

func encodeFail(format string, args ...any) { panic(&EncodeError{fmt.Sprintf(format, args...)}) }

// catchEncode turns an EncodeError panic into the function's error.
func catchEncode(err *error) {
	if r := recover(); r != nil {
		*err = r.(*EncodeError) // any other panic is a bug and stays one
	}
}

// Encode writes a value tree as a new document, without comments. Use a
// Document to keep an existing file's comments.
func Encode(root *tree.Node) (out []byte, err error) {
	defer catchEncode(&err)
	if root.Kind != tree.Table {
		encodeFail("the root must be a table, not %s", root.Kind)
	}
	var b strings.Builder
	writeSection(&b, nil, root, 0)
	return []byte(b.String()), nil
}

// isSection reports whether a value is written under its own header.
func isSection(v *tree.Node) bool {
	if v.Kind == tree.Table {
		return true
	}
	return v.Kind == tree.Array && len(v.Items) > 0 && !slices.ContainsFunc(v.Items, func(item *tree.Node) bool {
		return item.Kind != tree.Table
	})
}

func hasValues(t *tree.Node) bool {
	for _, k := range t.Keys() {
		if v, _ := t.Get(k); !isSection(v) {
			return true
		}
	}
	return false
}

func writeHeader(b *strings.Builder, open string, path []string, closer string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(open + keyText(path) + closer + "\n")
}

func checkDepth(depth int) {
	if depth > maxDepth {
		encodeFail("nesting is deeper than %d levels", maxDepth)
	}
}

// writeSection writes a table's plain values and then its sub-tables, which
// must come last because a header ends the table before it.
func writeSection(b *strings.Builder, path []string, t *tree.Node, depth int) {
	checkDepth(depth)
	for _, k := range t.Keys() {
		v, _ := t.Get(k)
		if !isSection(v) {
			b.WriteString(keyText([]string{k}) + " = ")
			writeValue(b, v, depth+1)
			b.WriteByte('\n')
		}
	}
	for _, k := range t.Keys() {
		v, _ := t.Get(k)
		if !isSection(v) {
			continue
		}
		sub := append(slices.Clip(path), k)
		if v.Kind == tree.Table {
			if v.Len() == 0 || hasValues(v) {
				writeHeader(b, "[", sub, "]")
			}
			writeSection(b, sub, v, depth+1)
			continue
		}
		checkDepth(depth + 1)
		for _, item := range v.Items {
			writeHeader(b, "[[", sub, "]]")
			writeSection(b, sub, item, depth+2)
		}
	}
}

// writeValue writes a value on one line.
func writeValue(b *strings.Builder, n *tree.Node, depth int) {
	checkDepth(depth)
	switch n.Kind {
	case tree.String:
		writeString(b, n.Str)
	case tree.Integer:
		b.WriteString(strconv.FormatInt(n.Int, 10))
	case tree.Float:
		b.WriteString(floatText(n.Flt))
	case tree.Bool:
		b.WriteString(strconv.FormatBool(n.Bool))
	case tree.OffsetDateTime, tree.LocalDateTime, tree.LocalDate, tree.LocalTime:
		if parsed, _ := parseDatetime(n.Str); parsed == nil || parsed.Kind != n.Kind {
			encodeFail("%q is not a valid %s", n.Str, n.Kind)
		}
		b.WriteString(n.Str)
	case tree.Array:
		b.WriteByte('[')
		for i, item := range n.Items {
			if i > 0 {
				b.WriteString(", ")
			}
			writeValue(b, item, depth+1)
		}
		b.WriteByte(']')
	case tree.Table:
		b.WriteByte('{')
		for i, k := range n.Keys() {
			if i > 0 {
				b.WriteString(", ")
			}
			v, _ := n.Get(k)
			b.WriteString(keyText([]string{k}) + " = ")
			writeValue(b, v, depth+1)
		}
		b.WriteByte('}')
	default:
		encodeFail("TOML has no %s value", n.Kind)
	}
}

func floatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

func writeString(b *strings.Builder, s string) {
	if !utf8.ValidString(s) {
		encodeFail("string %q is not valid UTF-8", s)
	}
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\r':
			b.WriteString(`\r`)
		case isControl(c):
			fmt.Fprintf(b, `\u%04X`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// keyText writes a dotted key, quoting the parts that are not bare.
func keyText(parts []string) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte('.')
		}
		if part != "" && strings.IndexFunc(part, notBare) < 0 {
			b.WriteString(part)
		} else {
			writeString(&b, part)
		}
	}
	return b.String()
}

func notBare(r rune) bool { return r >= utf8.RuneSelf || !isBareKeyChar(byte(r)) }

// Encode writes the document back: unedited statements are byte for byte what
// was read.
func (d *Document) Encode() []byte { return []byte(joinStatements(d.stmts)) }

func joinStatements(stmts []statement) string {
	var b strings.Builder
	for i := range stmts {
		b.WriteString(stmts[i].head)
		b.WriteString(stmts[i].value)
		b.WriteString(stmts[i].tail)
	}
	return b.String()
}

// commit replaces the document with an edited statement list. Parsing the
// result is what guarantees that an edit can never produce an invalid file;
// on failure the document stays as it was.
func (d *Document) commit(stmts []statement) error {
	next, err := Parse([]byte(joinStatements(stmts)))
	if err != nil {
		return fmt.Errorf("the edit would make the document invalid: %w", err)
	}
	*d = *next
	return nil
}

func inlineText(v *tree.Node) (text string, err error) {
	defer catchEncode(&err)
	var b strings.Builder
	writeValue(&b, v, 1)
	return b.String(), nil
}

func hasPathPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

// keyStatement finds the key/value statement whose key is path or a prefix of
// path. Keys under an array of tables have no single place to edit.
func (d *Document) keyStatement(path []string) (int, error) {
	for i := range d.stmts {
		st := &d.stmts[i]
		if st.kind == keyValue && hasPathPrefix(path, st.path) {
			if st.aot {
				return 0, fmt.Errorf("toml: %q is inside an array of tables", strings.Join(path, "."))
			}
			return i, nil
		}
	}
	return -1, nil
}

// inlineEntry locates one key/value of an inline table in its source text.
type inlineEntry struct {
	parts                       []string
	start, valueStart, valueEnd int
}

// scanInline finds the entries of an inline table whose text already parsed.
func scanInline(text string) []inlineEntry {
	p := &parser{scanner: scanner{src: text, pos: 1}, meta: map[*tree.Node]*nodeMeta{}}
	var entries []inlineEntry
	for {
		p.skipSpace()
		if p.peek() == '}' {
			return entries
		}
		e := inlineEntry{start: p.pos}
		e.parts = p.key()
		p.skipSpace()
		p.pos++ // the equals sign
		p.skipSpace()
		e.valueStart = p.pos
		p.value(1)
		e.valueEnd = p.pos
		entries = append(entries, e)
		p.skipSpace()
		if p.peek() == '}' {
			return entries
		}
		p.pos++ // the comma
	}
}

func notATable(path []string) error {
	return fmt.Errorf("toml: %q is not a table", strings.Join(path, "."))
}

// spliceInline rewrites only the span of the field at rest inside an inline
// table's text, so that its siblings keep their bytes, comments included. A
// nil newValue removes the field. It reports whether the text changed.
func spliceInline(text string, rest []string, newValue *string) (string, bool, error) {
	entries := scanInline(text)
	for i, e := range entries {
		switch {
		case slices.Equal(e.parts, rest):
			if newValue == nil {
				return removeEntry(text, entries, i), true, nil
			}
			return text[:e.valueStart] + *newValue + text[e.valueEnd:], true, nil
		case hasPathPrefix(rest, e.parts):
			value := text[e.valueStart:e.valueEnd]
			if value[0] != '{' {
				return "", false, notATable(e.parts)
			}
			inner, changed, err := spliceInline(value, rest[len(e.parts):], newValue)
			return text[:e.valueStart] + inner + text[e.valueEnd:], changed, err
		case hasPathPrefix(e.parts, rest):
			return "", false, fmt.Errorf("toml: %q is a table, not a key", strings.Join(rest, "."))
		}
	}
	if newValue == nil {
		return text, false, nil
	}
	field := keyText(rest) + " = " + *newValue
	if len(entries) == 0 {
		return text[:1] + field + text[1:], true, nil
	}
	end := entries[len(entries)-1].valueEnd
	return text[:end] + ", " + field + text[end:], true, nil
}

// removeEntry cuts one entry and the comma that goes with it.
func removeEntry(text string, entries []inlineEntry, i int) string {
	e := entries[i]
	switch {
	case len(entries) == 1:
		return text[:e.start] + text[e.valueEnd:]
	case i < len(entries)-1:
		return text[:e.start] + text[entries[i+1].start:]
	}
	return text[:entries[i-1].valueEnd] + text[e.valueEnd:]
}

// editInline changes one field inside the inline table held by the statement
// at index. Nothing is committed when the field is absent and newValue is nil.
func (d *Document) editInline(index int, path []string, newValue *string) (bool, error) {
	st := d.stmts[index]
	if st.value[0] != '{' {
		return false, notATable(st.path)
	}
	text, changed, err := spliceInline(st.value, path[len(st.path):], newValue)
	if err != nil || !changed {
		return false, err
	}
	stmts := slices.Clone(d.stmts)
	stmts[index].value = text
	if err := d.commit(stmts); err != nil {
		return false, err
	}
	return true, nil
}

// Set writes value at path, keeping every other byte of the document. An
// existing key keeps its place and its comment; a new key goes after the last
// key of its section; a missing section is added at the end. value must not
// be nil.
func (d *Document) Set(path []string, value *tree.Node) error {
	if len(path) == 0 {
		return &EncodeError{"an empty path names no key"}
	}
	text, err := inlineText(value)
	if err != nil {
		return err
	}
	index, err := d.keyStatement(path)
	if err != nil {
		return err
	}
	if index >= 0 {
		if len(d.stmts[index].path) < len(path) {
			_, err := d.editInline(index, path, &text)
			return err
		}
		stmts := slices.Clone(d.stmts)
		stmts[index].value = text
		return d.commit(stmts)
	}
	if _, exists := d.root.Lookup(path...); exists {
		return fmt.Errorf("toml: %q is a table, not a key", strings.Join(path, "."))
	}
	return d.insert(path, text)
}

func (d *Document) newline() string {
	for i := range d.stmts {
		st := &d.stmts[i]
		if strings.HasSuffix(st.head, "\r\n") || strings.HasSuffix(st.tail, "\r\n") {
			return "\r\n"
		}
	}
	return "\n"
}

// nearestSection finds the longest table header that is a prefix of the
// table holding path's key. It returns -1 for the root.
func (d *Document) nearestSection(path []string) (int, error) {
	best, bestLen := -1, 0
	for i := range d.stmts {
		st := &d.stmts[i]
		if st.kind != tableHeader && st.kind != arrayHeader {
			continue
		}
		if !hasPathPrefix(path, st.path) || len(st.path) == len(path) {
			continue
		}
		if st.aot {
			return 0, fmt.Errorf("toml: %q is inside an array of tables", strings.Join(path, "."))
		}
		if len(st.path) > bestLen {
			best, bestLen = i, len(st.path)
		}
	}
	return best, nil
}

func (d *Document) insert(path []string, text string) error {
	section, err := d.nearestSection(path)
	if err != nil {
		return err
	}
	nl := d.newline()
	if section < 0 && len(path) > 1 {
		return d.appendSection(path, text, nl)
	}
	sectionLen := 0
	if section >= 0 {
		sectionLen = len(d.stmts[section].path)
	}
	at := d.insertionPoint(section)
	line := statement{kind: keyValue, head: keyText(path[sectionLen:]) + " = ", value: text, tail: nl}
	stmts := make([]statement, 0, len(d.stmts)+1)
	stmts = append(stmts, d.stmts[:at]...)
	terminateLast(stmts, nl)
	stmts = append(append(stmts, line), d.stmts[at:]...)
	return d.commit(stmts)
}

// insertionPoint returns the index just after the section's last key, or
// after its header when it has no keys. The root section's keys end at the
// first header, and with no keys a new one goes just before that header.
func (d *Document) insertionPoint(section int) int {
	at := section + 1
	for i := section + 1; i < len(d.stmts); i++ {
		switch d.stmts[i].kind {
		case keyValue:
			at = i + 1
		case tableHeader, arrayHeader:
			if section < 0 && at == 0 {
				return i
			}
			return at
		}
	}
	if section < 0 && at == 0 {
		return len(d.stmts)
	}
	return at
}

// terminateLast makes sure the last statement ends its line, so that a
// statement can follow it.
func terminateLast(stmts []statement, nl string) {
	if len(stmts) == 0 {
		return
	}
	last := &stmts[len(stmts)-1]
	if text := last.head + last.value + last.tail; text != bom && !strings.HasSuffix(text, "\n") {
		last.tail += nl
	}
}

func (d *Document) appendSection(path []string, text, nl string) error {
	stmts := slices.Clone(d.stmts)
	terminateLast(stmts, nl)
	var head strings.Builder
	if len(stmts) > 0 {
		head.WriteString(nl)
	}
	last := len(path) - 1
	head.WriteString("[" + keyText(path[:last]) + "]" + nl + keyText(path[last:]) + " = ")
	stmts = append(stmts, statement{kind: keyValue, head: head.String(), value: text, tail: nl})
	return d.commit(stmts)
}

// Unset removes the key at path and reports whether it was there. The
// comments around it stay.
func (d *Document) Unset(path []string) (bool, error) {
	if len(path) == 0 {
		return false, &EncodeError{"an empty path names no key"}
	}
	index, err := d.keyStatement(path)
	if err != nil {
		return false, err
	}
	if index >= 0 {
		if len(d.stmts[index].path) < len(path) {
			return d.editInline(index, path, nil)
		}
		return true, d.commit(slices.Delete(slices.Clone(d.stmts), index, index+1))
	}
	if _, exists := d.root.Lookup(path...); exists {
		return false, fmt.Errorf("toml: %q is a table, not a key", strings.Join(path, "."))
	}
	return false, nil
}
