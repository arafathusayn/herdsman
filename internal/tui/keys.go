package tui

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KeyType names a key. KeyRune carries its character in Key.Rune, and KeyCtrl
// carries the letter held with Control.
type KeyType int

// The keys the parser reports.
const (
	KeyUnknown KeyType = iota
	KeyRune
	KeyCtrl
	KeyEnter
	KeyTab
	KeyShiftTab
	KeyBackspace
	KeyDelete
	KeyInsert
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
)

// Key is one key press.
type Key struct {
	Type             KeyType
	Rune             rune
	Alt, Ctrl, Shift bool
}

// Paste is text delivered through bracketed paste.
type Paste struct {
	Text string
}

const (
	maxSequence = 32
	maxPaste    = 1 << 20
)

var (
	pasteEnd = []byte("\x1b[201~")

	finalKeys = map[byte]KeyType{
		'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft,
		'H': KeyHome, 'F': KeyEnd, 'Z': KeyShiftTab,
	}
	tildeKeys = map[int]KeyType{
		1: KeyHome, 7: KeyHome, 4: KeyEnd, 8: KeyEnd,
		2: KeyInsert, 3: KeyDelete, 5: KeyPageUp, 6: KeyPageDown,
	}
)

// Parser turns the bytes of a terminal into messages. It holds a sequence that
// a read split in two until the rest arrives.
type Parser struct {
	buf     []byte
	pasting bool
}

// Pending reports whether bytes wait for more input. The caller gives them a
// moment and then calls Flush, which is how a lone Escape is told apart from
// the start of a sequence.
func (p *Parser) Pending() bool { return !p.pasting && len(p.buf) > 0 }

// Feed adds bytes and returns every message they complete.
func (p *Parser) Feed(data []byte) []Msg {
	p.buf = append(p.buf, data...)
	var out []Msg
	for len(p.buf) > 0 {
		if p.pasting {
			msg, ok := p.takePaste()
			if !ok {
				break
			}
			if msg != nil {
				out = append(out, msg)
			}
			continue
		}
		msg, n := p.parse(p.buf)
		if n == 0 {
			break
		}
		p.buf = p.buf[n:]
		if msg != nil {
			out = append(out, msg)
		}
	}
	return out
}

// Flush gives up waiting: a lone Escape becomes the Escape key, and any other
// partial sequence becomes one unknown key rather than stray characters.
func (p *Parser) Flush() []Msg {
	if !p.Pending() {
		return nil
	}
	lone := len(p.buf) == 1 && p.buf[0] == 0x1b
	p.buf = nil
	if lone {
		return []Msg{Key{Type: KeyEsc}}
	}
	return []Msg{Key{Type: KeyUnknown}}
}

// takePaste consumes paste text. The second result is false when the parser
// must wait for more input. The text is released in pieces once it passes the
// cap, so that a paste whose end marker never comes cannot grow without limit.
func (p *Parser) takePaste() (Msg, bool) {
	if i := bytes.Index(p.buf, pasteEnd); i >= 0 {
		text := p.buf[:i]
		p.buf = p.buf[i+len(pasteEnd):]
		p.pasting = false
		if len(text) == 0 {
			return nil, true
		}
		return Paste{Text: strings.ToValidUTF8(string(text), "�")}, true
	}
	if len(p.buf) <= maxPaste {
		return nil, false
	}
	keep := len(pasteEnd) - 1
	cut := len(p.buf) - keep
	for cut > 0 && !utf8.RuneStart(p.buf[cut]) {
		cut--
	}
	text := p.buf[:cut]
	p.buf = p.buf[cut:]
	return Paste{Text: strings.ToValidUTF8(string(text), "�")}, true
}

// parse reads one message from the start of b. It returns how many bytes that
// took, and 0 when b ends inside a sequence.
func (p *Parser) parse(b []byte) (Msg, int) {
	if b[0] != 0x1b {
		return plainKey(b)
	}
	if len(b) == 1 {
		return nil, 0
	}
	switch b[1] {
	case 0x1b:
		return Key{Type: KeyEsc}, 1
	case '[':
		return p.parseCSI(b)
	case 'O':
		return parseSS3(b)
	}
	msg, n := plainKey(b[1:])
	if n == 0 {
		return nil, 0
	}
	key := msg.(Key)
	key.Alt = true
	return key, n + 1
}

// plainKey reads one key that does not start with Escape.
func plainKey(b []byte) (Msg, int) {
	c := b[0]
	switch {
	case c == '\r' || c == '\n':
		return Key{Type: KeyEnter}, 1
	case c == '\t':
		return Key{Type: KeyTab}, 1
	case c == 0x7f || c == 0x08:
		return Key{Type: KeyBackspace}, 1
	case c < 0x20:
		return Key{Type: KeyCtrl, Rune: rune(c) + '@', Ctrl: true}, 1
	case c < utf8.RuneSelf:
		return Key{Type: KeyRune, Rune: rune(c)}, 1
	}
	if !utf8.FullRune(b) {
		return nil, 0
	}
	r, n := utf8.DecodeRune(b)
	if r == utf8.RuneError || !unicode.IsGraphic(r) {
		return Key{Type: KeyUnknown}, n
	}
	return Key{Type: KeyRune, Rune: r}, n
}

// parseCSI reads a control sequence: parameter bytes, intermediate bytes and
// one final byte.
func (p *Parser) parseCSI(b []byte) (Msg, int) {
	for i := 2; i < len(b); i++ {
		c := b[i]
		switch {
		case i >= maxSequence:
			return Key{Type: KeyUnknown}, i
		case c >= 0x20 && c <= 0x3f:
			continue
		case c >= 0x40 && c <= 0x7e:
			return p.finishCSI(string(b[2:i]), c), i + 1
		default:
			return Key{Type: KeyUnknown}, i
		}
	}
	return nil, 0
}

func (p *Parser) finishCSI(params string, final byte) Msg {
	parts := strings.Split(params, ";")
	first, _ := strconv.Atoi(parts[0])
	key := Key{}
	if final == '~' {
		switch first {
		case 200:
			p.pasting = true
			return nil
		case 201:
			return nil
		}
		key.Type = tildeKeys[first]
	} else {
		key.Type = finalKeys[final]
	}
	if key.Type == KeyUnknown {
		return Key{Type: KeyUnknown}
	}
	if len(parts) > 1 {
		if mod, err := strconv.Atoi(parts[1]); err == nil && mod > 1 {
			key.Shift = (mod-1)&1 != 0
			key.Alt = (mod-1)&2 != 0
			key.Ctrl = (mod-1)&4 != 0
		}
	}
	return key
}

// parseSS3 reads the three-byte form that some terminals send for arrows,
// Home and End.
func parseSS3(b []byte) (Msg, int) {
	if len(b) < 3 {
		return nil, 0
	}
	if b[2] == 0x1b {
		return Key{Type: KeyUnknown}, 2
	}
	return Key{Type: finalKeys[b[2]]}, 3
}
