// Package yaml reads the plain subset of YAML 1.2 that configuration files and
// front matter use: block and flow collections, the three scalar styles and
// block scalars, with the core schema. Anchors, aliases, tags, directives,
// complex keys and multiple documents are outside it and report
// ErrUnsupported instead of being misread.
package yaml

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

const (
	// maxInputBytes bounds the size of a document; parsers take hostile input.
	maxInputBytes = 8 << 20
	// maxDepth bounds how deep collections may nest.
	maxDepth = 128
)

// ErrUnsupported marks valid YAML that the subset leaves out. Test for it
// with errors.Is.
var ErrUnsupported = errors.New("construct outside the supported YAML subset")

// Error is a syntax error, or an unsupported construct, with its 1-based
// position. Column counts bytes.
type Error struct {
	Line, Column int
	Msg          string
	unsupported  bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("yaml: line %d, column %d: %s", e.Line, e.Column, e.Msg)
}

// Unwrap makes errors.Is(err, ErrUnsupported) work.
func (e *Error) Unwrap() error {
	if e.unsupported {
		return ErrUnsupported
	}
	return nil
}

// Decode reads one document into a value tree. An empty document is a null
// node. A mapping key is kept as its text, so `1: a` has the key "1".
func Decode(data []byte) (root *tree.Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			root, err = nil, r.(*Error) // any other panic is a bug and stays one
		}
	}()
	p := newParser(data)
	return p.document(), nil
}

var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n")

type parser struct {
	src        string
	pos        int
	lineStarts []int
	depth      int
}

func newParser(data []byte) *parser {
	p := &parser{lineStarts: []int{0}}
	if len(data) > maxInputBytes {
		p.fail("the document is larger than %d bytes", maxInputBytes)
	}
	if !utf8.Valid(data) {
		p.fail("invalid UTF-8")
	}
	p.src = lineBreaks.Replace(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))
	for i := range len(p.src) {
		switch c := p.src[i]; {
		case c == '\n':
			p.lineStarts = append(p.lineStarts, i+1)
		case (c < 0x20 && c != '\t') || c == 0x7f:
			p.pos = i
			p.fail("control character")
		}
	}
	return p
}

func (p *parser) lineIndex(pos int) int {
	i, found := slices.BinarySearch(p.lineStarts, pos)
	if !found {
		i--
	}
	return i
}

func (p *parser) raise(unsupported bool, format string, args ...any) {
	line := p.lineIndex(p.pos)
	panic(&Error{
		Line: line + 1, Column: p.pos - p.lineStarts[line] + 1,
		Msg: fmt.Sprintf(format, args...), unsupported: unsupported,
	})
}

func (p *parser) fail(format string, args ...any) { p.raise(false, format, args...) }

func (p *parser) unsupported(what string) { p.raise(true, "%s are not supported", what) }

func (p *parser) eof() bool { return p.pos >= len(p.src) }

// peek returns the next byte, or 0 at the end; the input holds no NUL.
func (p *parser) peek() byte { return p.at(p.pos) }

func (p *parser) at(i int) byte {
	if i >= len(p.src) {
		return 0
	}
	return p.src[i]
}

func (p *parser) col() int { return p.pos - p.lineStarts[p.lineIndex(p.pos)] }

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// isEnd reports white space, a line end or the end of the input.
func isEnd(c byte) bool { return isBlank(c) || c == '\n' || c == 0 }

func isFlowIndicator(c byte) bool { return strings.IndexByte(",[]{}", c) >= 0 && c != 0 }

func (p *parser) skipBlanks() {
	for isBlank(p.peek()) {
		p.pos++
	}
}

// isMarker reports a document marker at the current position.
func (p *parser) isMarker(marker string) bool {
	return p.col() == 0 && strings.HasPrefix(p.src[p.pos:], marker) && isEnd(p.at(p.pos+3))
}

func (p *parser) atAnyMarker() bool { return p.isMarker("---") || p.isMarker("...") }

// skipTrivia moves to the next token over white space, comments and newlines.
func (p *parser) skipTrivia() {
	for {
		p.skipBlanks()
		switch p.peek() {
		case '#':
			if p.pos > 0 && !isEnd(p.src[p.pos-1]) {
				p.fail("a comment must follow white space")
			}
			for !p.eof() && p.peek() != '\n' {
				p.pos++
			}
		case '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) document() *tree.Node {
	p.skipTrivia()
	if p.col() == 0 && p.peek() == '%' {
		p.unsupported("directives")
	}
	if p.isMarker("---") {
		p.pos += 3
		p.skipBlanks()
		if !isEnd(p.peek()) && p.peek() != '#' && (p.atSequenceEntry() || p.looksLikeKey()) {
			p.fail("a block collection cannot start on the document marker's line")
		}
	}
	root := p.blockNode(-1, false)
	p.skipTrivia()
	if p.isMarker("...") {
		p.pos += 3
		p.skipTrivia()
		if !p.eof() {
			p.unsupported("multiple documents")
		}
	}
	if p.isMarker("---") {
		p.unsupported("multiple documents")
	}
	if !p.eof() {
		p.fail("unexpected content")
	}
	return root
}

func (p *parser) enter() {
	p.depth++
	if p.depth > maxDepth {
		p.fail("nesting is deeper than %d levels", maxDepth)
	}
}

// blockNode reads the node that follows, or null when the next token is not
// indented past parent. seqAtParent allows a sequence at the parent's own
// indent, which YAML permits for a mapping's value.
func (p *parser) blockNode(parent int, seqAtParent bool) *tree.Node {
	p.skipTrivia()
	if p.eof() || p.atAnyMarker() {
		return tree.NewNull()
	}
	c := p.col()
	if c > parent && p.spaceIndent() <= parent {
		p.fail("tabs cannot indent a block")
	}
	if c < parent || (c == parent && !(seqAtParent && p.atSequenceEntry())) {
		return tree.NewNull()
	}
	return p.nodeAt(c, parent)
}

func (p *parser) checkIndentTabs() {
	prefix := p.src[p.lineStarts[p.lineIndex(p.pos)]:p.pos]
	if strings.Contains(prefix, "\t") && strings.Trim(prefix, " \t") == "" {
		p.fail("tabs cannot indent a block")
	}
}

// spaceIndent counts the spaces that start the current line. YAML indentation
// is spaces only, so a tab never counts toward it.
func (p *parser) spaceIndent() int {
	n, start := 0, p.lineStarts[p.lineIndex(p.pos)]
	for p.at(start+n) == ' ' {
		n++
	}
	return n
}

func (p *parser) atSequenceEntry() bool { return p.peek() == '-' && isEnd(p.at(p.pos+1)) }

// nodeAt reads a block node whose first token is at the current position,
// in column c.
func (p *parser) nodeAt(c, parent int) *tree.Node {
	p.enter()
	defer func() { p.depth-- }()
	switch ch := p.peek(); {
	case ch == '|' || ch == '>':
		return p.blockScalar(parent)
	case p.atSequenceEntry():
		p.checkIndentTabs()
		return p.sequence(c)
	case ch == '?' && isEnd(p.at(p.pos+1)):
		p.unsupported("complex keys")
	case ch == ':' && isEnd(p.at(p.pos+1)):
		p.unsupported("empty keys")
	}
	p.refuseProperties()
	if p.looksLikeKey() {
		p.checkIndentTabs()
		return p.mapping(c)
	}
	return p.scalarOrFlow(parent)
}

func (p *parser) refuseProperties() {
	switch p.peek() {
	case '&':
		p.unsupported("anchors")
	case '*':
		p.unsupported("aliases")
	case '!':
		p.unsupported("tags")
	}
}

// scalarOrFlow reads a value that must end its line.
func (p *parser) scalarOrFlow(parent int) *tree.Node {
	var node *tree.Node
	switch p.peek() {
	case '[', '{':
		node = p.flow(parent)
		mark := p.pos
		p.skipBlanks()
		if p.peek() == ':' && isEnd(p.at(p.pos+1)) {
			p.unsupported("flow collections as keys")
		}
		p.pos = mark
	case '"', '\'':
		node = tree.NewString(p.quoted(parent))
	default:
		node = resolve(p.plain(parent, false))
	}
	p.endLine()
	return node
}

// endLine checks that nothing but a comment follows on this line.
func (p *parser) endLine() {
	start := p.pos
	p.skipBlanks()
	if p.eof() || p.peek() == '\n' || (p.peek() == '#' && p.pos > start) {
		return
	}
	p.fail("unexpected content after the value")
}

// looksLikeKey reports whether a single-line key and its colon start here.
func (p *parser) looksLikeKey() bool {
	mark := p.pos
	defer func() { p.pos = mark }()
	switch p.peek() {
	case '"':
		if !p.skipQuotedLine('"') {
			return false
		}
	case '\'':
		if !p.skipQuotedLine('\'') {
			return false
		}
	default:
		if !p.plainStart(false) {
			return false
		}
		p.segment(false)
	}
	p.skipBlanks()
	return p.peek() == ':' && isEnd(p.at(p.pos+1))
}

// skipQuotedLine moves past a quoted scalar that closes on this line.
func (p *parser) skipQuotedLine(q byte) bool {
	for p.pos++; !p.eof() && p.peek() != '\n'; p.pos++ {
		switch {
		case q == '"' && p.peek() == '\\':
			p.pos++
		case p.peek() == q && q == '\'' && p.at(p.pos+1) == q:
			p.pos++
		case p.peek() == q:
			p.pos++
			return true
		}
	}
	return false
}

func (p *parser) mapping(c int) *tree.Node {
	m := tree.NewTable()
	for {
		key := p.key()
		if _, dup := m.Get(key); dup {
			p.fail("duplicate key %q", key)
		}
		m.Set(key, p.mapValue(c))
		p.skipTrivia()
		if p.eof() || p.atAnyMarker() || p.col() < c {
			return m
		}
		if p.col() > c {
			p.fail("bad indentation of a mapping entry")
		}
		if p.atSequenceEntry() {
			return m
		}
		switch {
		case p.peek() == '?' && isEnd(p.at(p.pos+1)):
			p.unsupported("complex keys")
		case p.peek() == ':' && isEnd(p.at(p.pos+1)):
			p.unsupported("empty keys")
		}
		p.refuseProperties()
		if !p.looksLikeKey() {
			p.fail("expected a mapping key")
		}
	}
}

// key reads a key that looksLikeKey accepted, and its colon.
func (p *parser) key() string {
	var text string
	if q := p.peek(); q == '"' || q == '\'' {
		text = p.quoted(-1)
	} else {
		text = p.segment(false)
	}
	p.skipBlanks()
	p.pos++
	return text
}

func (p *parser) mapValue(c int) *tree.Node {
	p.skipBlanks()
	switch ch := p.peek(); {
	case p.eof() || ch == '\n' || ch == '#':
		return p.blockNode(c, true)
	case ch == '|' || ch == '>':
		return p.blockScalar(c)
	case p.atSequenceEntry():
		p.fail("a sequence cannot start on the line of its key")
	}
	p.refuseProperties()
	return p.scalarOrFlow(c)
}

func (p *parser) sequence(c int) *tree.Node {
	seq := tree.NewArray()
	for {
		p.pos++ // the dash
		separator := p.pos
		p.skipBlanks()
		if strings.Contains(p.src[separator:p.pos], "\t") && (p.atSequenceEntry() || p.looksLikeKey()) {
			p.fail("a tab cannot separate a compact collection from its dash")
		}
		var item *tree.Node
		if ch := p.peek(); p.eof() || ch == '\n' || ch == '#' {
			item = p.blockNode(c, false)
		} else {
			item = p.nodeAt(p.col(), c)
		}
		seq.Items = append(seq.Items, item)
		p.skipTrivia()
		if p.eof() || p.atAnyMarker() || p.col() < c {
			return seq
		}
		if p.col() > c {
			p.fail("bad indentation of a sequence entry")
		}
		if !p.atSequenceEntry() {
			return seq
		}
	}
}

// plainStart reports whether a plain scalar can start here.
func (p *parser) plainStart(flow bool) bool {
	c := p.peek()
	switch c {
	case ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`', 0:
		return false
	case '-', '?', ':':
		next := p.at(p.pos + 1)
		return !isEnd(next) && !(flow && isFlowIndicator(next))
	}
	return true
}

// segment reads the plain text left on this line, without trailing blanks.
func (p *parser) segment(flow bool) string {
	start, end := p.pos, p.pos
	for i := p.pos; i < len(p.src); i++ {
		c := p.src[i]
		if c == '\n' || (flow && isFlowIndicator(c)) {
			break
		}
		if c == ':' && (isEnd(p.at(i+1)) || (flow && isFlowIndicator(p.at(i+1)))) {
			break
		}
		if c == '#' && i > start && isBlank(p.src[i-1]) {
			break
		}
		if !isBlank(c) {
			end = i + 1
		}
	}
	p.pos = end
	return p.src[start:end]
}

// plain reads a plain scalar, folding its continuation lines.
func (p *parser) plain(parent int, flow bool) string {
	if !p.plainStart(flow) {
		p.fail("a plain scalar cannot start with %q", string(p.peek()))
	}
	var b strings.Builder
	b.WriteString(p.segment(flow))
	for {
		mark := p.pos
		breaks := p.nextContinuation(parent)
		if breaks == 0 {
			p.pos = mark
			return b.String()
		}
		text := p.segment(flow)
		if text == "" {
			p.pos = mark
			return b.String()
		}
		writeFold(&b, breaks)
		b.WriteString(text)
	}
}

// nextContinuation moves to the next line of a plain scalar and returns how
// many line breaks it crossed, or 0 when the scalar ends here.
func (p *parser) nextContinuation(parent int) int {
	p.skipBlanks()
	breaks := 0
	for p.peek() == '\n' {
		p.pos++
		breaks++
		p.skipBlanks()
	}
	if p.eof() || p.peek() == '#' || p.atAnyMarker() || p.spaceIndent() <= parent {
		return 0
	}
	return breaks
}

// writeFold applies line folding: one break is a space, n breaks are n-1
// newlines.
func writeFold(b *strings.Builder, breaks int) {
	if breaks == 1 {
		b.WriteByte(' ')
		return
	}
	b.WriteString(strings.Repeat("\n", breaks-1))
}

// quoted reads a single- or double-quoted scalar that may span lines, each
// continuation indented past parent.
func (p *parser) quoted(parent int) string {
	q := p.peek()
	p.pos++
	var b strings.Builder
	// A run of blanks is held back until it is known not to end a line, where
	// it would be dropped; a builder cannot be shortened in place.
	blanks := -1
	flush := func() {
		if blanks >= 0 {
			b.WriteString(p.src[blanks:p.pos])
			blanks = -1
		}
	}
	for {
		switch c := p.peek(); {
		case p.eof():
			p.fail("unterminated quoted scalar")
		case c == q && q == '\'' && p.at(p.pos+1) == q:
			flush()
			b.WriteByte(q)
			p.pos += 2
		case c == q:
			flush()
			p.pos++
			return b.String()
		case c == '\\' && q == '"':
			flush()
			p.escape(&b)
		case c == '\n':
			blanks = -1
			writeFold(&b, p.quotedBreaks(parent))
		case isBlank(c):
			if blanks < 0 {
				blanks = p.pos
			}
			p.pos++
		default:
			flush()
			b.WriteByte(c)
			p.pos++
		}
	}
}

// quotedBreaks crosses the line breaks inside a quoted scalar and the
// indentation after them.
func (p *parser) quotedBreaks(parent int) int {
	breaks := 0
	for p.peek() == '\n' {
		p.pos++
		breaks++
		p.skipBlanks()
	}
	if p.atAnyMarker() {
		p.fail("a document marker inside a quoted scalar")
	}
	if !p.eof() && p.spaceIndent() <= parent {
		p.fail("a quoted scalar's continuation must be indented")
	}
	return breaks
}

var shortEscapes = map[byte]string{
	'0': "\x00", 'a': "\a", 'b': "\b", 't': "\t", '\t': "\t", 'n': "\n", 'v': "\v", 'f': "\f",
	'r': "\r", 'e': "\x1b", ' ': " ", '"': "\"", '/': "/", '\\': "\\",
	'N': "\u0085", '_': " ", 'L': " ", 'P': " ",
}

func (p *parser) escape(b *strings.Builder) {
	p.pos++
	c := p.peek()
	if c == '\n' {
		// An escaped line break joins the lines without a space.
		p.pos++
		for p.peek() == '\n' || isBlank(p.peek()) {
			if p.peek() == '\n' {
				b.WriteByte('\n')
			}
			p.pos++
		}
		return
	}
	if s, ok := shortEscapes[c]; ok && !p.eof() {
		b.WriteString(s)
		p.pos++
		return
	}
	switch c {
	case 'x':
		p.hexEscape(b, 2)
	case 'u':
		p.hexEscape(b, 4)
	case 'U':
		p.hexEscape(b, 8)
	default:
		p.fail("invalid escape sequence")
	}
}

func (p *parser) hexEscape(b *strings.Builder, digits int) {
	start := p.pos + 1
	end := min(start+digits, len(p.src))
	v, err := strconv.ParseUint(p.src[start:end], 16, 32)
	if err != nil || end-start != digits || strings.ContainsAny(p.src[start:end], "+-_") {
		p.fail("invalid hexadecimal escape")
	}
	if v > utf8.MaxRune || !utf8.ValidRune(rune(v)) {
		p.fail("escape is not a unicode scalar value")
	}
	b.WriteRune(rune(v))
	p.pos = end
}

// blockHeader is the indicator line of a block scalar.
type blockHeader struct {
	folded bool
	chomp  byte // '+', '-' or 0
	indent int  // 0 means detect from the first line
}

func (p *parser) readBlockHeader() blockHeader {
	h := blockHeader{folded: p.peek() == '>'}
	p.pos++
	for range 2 {
		switch c := p.peek(); {
		case c == '+' || c == '-':
			if h.chomp != 0 {
				p.fail("two chomping indicators")
			}
			h.chomp = c
			p.pos++
		case c >= '1' && c <= '9':
			if h.indent != 0 {
				p.fail("two indentation indicators")
			}
			h.indent = int(c - '0')
			p.pos++
		case c == '0':
			p.fail("the indentation indicator cannot be zero")
		}
	}
	p.endLine()
	for !p.eof() && p.peek() != '\n' {
		p.pos++
	}
	p.pos++ // the line break, or past the end, which eof treats alike
	return h
}

func (p *parser) blockScalar(parent int) *tree.Node {
	h := p.readBlockHeader()
	lines, finalBreak := p.blockLines(h, parent)
	last := len(lines) - 1
	for last >= 0 && lines[last] == "" {
		last--
	}
	body, trailing := lines[:last+1], len(lines)-1-last
	var text string
	if h.folded {
		text = foldLines(body)
	} else {
		text = strings.Join(body, "\n")
	}
	hasBreak := len(body) > 0 && (finalBreak || trailing > 0)
	switch {
	case h.chomp == '+' && hasBreak:
		text += strings.Repeat("\n", trailing+1)
	case h.chomp == '+':
		text += strings.Repeat("\n", trailing)
	case h.chomp == 0 && hasBreak:
		text += "\n"
	}
	return tree.NewString(text)
}

// blockLines reads the content lines of a block scalar, each with its
// indentation removed, and reports whether the last one ended in a break.
func (p *parser) blockLines(h blockHeader, parent int) ([]string, bool) {
	indent, detected := max(parent, 0)+h.indent, h.indent != 0
	var lines []string
	widestBlank, finalBreak := 0, true
	for p.pos < len(p.src) {
		start := p.pos
		n := 0
		for p.at(start+n) == ' ' {
			n++
		}
		end := len(p.src)
		if i := strings.IndexByte(p.src[start+n:], '\n'); i >= 0 {
			end = start + n + i
		}
		blank := start+n == end
		if !detected {
			if blank {
				widestBlank = max(widestBlank, n)
			} else {
				if n <= parent {
					if strings.Trim(p.src[start+n:end], " \t") == "" {
						p.fail("a tab cannot indent a block scalar")
					}
					break
				}
				if widestBlank > n {
					p.fail("a leading blank line is wider than the content")
				}
				indent, detected = n, true
			}
		}
		if !blank && (n < indent || (n == 0 && p.atAnyMarker())) {
			break
		}
		if blank && n <= indent || blank && !detected {
			lines = append(lines, "")
		} else {
			lines = append(lines, p.src[start+indent:end])
		}
		p.pos = end + 1
		// A last line of only spaces counts as ended, as the official suite
		// expects (case L24T).
		finalBreak = end < len(p.src) || blank
	}
	p.pos = min(p.pos, len(p.src))
	return lines, finalBreak
}

// foldLines joins the lines of a folded scalar: a single break between two
// plain lines is a space, and lines that start with white space keep theirs.
func foldLines(lines []string) string {
	var b strings.Builder
	breaks, started, prevMore := 0, false, false
	for _, line := range lines {
		if line == "" {
			breaks++
			continue
		}
		more := isBlank(line[0])
		switch {
		case !started:
			b.WriteString(strings.Repeat("\n", breaks))
		case prevMore || more:
			b.WriteString(strings.Repeat("\n", breaks+1))
		case breaks == 0:
			b.WriteByte(' ')
		default:
			b.WriteString(strings.Repeat("\n", breaks))
		}
		b.WriteString(line)
		started, prevMore, breaks = true, more, 0
	}
	return b.String()
}

// flow reads a flow collection.
func (p *parser) flow(parent int) *tree.Node {
	p.enter()
	defer func() { p.depth-- }()
	if p.peek() == '[' {
		return p.flowSequence(parent)
	}
	return p.flowMapping(parent)
}

// flowSkip moves over white space and comments inside a flow collection.
func (p *parser) flowSkip(parent int) {
	line := p.lineIndex(p.pos)
	p.skipTrivia()
	if p.lineIndex(p.pos) == line {
		return
	}
	if p.atAnyMarker() {
		p.fail("a document marker inside a flow collection")
	}
	if !p.eof() && p.spaceIndent() <= parent {
		p.fail("a flow collection's continuation must be indented")
	}
}

// flowItem is one node of a flow collection; text is its source for a scalar.
type flowItem struct {
	node     *tree.Node
	text     string
	scalar   bool
	jsonLike bool // a value after which ':' needs no space
}

func (p *parser) flowNode(parent int) flowItem {
	switch c := p.peek(); {
	case c == '[' || c == '{':
		return flowItem{node: p.flow(parent), jsonLike: true}
	case c == '"' || c == '\'':
		text := p.quoted(parent)
		return flowItem{node: tree.NewString(text), text: text, scalar: true, jsonLike: true}
	case c == '?' && (isEnd(p.at(p.pos+1)) || isFlowIndicator(p.at(p.pos+1))):
		p.unsupported("complex keys")
	case c == ':' && (isEnd(p.at(p.pos+1)) || isFlowIndicator(p.at(p.pos+1))):
		p.unsupported("empty keys")
	}
	p.refuseProperties()
	text := p.plain(parent, true)
	return flowItem{node: resolve(text), text: text, scalar: true}
}

// flowColon reports a value indicator after item.
func (p *parser) flowColon(item flowItem) bool {
	if p.peek() != ':' {
		return false
	}
	next := p.at(p.pos + 1)
	if !item.jsonLike && !isEnd(next) && !isFlowIndicator(next) {
		return false
	}
	if !item.scalar {
		p.unsupported("flow collections as keys")
	}
	return true
}

// flowValue reads the value after a colon, which may be left out.
func (p *parser) flowValue(parent int) *tree.Node {
	p.pos++
	p.flowSkip(parent)
	if c := p.peek(); c == ',' || c == ']' || c == '}' {
		return tree.NewNull()
	}
	return p.flowNode(parent).node
}

// flowSeparator consumes a comma, or stops before the closer.
func (p *parser) flowSeparator(parent int, closer byte) {
	p.flowSkip(parent)
	switch p.peek() {
	case ',':
		p.pos++
	case closer:
	default:
		p.fail("expected ',' or %q in a flow collection", string(closer))
	}
}

func (p *parser) flowSequence(parent int) *tree.Node {
	p.pos++
	seq := tree.NewArray()
	for {
		p.flowSkip(parent)
		if p.peek() == ']' {
			p.pos++
			return seq
		}
		line := p.lineIndex(p.pos)
		item := p.flowNode(parent)
		p.flowSkip(parent)
		node := item.node
		if p.flowColon(item) {
			if p.lineIndex(p.pos) != line {
				p.fail("a single-pair key must fit on one line")
			}
			node = tree.NewTable()
			node.Set(item.text, p.flowValue(parent))
		}
		seq.Items = append(seq.Items, node)
		p.flowSeparator(parent, ']')
	}
}

func (p *parser) flowMapping(parent int) *tree.Node {
	p.pos++
	m := tree.NewTable()
	for {
		p.flowSkip(parent)
		if p.peek() == '}' {
			p.pos++
			return m
		}
		item := p.flowNode(parent)
		p.flowSkip(parent)
		value := tree.NewNull()
		if p.flowColon(item) {
			value = p.flowValue(parent)
		} else if !item.scalar {
			p.unsupported("flow collections as keys")
		}
		if _, dup := m.Get(item.text); dup {
			p.fail("duplicate key %q", item.text)
		}
		m.Set(item.text, value)
		p.flowSeparator(parent, '}')
	}
}

// resolve applies the core schema to a plain scalar.
func resolve(text string) *tree.Node {
	switch text {
	case "", "~", "null", "Null", "NULL":
		return tree.NewNull()
	case "true", "True", "TRUE":
		return tree.NewBool(true)
	case "false", "False", "FALSE":
		return tree.NewBool(false)
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return tree.NewFloat(math.Inf(1))
	case "-.inf", "-.Inf", "-.INF":
		return tree.NewFloat(math.Inf(-1))
	case ".nan", ".NaN", ".NAN":
		return tree.NewFloat(math.NaN())
	}
	if v, ok := parseInt(text); ok {
		return tree.NewInt(v)
	}
	if isFloat(text) {
		if v, err := strconv.ParseFloat(text, 64); err == nil {
			return tree.NewFloat(v)
		}
	}
	return tree.NewString(text)
}

func parseInt(text string) (int64, bool) {
	base, digits := 10, text
	switch {
	case strings.HasPrefix(text, "0o"):
		base, digits = 8, text[2:]
	case strings.HasPrefix(text, "0x"):
		base, digits = 16, text[2:]
	}
	if base != 10 && (digits == "" || digits[0] == '+' || digits[0] == '-') {
		return 0, false
	}
	v, err := strconv.ParseInt(digits, base, 64)
	return v, err == nil
}

func countDigits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// isFloat matches the core schema's float form, which integers too large for
// an int64 also take.
func isFloat(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-")
	whole := countDigits(s)
	s = s[whole:]
	fraction := 0
	if strings.HasPrefix(s, ".") {
		fraction = countDigits(s[1:])
		s = s[1+fraction:]
	}
	if whole+fraction == 0 {
		return false
	}
	if s == "" {
		return true
	}
	if s[0] != 'e' && s[0] != 'E' {
		return false
	}
	s = strings.TrimLeft(s[1:], "+-")
	return s != "" && countDigits(s) == len(s)
}
