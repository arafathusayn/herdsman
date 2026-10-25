package checkpoint

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// The transcript reader streams: it finds the start of the last 400 lines
// with a backward scan through a fixed buffer, then reads each line once
// through a fixed buffer, keeping only the few fields the reading needs
// (short strings, and numbers reduced as they are read). A record of any
// size costs the same memory, and its usage still counts.

const (
	scanBuf  = 64 << 10
	maxDepth = 10000
	// keepRaw bounds the raw spelling of a kept string. Every wanted key and
	// value is ASCII of at most 40 bytes, and each ASCII character takes at
	// most 6 raw bytes (\uXXXX), so a longer spelling can never equal one:
	// it is checked to its end but not kept.
	keepRaw = 6 * 64
)

// lastLinesStart is the offset of the first of the last n lines of f.
func lastLinesStart(f *os.File, size int64, n int) (int64, error) {
	buf := make([]byte, scanBuf)
	pos := size
	seen := 0
	first := true
	for pos > 0 {
		step := min(int64(scanBuf), pos)
		pos -= step
		if _, err := f.ReadAt(buf[:step], pos); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for i := step - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				first = false
				continue
			}
			if first {
				// The newline that ends the file ends the last line.
				first = false
				continue
			}
			seen++
			if seen == n {
				return pos + i + 1, nil
			}
		}
	}
	return 0, nil
}

// errLine is a line that is not exactly one JSON value.
var errLine = errors.New("not one JSON value")

// capture is a kept field: its JSON kind ('s' string, 'n' number, 't', 'f',
// 'z' null, 'o' object, 'a' array), a short string, a number's count.
type capture struct {
	kind  byte
	str   string
	long  bool
	count int64
	ok    bool // the number is a whole non-negative int64
}

// A path is kept encoded, each member name as <length>:<name>, so a member
// named "message.usage" or "" is never taken for a nested field, and the
// encoding of a field starts with the encoding of each of its parents.
func segment(name string) string { return strconv.Itoa(len(name)) + ":" + name }

func encode(dotted string) string {
	if dotted == "" {
		return ""
	}
	var b strings.Builder
	for s := range strings.SplitSeq(dotted, ".") {
		b.WriteString(segment(s))
	}
	return b.String()
}

// fieldNames are the fields the reading needs ("" is the line's value).
var fieldNames = []string{"", "type", "isSidechain", "message", "message.usage",
	"message.usage.input_tokens", "message.usage.cache_creation_input_tokens", "message.usage.cache_read_input_tokens",
	"payload", "payload.type", "payload.info", "payload.info.last_token_usage", "payload.info.last_token_usage.input_tokens"}

// wanted maps an encoded path to its field name; parents are the encoded
// paths whose members can be wanted.
var wanted, parents = func() (map[string]string, map[string]bool) {
	w, p := map[string]string{}, map[string]bool{}
	for _, n := range fieldNames {
		w[encode(n)] = n
		if i := strings.LastIndexByte(n, '.'); i >= 0 {
			p[encode(n[:i])] = true
		} else if n != "" {
			p[""] = true
		}
	}
	return w, p
}()

type lineReader struct {
	r    *bufio.Reader
	got  map[string]capture // by field name
	done bool               // the file ended
	eol  bool               // the line's newline was read
}

func (l *lineReader) peek() (byte, error) {
	b, err := l.r.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// byte reads one byte and notes a newline, so a parse that stops on it
// never skips the next line.
func (l *lineReader) byte() (byte, error) {
	b, err := l.r.ReadByte()
	if err == nil && b == '\n' {
		l.eol = true
	}
	return b, err
}

// space skips blanks inside a line; a newline ends the line, so it is not
// a blank here.
func (l *lineReader) space() {
	for {
		b, err := l.peek()
		if err != nil || (b != ' ' && b != '\t' && b != '\r') {
			return
		}
		_, _ = l.byte()
	}
}

// rest skips to the end of the line, unless its newline was already read.
func (l *lineReader) rest() {
	for !l.eol {
		if _, err := l.byte(); err != nil {
			l.done = true
			return
		}
	}
}

// line reads one line: whether it held anything, and errLine when it is
// not exactly one JSON value.
func (l *lineReader) line() (bool, error) {
	l.got = map[string]capture{}
	l.eol = false
	l.space()
	b, err := l.peek()
	if err != nil {
		l.done = true
		return false, nil
	}
	if b == '\n' {
		_, _ = l.byte()
		return false, nil
	}
	if err := l.value("", true, 0); err != nil {
		l.rest()
		return true, errLine
	}
	l.space()
	b, err = l.byte()
	switch {
	case err != nil:
		l.done = true
	case b != '\n':
		l.rest()
		return true, errLine
	}
	return true, nil
}

// keep records the value that starts at a wanted path, and drops what an
// earlier value at that path left below it (a repeated member replaces the
// whole earlier one, as a JSON reader does).
func (l *lineReader) keep(path string, c capture) {
	name, ok := wanted[path]
	if !ok {
		return
	}
	for k := range l.got {
		if k != name && (name == "" || strings.HasPrefix(k, name+".")) {
			delete(l.got, k)
		}
	}
	l.got[name] = c
}

// value reads one value at path (tracked: the path can hold a wanted field).
func (l *lineReader) value(path string, tracked bool, depth int) error {
	if depth > maxDepth {
		return errLine
	}
	l.space()
	b, err := l.peek()
	if err != nil {
		return errLine
	}
	keep := func(c capture) {
		if tracked {
			l.keep(path, c)
		}
	}
	switch {
	case b == '{':
		keep(capture{kind: 'o'})
		return l.object(path, tracked && parents[path], depth)
	case b == '[':
		keep(capture{kind: 'a'})
		return l.array(depth)
	case b == '"':
		s, long, err := l.str(tracked)
		keep(capture{kind: 's', str: s, long: long})
		return err
	case b == '-' || isDigit(b):
		n, ok, err := l.number()
		keep(capture{kind: 'n', count: n, ok: ok})
		return err
	}
	for _, lit := range []string{"true", "false", "null"} {
		if b == lit[0] {
			for i := range len(lit) {
				if c, err := l.byte(); err != nil || c != lit[i] {
					return errLine
				}
			}
			keep(capture{kind: map[byte]byte{'t': 't', 'f': 'f', 'n': 'z'}[b]})
			return nil
		}
	}
	return errLine
}

// object reads an object at path; its members are tracked when the path is
// a parent of wanted fields.
func (l *lineReader) object(path string, tracked bool, depth int) error {
	_, _ = l.byte() // {
	l.space()
	if b, err := l.peek(); err == nil && b == '}' {
		_, _ = l.byte()
		return nil
	}
	for {
		l.space()
		if b, err := l.peek(); err != nil || b != '"' {
			return errLine
		}
		key, long, err := l.str(tracked)
		if err != nil {
			return err
		}
		l.space()
		if c, err := l.byte(); err != nil || c != ':' {
			return errLine
		}
		// A path is built only along the wanted fields' parents (at most
		// four members deep), so unrelated nesting costs no memory.
		sub, subTracked := "", false
		if tracked && !long {
			sub, subTracked = path+segment(key), true
		}
		if err := l.value(sub, subTracked, depth+1); err != nil {
			return err
		}
		l.space()
		c, err := l.byte()
		if err != nil || c != '}' && c != ',' {
			return errLine
		}
		if c == '}' {
			return nil
		}
	}
}

// array reads an array; no wanted field is inside one.
func (l *lineReader) array(depth int) error {
	_, _ = l.byte() // [
	l.space()
	if b, err := l.peek(); err == nil && b == ']' {
		_, _ = l.byte()
		return nil
	}
	for {
		if err := l.value("", false, depth+1); err != nil {
			return err
		}
		l.space()
		c, err := l.byte()
		if err != nil || c != ']' && c != ',' {
			return errLine
		}
		if c == ']' {
			return nil
		}
	}
}

// str reads a string: its decoded value when its spelling is at most
// keepRaw bytes (long reports a longer one, which is checked to its end
// but not kept).
func (l *lineReader) str(keep bool) (string, bool, error) {
	_, _ = l.byte() // "
	var raw []byte
	long := !keep // a string no field can be is checked, never kept
	if keep {
		raw = append(make([]byte, 0, 64), '"')
	}
	for {
		if long {
			l.skipPlain()
		}
		c, err := l.byte()
		if err != nil || c < 0x20 {
			return "", false, errLine
		}
		if !long {
			raw = append(raw, c)
		}
		if c == '"' {
			break
		}
		if c == '\\' {
			e, err := l.byte()
			if err != nil || !isEscape(e) {
				return "", false, errLine
			}
			if !long {
				raw = append(raw, e)
			}
			if e == 'u' {
				for range 4 {
					h, err := l.byte()
					if err != nil || !isHex(h) {
						return "", false, errLine
					}
					if !long {
						raw = append(raw, h)
					}
				}
			}
		}
		if len(raw) > keepRaw {
			long, raw = true, nil
		}
	}
	if long {
		return "", true, nil
	}
	var s string
	_ = json.Unmarshal(raw, &s) // checked above: a valid JSON string
	return s, false, nil
}

func isEscape(e byte) bool {
	switch e {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}

func isHex(h byte) bool { return h >= '0' && h <= '9' || h >= 'a' && h <= 'f' || h >= 'A' && h <= 'F' }

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// number reads a JSON number and reduces it as it goes: it is a count when
// it is exactly a whole non-negative int64, however it is spelled. Only the
// significant digits (at most 20) and two counters are kept, so a number of
// any length costs the same.
func (l *lineReader) number() (int64, bool, error) {
	var d numDigits
	neg := false
	if b, _ := l.peek(); b == '-' {
		neg = true
		_, _ = l.byte()
	}
	// The integer part: 0, or a digit 1-9 and more digits.
	b, err := l.byte()
	if err != nil || !isDigit(b) {
		return 0, false, errLine
	}
	d.add(b, true)
	if b != '0' {
		for {
			c, err := l.peek()
			if err != nil || !isDigit(c) {
				break
			}
			_, _ = l.byte()
			d.add(c, true)
		}
	}
	if c, err := l.peek(); err == nil && c == '.' {
		_, _ = l.byte()
		n := 0
		for {
			c, err := l.peek()
			if err != nil || !isDigit(c) {
				break
			}
			_, _ = l.byte()
			d.add(c, false)
			n++
		}
		if n == 0 {
			return 0, false, errLine
		}
	}
	exp := int64(0)
	if c, err := l.peek(); err == nil && (c == 'e' || c == 'E') {
		_, _ = l.byte()
		sign := int64(1)
		if c, err := l.peek(); err == nil && (c == '+' || c == '-') {
			_, _ = l.byte()
			if c == '-' {
				sign = -1
			}
		}
		n := 0
		for {
			c, err := l.peek()
			if err != nil || !isDigit(c) {
				break
			}
			_, _ = l.byte()
			if exp < 1<<40 {
				exp = exp*10 + int64(c-'0')
			}
			n++
		}
		if n == 0 {
			return 0, false, errLine
		}
		exp *= sign
	}
	v, ok := d.value(exp)
	if neg && v != 0 {
		ok = false
	}
	return v, ok, nil
}

// numDigits reduces a decimal coefficient: its significant digits (at most
// 19 kept; more cannot be an int64 count), the zeros pending after the last
// one, and where the last one sits: k integer-part zeros after it, or at
// fraction position fracPos.
type numDigits struct {
	sig       []byte
	pending   int64 // zeros since the last significant digit
	intTrail  int64 // integer-part zeros after the last significant digit
	fracCount int64 // fraction digits read
	fracPos   int64 // fraction position of the last significant digit (0: in the integer part)
	over      bool
}

func (d *numDigits) add(c byte, intPart bool) {
	if !intPart {
		d.fracCount++
	}
	if c == '0' {
		if len(d.sig) > 0 || d.over {
			d.pending++
			if intPart {
				d.intTrail++
			}
		}
		return
	}
	if int64(len(d.sig))+d.pending+1 > 19 {
		d.over = true
	} else {
		for range d.pending {
			d.sig = append(d.sig, '0')
		}
		d.sig = append(d.sig, c)
	}
	d.pending, d.intTrail = 0, 0
	if intPart {
		d.fracPos = 0
	} else {
		d.fracPos = d.fracCount
	}
}

// value is the coefficient times 10^exp when that is a whole int64.
func (d *numDigits) value(exp int64) (int64, bool) {
	if d.over {
		return 0, false
	}
	if len(d.sig) == 0 {
		return 0, true // zero, however spelled
	}
	k := d.intTrail - d.fracPos + exp
	if k < 0 || k > 19 {
		return 0, false
	}
	var v int64
	for _, c := range d.sig {
		if v > (math.MaxInt64-int64(c-'0'))/10 {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	for range k {
		if v > math.MaxInt64/10 {
			return 0, false
		}
		v *= 10
	}
	return v, true
}

// skipPlain passes over the buffered run of plain string bytes (no quote,
// backslash or control byte) a buffer at a time, so a long string that is
// not kept costs one scan and no copy.
func (l *lineReader) skipPlain() {
	for {
		n := l.r.Buffered()
		if n == 0 {
			if _, err := l.r.Peek(1); err != nil {
				return
			}
			n = l.r.Buffered()
		}
		buf, _ := l.r.Peek(n)
		i := 0
		for i < len(buf) && buf[i] >= 0x20 && buf[i] != '"' && buf[i] != '\\' {
			i++
		}
		_, _ = l.r.Discard(i)
		if i < len(buf) {
			return
		}
	}
}
