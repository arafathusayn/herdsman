// Package toml reads and writes TOML 1.0. Parse keeps every byte of the
// source, so a document can be edited and written back with its comments and
// layout; Decode and Encode work on the plain value tree.
package toml

import (
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

const (
	// maxInputBytes bounds the size of a document; parsers take hostile input.
	maxInputBytes = 8 << 20
	// maxDepth bounds how deep a value may sit in the tree, counting tables
	// and arrays alike.
	maxDepth = 128

	bom = "\xef\xbb\xbf"
)

type statementKind uint8

const (
	trivia statementKind = iota // a blank or comment-only line
	keyValue
	tableHeader
	arrayHeader
)

// statement is one source line or multi-line value. Its three texts
// concatenate to exactly the bytes it was read from.
type statement struct {
	kind  statementKind
	head  string // for a key/value: everything before the value
	value string
	tail  string // space, comment and newline after the statement

	path []string // the full path of the key, or the table the header names
	aot  bool     // the path runs through an array of tables
}

// Document is a parsed file that remembers its source.
type Document struct {
	stmts []statement
	root  *tree.Node
}

// Tree returns the document's values. The caller must not change the tree;
// edits go through Set and Unset.
func (d *Document) Tree() *tree.Node { return d.root }

// Decode reads a document into a value tree.
func Decode(data []byte) (*tree.Node, error) {
	doc, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return doc.root, nil
}

// Parse reads a document and keeps its source for writing back.
func Parse(data []byte) (doc *Document, err error) {
	p := &parser{scanner: scanner{src: string(data)}, meta: map[*tree.Node]*nodeMeta{}}
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, r.(*Error) // any other panic is a bug and stays one
		}
	}()
	return p.document(), nil
}

// tableState records how a table came to exist; TOML's rules on reopening a
// table depend on it.
type tableState uint8

const (
	implicit tableState = iota // created as a prefix of a header
	explicit                   // named by a header
	dotted                     // created by a dotted key
	inline                     // an inline table, closed to additions
)

type nodeMeta struct {
	state tableState
	depth int
	aot   bool // an array created by [[header]]
}

type parser struct {
	scanner
	meta       map[*tree.Node]*nodeMeta
	root       *tree.Node
	current    *tree.Node
	section    []string
	sectionAOT bool
}

func (p *parser) document() *Document {
	if len(p.src) > maxInputBytes {
		p.fail("the document is larger than %d bytes", maxInputBytes)
	}
	if !utf8.ValidString(p.src) {
		p.failAt(firstInvalidUTF8(p.src), "invalid UTF-8")
	}
	p.root = tree.NewTable()
	p.meta[p.root] = &nodeMeta{state: explicit}
	p.current = p.root

	doc := &Document{root: p.root}
	if p.hasPrefix(bom) {
		p.pos = len(bom)
		doc.stmts = append(doc.stmts, statement{kind: trivia, head: bom})
	}
	for !p.eof() {
		doc.stmts = append(doc.stmts, p.statement())
	}
	return doc
}

// firstInvalidUTF8 needs s to hold an invalid sequence.
func firstInvalidUTF8(s string) int {
	for i := 0; ; {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
}

func (p *parser) statement() statement {
	start := p.pos
	p.skipSpace()
	switch c := p.peek(); {
	case c == '[':
		return p.header(start)
	case p.eof(), c == '#', c == '\n', c == '\r':
		p.endOfLine()
		return statement{kind: trivia, head: p.src[start:p.pos]}
	}
	return p.keyValue(start)
}

func (p *parser) header(start int) statement {
	st := statement{kind: tableHeader}
	closer := "]"
	p.pos++
	if p.peek() == '[' {
		st.kind, closer = arrayHeader, "]]"
		p.pos++
	}
	p.skipSpace()
	parts := p.key()
	p.skipSpace()
	if !p.hasPrefix(closer) {
		p.fail("expected %q to close the header", closer)
	}
	p.pos += len(closer)
	st.head = p.src[start:p.pos]

	p.sectionAOT = false
	p.current = p.enterSection(parts, st.kind == arrayHeader)
	p.section = parts
	st.path, st.aot = parts, p.sectionAOT

	tailStart := p.pos
	p.endOfLine()
	st.tail = p.src[tailStart:p.pos]
	return st
}

func (p *parser) newTable(parent *tree.Node, state tableState) *tree.Node {
	return p.register(tree.NewTable(), parent, state)
}

// register records a new node one level below parent.
func (p *parser) register(n, parent *tree.Node, state tableState) *tree.Node {
	depth := p.meta[parent].depth + 1
	if depth > maxDepth {
		p.fail("nesting is deeper than %d levels", maxDepth)
	}
	p.meta[n] = &nodeMeta{state: state, depth: depth}
	return n
}

// enterSection applies a header and returns the table that receives the
// key/values after it.
func (p *parser) enterSection(parts []string, array bool) *tree.Node {
	t := p.root
	last := len(parts) - 1
	for _, key := range parts[:last] {
		t = p.descendHeader(t, key)
	}
	if array {
		return p.appendArrayTable(t, parts[last])
	}
	child, ok := t.Get(parts[last])
	if !ok {
		child = p.newTable(t, explicit)
		t.Set(parts[last], child)
		return child
	}
	if child.Kind != tree.Table || p.meta[child].state != implicit {
		p.fail("table %q is already defined", strings.Join(parts, "."))
	}
	p.meta[child].state = explicit
	return child
}

// descendHeader follows one prefix part of a header.
func (p *parser) descendHeader(t *tree.Node, key string) *tree.Node {
	child, ok := t.Get(key)
	if !ok {
		child = p.newTable(t, implicit)
		t.Set(key, child)
		return child
	}
	switch {
	case child.Kind == tree.Table && p.meta[child].state != inline:
		return child
	case p.isArrayOfTables(child):
		p.sectionAOT = true
		return child.Items[len(child.Items)-1]
	}
	p.fail("key %q cannot be extended by a header", key)
	return nil
}

// isArrayOfTables tells an array made by [[header]] from a static array, which
// has no metadata because no header can extend it.
func (p *parser) isArrayOfTables(n *tree.Node) bool {
	m := p.meta[n]
	return m != nil && m.aot
}

func (p *parser) appendArrayTable(t *tree.Node, key string) *tree.Node {
	p.sectionAOT = true
	array, ok := t.Get(key)
	if !ok {
		array = p.register(tree.NewArray(), t, explicit)
		p.meta[array].aot = true
		t.Set(key, array)
	} else if !p.isArrayOfTables(array) {
		p.fail("key %q is not an array of tables", key)
	}
	element := p.newTable(array, explicit)
	array.Items = append(array.Items, element)
	return element
}

func (p *parser) keyValue(start int) statement {
	parts := p.key()
	p.skipSpace()
	if p.peek() != '=' {
		p.fail("expected '=' after the key")
	}
	p.pos++
	p.skipSpace()
	st := statement{kind: keyValue, head: p.src[start:p.pos], aot: p.sectionAOT}
	st.path = append(append([]string{}, p.section...), parts...)

	valueStart := p.pos
	v := p.value(p.meta[p.current].depth + len(parts))
	p.assign(p.current, parts, v, valueStart)
	st.value = p.src[valueStart:p.pos]

	tailStart := p.pos
	p.endOfLine()
	st.tail = p.src[tailStart:p.pos]
	return st
}

// assign stores a value under a dotted key inside table t.
func (p *parser) assign(t *tree.Node, parts []string, v *tree.Node, pos int) {
	last := len(parts) - 1
	for _, key := range parts[:last] {
		child, ok := t.Get(key)
		switch {
		case !ok:
			child = p.newTable(t, dotted)
			t.Set(key, child)
		case child.Kind != tree.Table || p.meta[child].state != dotted:
			p.failAt(pos, "key %q cannot be extended by a dotted key", key)
		}
		t = child
	}
	if _, ok := t.Get(parts[last]); ok {
		p.failAt(pos, "duplicate key %q", parts[last])
	}
	t.Set(parts[last], v)
}

// value reads a value that will sit at the given depth.
func (p *parser) value(depth int) *tree.Node {
	if depth > maxDepth {
		p.fail("nesting is deeper than %d levels", maxDepth)
	}
	switch p.peek() {
	case '"', '\'':
		return tree.NewString(p.stringValue())
	case '[':
		return p.array(depth)
	case '{':
		return p.inlineTable(depth)
	}
	return p.scalar()
}

// skipArraySpace consumes white space, newlines and comments between array
// elements.
func (p *parser) skipArraySpace() {
	for {
		p.skipSpace()
		p.comment()
		if !p.newline() {
			return
		}
	}
}

func (p *parser) array(depth int) *tree.Node {
	p.pos++
	array := tree.NewArray()
	for {
		p.skipArraySpace()
		if p.peek() == ']' {
			p.pos++
			return array
		}
		array.Items = append(array.Items, p.value(depth+1))
		p.skipArraySpace()
		if p.peek() == ',' {
			p.pos++
		} else if p.peek() != ']' {
			p.fail("expected ',' or ']' in an array")
		}
	}
}

func (p *parser) inlineTable(depth int) *tree.Node {
	p.pos++
	table := tree.NewTable()
	p.meta[table] = &nodeMeta{state: inline, depth: depth}
	p.skipSpace()
	if p.peek() == '}' {
		p.pos++
		return table
	}
	for {
		parts := p.key()
		p.skipSpace()
		if p.peek() != '=' {
			p.fail("expected '=' in an inline table")
		}
		p.pos++
		p.skipSpace()
		start := p.pos
		v := p.value(depth + len(parts))
		p.assign(table, parts, v, start)
		p.skipSpace()
		switch p.peek() {
		case '}':
			p.pos++
			return table
		case ',':
			p.pos++
			p.skipSpace()
		default:
			p.fail("expected ',' or '}' in an inline table")
		}
	}
}
