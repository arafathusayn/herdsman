package checkpoint

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
)

// The transcript reader streams: it finds the start of the last 400 lines
// with a backward scan through a fixed buffer, then reads each line once
// through a fixed buffer, keeping only the few fields the reading needs
// (short strings, and numbers reduced as they are read). A record of any
// size costs the same memory, and its usage still counts.

const (
	scanBuf  = 64 << 10
	maxDepth = 10000
	keepStr  = 64 // longer strings are checked but not kept
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

// wanted are the fields the reading needs, by path.
var wanted = map[string]bool{
	"": true, "type": true, "isSidechain": true, "message": true, "message.usage": true,
	"message.usage.input_tokens": true, "message.usage.cache_creation_input_tokens": true,
	"message.usage.cache_read_input_tokens": true, "payload": true, "payload.type": true,
	"payload.info": true, "payload.info.last_token_usage": true, "payload.info.last_token_usage.input_tokens": true,
}

// parents are the paths whose members can be wanted.
var parents = map[string]bool{"": true, "message": true, "message.usage": true, "payload": true, "payload.info": true, "payload.info.last_token_usage": true}

type lineReader struct {
	r    *bufio.Reader
	got  map[string]capture
	done bool // the file ended
}

func (l *lineReader) peek() (byte, error) {
	b, err := l.r.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (l *lineReader) byte() (byte, error) { return l.r.ReadByte() }

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

// rest skips to the end of the line.
func (l *lineReader) rest() {
	for {
		b, err := l.byte()
		if err != nil {
			l.done = true
			return
		}
		if b == '\n' {
			return
		}
	}
}

// line reads one line: whether it held anything, and errLine when it is
// not exactly one JSON value.
func (l *lineReader) line() (bool, error) {
	l.got = map[string]capture{}
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
	if err := l.value("", 0); err != nil {
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

func (l *lineReader) keep(path string, c capture) {
	if wanted[path] {
		l.got[path] = c
	}
}

func (l *lineReader) value(path string, depth int) error {
	if depth > maxDepth {
		return errLine
	}
	l.space()
	b, err := l.peek()
	if err != nil {
		return errLine
	}
	switch {
	case b == '{':
		l.keep(path, capture{kind: 'o'})
		return l.object(path, depth)
	case b == '[':
		l.keep(path, capture{kind: 'a'})
		return l.array(path, depth)
	case b == '"':
		s, long, err := l.str()
		l.keep(path, capture{kind: 's', str: s, long: long})
		return err
	case b == '-' || b >= '0' && b <= '9':
		n, ok, err := l.number()
		l.keep(path, capture{kind: 'n', count: n, ok: ok})
		return err
	}
	for _, lit := range []string{"true", "false", "null"} {
		if b == lit[0] {
			for i := range len(lit) {
				if c, err := l.byte(); err != nil || c != lit[i] {
					return errLine
				}
			}
			l.keep(path, capture{kind: map[byte]byte{'t': 't', 'f': 'f', 'n': 'z'}[b]})
			return nil
		}
	}
	return errLine
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (l *lineReader) object(path string, depth int) error {
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
		key, long, err := l.str()
		if err != nil {
			return err
		}
		l.space()
		if c, err := l.byte(); err != nil || c != ':' {
			return errLine
		}
		sub := "\x00" // a path no field matches
		if parents[path] && !long {
			sub = join(path, key)
		}
		if err := l.value(sub, depth+1); err != nil {
			return err
		}
		l.space()
		c, err := l.byte()
		if err != nil {
			return errLine
		}
		if c == '}' {
			return nil
		}
		if c != ',' {
			return errLine
		}
	}
}

func (l *lineReader) array(path string, depth int) error {
	_, _ = l.byte() // [
	l.space()
	if b, err := l.peek(); err == nil && b == ']' {
		_, _ = l.byte()
		return nil
	}
	for {
		if err := l.value("\x00", depth+1); err != nil {
			return err
		}
		l.space()
		c, err := l.byte()
		if err != nil {
			return errLine
		}
		if c == ']' {
			return nil
		}
		if c != ',' {
			return errLine
		}
	}
}

// str reads a string: its value when short (long reports a longer one,
// which is checked to its end but not kept).
func (l *lineReader) str() (string, bool, error) {
	_, _ = l.byte() // "
	raw := []byte{'"'}
	long := false
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
		if len(raw) > keepStr {
			long, raw = true, nil
		}
	}
	if long {
		return "", true, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false, errLine
	}
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
