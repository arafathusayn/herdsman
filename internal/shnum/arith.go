package shnum

import (
	"strconv"
	"strings"
)

// Eval evaluates an arithmetic expression as bash 3.2's $(( )) does (the
// /bin/bash of macOS, which runs the skill's scripts; bash 4 and 5 agree on
// everything the scripts can meet): integer literals in decimal, octal
// (leading 0), hexadecimal (0x) and base#digits (bases 2 to 64), 64-bit
// two's-complement wraparound, the unary operators + - ! ~ ++ --, the
// binary operators ** * / % + - << >> < <= > >= == != & ^ | && ||, ?:,
// the assignment operators, the comma and parentheses, with bash's
// precedence and associativity.
//
// A name takes its value from lookup: unset or empty is 0, any other value
// is evaluated as an expression of its own (so FOO=BAR and BAR=5 give 5),
// up to bash's recursion limit of 1024. An assignment is seen by the rest
// of the expression only, never by lookup's source: the expansion runs as
// in a subshell. As in bash 3.2, an error (a division by zero, a digit too
// great for its base, a syntax error) is an error even in the branch that
// && , || or ?: does not take; only names and assignments are skipped
// there.
func Eval(expr string, lookup func(string) (string, bool)) (int64, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	e := &evaluator{lookup: lookup, vars: map[string]string{}}
	return e.eval(expr)
}

// Error is an arithmetic error, worded as bash words it.
type Error struct {
	Expr string // the expression being evaluated
	Msg  string // what bash prints after it, e.g. "division by 0"
}

func (e *Error) Error() string { return e.Expr + ": " + e.Msg }

const maxDepth = 1024 // bash's MAX_EXPR_RECURSION_LEVEL

type evaluator struct {
	lookup func(string) (string, bool)
	vars   map[string]string // assignments made by this expansion
	depth  int
	noeval int
}

// Tokens besides the single-character operators, which stand for
// themselves.
const (
	tEOF = iota + 256
	tNum
	tStr
	tEqEq
	tNeq
	tLeq
	tGeq
	tLsh
	tRsh
	tLand
	tLor
	tPower
	tPreInc
	tPreDec
	tPostInc
	tPostDec
	tOpAssign
)

type parser struct {
	e        *evaluator
	src      string
	pos      int
	tok      int
	lasttok  int
	tokval   int64
	tokstr   string // the name of the last tStr
	assignOp int    // the operator of a tOpAssign
}

// panic(errStop) carries an arithmetic error up to eval.
type errStop struct{ err *Error }

func (e *evaluator) eval(expr string) (v int64, err error) {
	if strings.Trim(expr, " \t\n") == "" {
		return 0, nil
	}
	e.depth++
	defer func() { e.depth-- }()
	p := &parser{e: e, src: expr}
	defer func() {
		if r := recover(); r != nil {
			v, err = 0, r.(errStop).err // any other panic is a bug: it panics again here
		}
	}()
	if e.depth > maxDepth {
		p.fail("expression recursion level exceeded")
	}
	p.readtok()
	v = p.comma()
	if p.tok != tEOF {
		p.fail("syntax error in expression")
	}
	return v, nil
}

func (p *parser) fail(msg string) {
	panic(errStop{&Error{Expr: p.src, Msg: msg}})
}

func isStarter(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isStarter(c) || (c >= '0' && c <= '9') }

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func (p *parser) at(i int) byte {
	if i < len(p.src) {
		return p.src[i]
	}
	return 0
}

// readtok reads the next token, as bash's readtok does.
func (p *parser) readtok() {
	for p.pos < len(p.src) && isBlank(p.src[p.pos]) {
		p.pos++
	}
	p.lasttok = p.tok
	if p.pos >= len(p.src) {
		p.tok = tEOF
		return
	}
	c := p.src[p.pos]
	switch {
	case isStarter(c):
		start := p.pos
		for p.pos < len(p.src) && isNameChar(p.src[p.pos]) {
			p.pos++
		}
		name, sub, hasSub := p.src[start:p.pos], "", false
		if p.at(p.pos) == '[' {
			hasSub = true
			end := strings.IndexByte(p.src[p.pos:], ']')
			if end < 0 {
				p.fail("bad array subscript")
			}
			sub = p.src[p.pos+1 : p.pos+end]
			p.pos += end + 1
		}
		// Peek: a name followed by = is an assignment target, not read.
		save := *p
		p.e.noeval++
		p.readtok()
		peek := p.tok
		p.e.noeval--
		*p = save
		p.tokstr = name
		if save.tok == tPreInc || save.tok == tPreDec || peek != '=' {
			p.tokval = p.value(name, sub, hasSub)
		} else {
			p.tokval = 0
		}
		p.lasttok, p.tok = save.tok, tStr
		return
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.src) {
			d := p.src[p.pos]
			if !isNameChar(d) && d != '#' && d != '@' {
				break
			}
			p.pos++
		}
		p.tokval = p.number(p.src[start:p.pos])
		p.tok = tNum
		return
	}
	c1 := p.at(p.pos + 1)
	two := map[[2]byte]int{{'=', '='}: tEqEq, {'!', '='}: tNeq, {'>', '='}: tGeq, {'<', '='}: tLeq, {'<', '<'}: tLsh, {'>', '>'}: tRsh,
		{'&', '&'}: tLand, {'|', '|'}: tLor, {'*', '*'}: tPower}
	p.pos++
	if t, ok := two[[2]byte{c, c1}]; ok {
		p.pos++
		if (t == tLsh || t == tRsh) && p.at(p.pos) == '=' {
			p.pos++
			p.tok, p.assignOp = tOpAssign, t
			return
		}
		p.tok = t
		return
	}
	if (c == '-' || c == '+') && c1 == c {
		if p.tok == tStr {
			p.pos++
			p.tok = map[byte]int{'-': tPostDec, '+': tPostInc}[c]
			return
		}
		i := p.pos + 1
		for i < len(p.src) && isBlank(p.src[i]) {
			i++
		}
		if isStarter(p.at(i)) {
			p.pos++
			p.tok = map[byte]int{'-': tPreDec, '+': tPreInc}[c]
			return
		}
		p.tok = int(c) // two unary signs: read one now
		return
	}
	if c1 == '=' && strings.IndexByte("*/%+-&^|", c) >= 0 {
		p.pos++
		p.tok, p.assignOp = tOpAssign, int(c)
		return
	}
	p.tok = int(c)
}

// number is bash's strlong: a literal with its base.
func (p *parser) number(s string) int64 {
	var val int64
	base, foundBase, i := int64(10), false, 0
	if s[0] == '0' {
		i = 1
		if len(s) == 1 {
			return 0
		}
		if s[1] == 'x' || s[1] == 'X' {
			base, i = 16, 2
		} else {
			base = 8
		}
		foundBase = true
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c == '#' {
			if foundBase {
				p.fail("invalid number")
			}
			if val < 2 || val > 64 {
				p.fail("invalid arithmetic base")
			}
			base, val, foundBase = val, 0, true
			continue
		}
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'z':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			if base <= 36 {
				d = int64(c-'A') + 10
			} else {
				d = int64(c-'A') + 36
			}
		case c == '@':
			d = 62
		default: // '_'
			d = 63
		}
		if d >= base {
			p.fail("value too great for base")
		}
		val = val*base + d
	}
	return val
}

// value is a name's value: 0 when unset or empty, else its own expression.
// Under noeval (a branch not taken) a name is not read.
func (p *parser) value(name, sub string, hasSub bool) int64 {
	if p.e.noeval > 0 {
		return 0
	}
	if hasSub {
		idx, err := p.e.eval(sub)
		if err != nil {
			panic(errStop{err.(*Error)})
		}
		if idx != 0 { // the environment holds scalars: element 0 only
			return 0
		}
	}
	v, ok := p.e.vars[name]
	if !ok {
		v, ok = p.e.lookup(name)
	}
	if !ok || v == "" {
		return 0
	}
	n, err := p.e.eval(v)
	if err != nil {
		panic(errStop{err.(*Error)})
	}
	return n
}

func (p *parser) assign(name string, v int64) {
	if p.e.noeval == 0 {
		p.e.vars[name] = strconv.FormatInt(v, 10)
	}
}

func (p *parser) comma() int64 {
	v := p.assignment()
	for p.tok == ',' {
		p.readtok()
		v = p.assignment()
	}
	return v
}

func (p *parser) assignment() int64 {
	v := p.cond()
	if p.tok != '=' && p.tok != tOpAssign {
		return v
	}
	if p.lasttok != tStr {
		p.fail("attempted assignment to non-variable")
	}
	name, op, lval := p.tokstr, p.assignOp, p.tokval
	isOp := p.tok == tOpAssign
	p.readtok()
	r := p.assignment()
	if isOp {
		r = p.binary(op, lval, r)
	}
	p.assign(name, r)
	return r
}

func (p *parser) cond() int64 {
	c := p.lor()
	if p.tok != '?' {
		return c
	}
	p.readtok()
	if p.tok == tEOF || p.tok == ':' {
		p.fail("expression expected")
	}
	if c == 0 {
		p.e.noeval++
	}
	v1 := p.comma()
	if c == 0 {
		p.e.noeval--
	}
	if p.tok != ':' {
		p.fail("`:' expected for conditional expression")
	}
	p.readtok()
	if p.tok == tEOF {
		p.fail("expression expected")
	}
	if c != 0 {
		p.e.noeval++
	}
	v2 := p.cond()
	if c != 0 {
		p.e.noeval--
	}
	if c != 0 {
		return v1
	}
	return v2
}

func (p *parser) lor() int64 {
	v := p.land()
	for p.tok == tLor {
		p.readtok()
		if v != 0 {
			p.e.noeval++
		}
		v2 := p.land()
		if v != 0 {
			p.e.noeval--
		}
		v = b2i(v != 0 || v2 != 0)
	}
	return v
}

func (p *parser) land() int64 {
	v := p.level(0)
	for p.tok == tLand {
		p.readtok()
		if v == 0 {
			p.e.noeval++
		}
		v2 := p.level(0)
		if v == 0 {
			p.e.noeval--
		}
		v = b2i(v != 0 && v2 != 0)
	}
	return v
}

// levels are the left-associative binary levels, loosest first.
var levels = [][]int{{'|'}, {'^'}, {'&'}, {tEqEq, tNeq}, {'<', '>', tLeq, tGeq}, {tLsh, tRsh}, {'+', '-'}, {'*', '/', '%'}}

func (p *parser) level(i int) int64 {
	if i == len(levels) {
		return p.power()
	}
	v := p.level(i + 1)
	for contains(levels[i], p.tok) {
		op := p.tok
		p.readtok()
		v = p.binary(op, v, p.level(i+1))
	}
	return v
}

func contains(ops []int, t int) bool {
	for _, o := range ops {
		if o == t {
			return true
		}
	}
	return false
}

// power is right-associative: 2**3**2 is 2**9.
func (p *parser) power() int64 {
	v := p.unary()
	if p.tok == tPower {
		p.readtok()
		v = p.binary(tPower, v, p.power())
	}
	return v
}

func (p *parser) unary() int64 {
	switch p.tok {
	case '!':
		p.readtok()
		return b2i(p.unary() == 0)
	case '~':
		p.readtok()
		return ^p.unary()
	case '-':
		p.readtok()
		return -p.unary()
	case '+':
		p.readtok()
		return p.unary()
	}
	return p.operand()
}

func (p *parser) operand() int64 {
	switch p.tok {
	case tPreInc, tPreDec:
		d := map[int]int64{tPreInc: 1, tPreDec: -1}[p.tok]
		p.readtok() // the lexer made ++ or -- a prefix only before a name
		v := p.tokval + d
		p.assign(p.tokstr, v)
		p.readtok()
		return v
	case '(':
		p.readtok()
		v := p.comma()
		if p.tok != ')' {
			p.fail("missing `)'")
		}
		p.readtok()
		return v
	case tNum:
		v := p.tokval
		p.readtok()
		return v
	case tStr:
		v, name := p.tokval, p.tokstr
		p.readtok()
		if p.tok == tPostInc || p.tok == tPostDec {
			p.assign(name, v+map[int]int64{tPostInc: 1, tPostDec: -1}[p.tok])
			p.readtok()
		}
		return v
	}
	p.fail("syntax error: operand expected")
	return 0
}

func (p *parser) binary(op int, a, b int64) int64 {
	switch op {
	case '*':
		return a * b
	case '/', '%':
		if b == 0 {
			p.fail("division by 0")
		}
		if op == '/' {
			return a / b
		}
		return a % b
	case '+':
		return a + b
	case '-':
		return a - b
	case tLsh:
		return a << (uint64(b) & 63)
	case tRsh:
		return a >> (uint64(b) & 63)
	case '<':
		return b2i(a < b)
	case '>':
		return b2i(a > b)
	case tLeq:
		return b2i(a <= b)
	case tGeq:
		return b2i(a >= b)
	case tEqEq:
		return b2i(a == b)
	case tNeq:
		return b2i(a != b)
	case '&':
		return a & b
	case '^':
		return a ^ b
	case '|':
		return a | b
	default: // tPower
		if b < 0 {
			p.fail("exponent less than 0")
		}
		r := int64(1) // by squaring: the same wrapped product as bash's loop
		for ; b > 0; b >>= 1 {
			if b&1 == 1 {
				r *= a
			}
			a *= a
		}
		return r
	}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
