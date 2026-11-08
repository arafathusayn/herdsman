package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"sync"
)

// Marker replaces a secret.
const Marker = "<redacted>"

// escapeLimit is how far an escape sequence is looked for. Past it the bytes
// count as text, so a stream cannot make a Writer hold data without limit.
const escapeLimit = 4096

// truncatedMin is how much of a secret's beginning, left at the end of a
// stream, is hidden. Below it the tail is far more likely to be ordinary text.
const truncatedMin = 4

// Redactor replaces known secrets in text. It is immutable and a nil Redactor
// changes nothing.
type Redactor struct {
	needles [][]byte
	longest int
	json    *Redactor
}

// NewRedactor prepares the secrets in the forms text commonly carries them.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	for _, s := range secrets {
		if len(flatten(s)) == 0 {
			continue
		}
		for _, form := range forms(s) {
			flat := flatten(form)
			if len(flat) > 0 && !slices.ContainsFunc(r.needles, func(n []byte) bool { return bytes.Equal(n, flat) }) {
				r.needles = append(r.needles, flat)
				r.longest = max(r.longest, len(flat))
			}
		}
	}
	r.json = &Redactor{}
	for _, n := range r.needles {
		if json.Valid([]byte(`"` + string(n) + `"`)) {
			r.json.needles = append(r.json.needles, n)
			r.json.longest = max(r.json.longest, len(n))
		}
	}
	return r
}

// JSON is a redactor for text known to be JSON. A secret's raw form may hold a
// quote, which in JSON text could only be a delimiter, so forms that cannot
// occur inside a JSON string are left out.
func (r *Redactor) JSON() *Redactor {
	if r == nil {
		return nil
	}
	return r.json
}

// flatten drops control characters: a terminal may break a value anywhere and
// a program may interleave them, so they carry no meaning for matching.
func flatten(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if !isControl(s[i]) {
			out = append(out, s[i])
		}
	}
	return out
}

func isControl(c byte) bool { return c < 0x20 || c == 0x7f }

func forms(s string) []string {
	var html, plain bytes.Buffer
	_ = json.NewEncoder(&html).Encode(s)
	e := json.NewEncoder(&plain)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return []string{
		s,
		unquote(html.Bytes()),
		unquote(plain.Bytes()),
		url.QueryEscape(s),
		url.PathEscape(s),
		base64.StdEncoding.EncodeToString([]byte(s)),
		base64.URLEncoding.EncodeToString([]byte(s)),
	}
}

// unquote removes the encoder's newline and exactly its two enclosing quotes;
// any quote next to them belongs to the value.
func unquote(encoded []byte) string {
	return string(encoded[1 : len(bytes.TrimSuffix(encoded, []byte("\n")))-1])
}

// Active says whether there is anything to replace.
func (r *Redactor) Active() bool { return r != nil && len(r.needles) > 0 }

// String returns s with every secret replaced.
func (r *Redactor) String(s string) string {
	if !r.Active() {
		return s
	}
	return string(r.bytes([]byte(s)))
}

func (r *Redactor) bytes(b []byte) []byte {
	spans := r.spans(b, scan(b))
	var out bytes.Buffer
	at := 0
	for _, s := range spans {
		out.Write(b[at:s.from])
		out.WriteString(Marker)
		at = s.to
	}
	out.Write(b[at:])
	return out.Bytes()
}

type span struct{ from, to int }

// view is text with its escape sequences and line ends taken out; origin maps
// each byte back. pending is where the first unfinished escape starts, or -1,
// and settled is how much of text comes before it. Bytes after that point may
// yet turn out to be part of the escape.
type view struct {
	text    []byte
	origin  []int
	pending int
	settled int
}

func scan(b []byte) view {
	v := view{text: make([]byte, 0, len(b)), origin: make([]int, 0, len(b)), pending: -1}
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == 0x1b:
			n := escapeLen(b[i:])
			if n == 0 {
				if len(b)-i < escapeLimit && v.pending < 0 {
					v.pending, v.settled = i, len(v.text)
				}
				n = 1
			}
			i += n
		case isControl(c):
			i++
		default:
			v.text = append(v.text, c)
			v.origin = append(v.origin, i)
			i++
		}
	}
	if v.pending < 0 {
		v.settled = len(v.text)
	}
	return v
}

// escapeLen measures the terminal escape at the start of b, 0 when it is not
// finished within the limit.
func escapeLen(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	limit := min(len(b), escapeLimit)
	switch b[1] {
	case '[':
		for i := 2; i < limit; i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		return 0
	case ']', 'P', 'X', '^', '_':
		for i := 2; i < limit; i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < limit && b[i+1] == '\\' {
				return i + 2
			}
		}
		return 0
	}
	// Any number of intermediate bytes, then one final byte, as in ESC ( B.
	for i := 1; i < limit; i++ {
		switch c := b[i]; {
		case c >= 0x20 && c <= 0x2f:
		case c >= 0x30 && c <= 0x7e:
			return i + 1
		default:
			return 1
		}
	}
	return 0
}

// spans finds every secret, both through the view and in the raw bytes, which
// is where a value inside an escape sequence such as a window title shows.
func (r *Redactor) spans(b []byte, v view) []span {
	var found []span
	for _, n := range r.needles {
		for at := 0; ; {
			i := bytes.Index(v.text[at:], n)
			if i < 0 {
				break
			}
			start := at + i
			found = append(found, span{v.origin[start], v.origin[start+len(n)-1] + 1})
			at = start + 1
		}
		for at := 0; ; {
			i := bytes.Index(b[at:], n)
			if i < 0 {
				break
			}
			found = append(found, span{at + i, at + i + len(n)})
			at += i + 1
		}
	}
	slices.SortFunc(found, func(a, c span) int { return a.from - c.from })
	var merged []span
	for _, s := range found {
		if last := len(merged) - 1; last >= 0 && s.from <= merged[last].to {
			merged[last].to = max(merged[last].to, s.to)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// partial is the length of the longest end of text that starts some secret
// without completing it.
func (r *Redactor) partial(text []byte) int {
	for l := min(r.longest-1, len(text)); l > 0; l-- {
		tail := text[len(text)-l:]
		if slices.ContainsFunc(r.needles, func(n []byte) bool { return len(n) > l && bytes.HasPrefix(n, tail) }) {
			return l
		}
	}
	return 0
}

// holdFrom is where the part of b that later bytes could still turn into a
// secret begins.
func (r *Redactor) holdFrom(b []byte, v view) int {
	cut := len(b)
	if l := r.partial(v.text[:v.settled]); l > 0 {
		cut = v.origin[v.settled-l]
	}
	if v.pending >= 0 {
		cut = min(cut, v.pending)
	}
	for _, s := range r.spans(b, v) {
		if cut > s.from && cut < s.to {
			cut = s.to
		}
	}
	return cut
}

// Writer redacts a stream. A secret may be split across writes, so the end of
// the data is held back while it could still become one. Close releases it.
type Writer struct {
	mu  sync.Mutex
	r   *Redactor
	to  io.Writer
	buf []byte
}

// Writer returns a stream that redacts into w.
func (r *Redactor) Writer(w io.Writer) *Writer { return &Writer{r: r, to: w} }

// Write takes all of p unless the destination fails.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.r.Active() {
		return w.to.Write(p)
	}
	w.buf = append(w.buf, p...)
	cut := w.r.holdFrom(w.buf, scan(w.buf))
	if cut == 0 {
		return len(p), nil
	}
	_, err := w.to.Write(w.r.bytes(w.buf[:cut]))
	w.buf = append(w.buf[:0], w.buf[cut:]...)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close releases what is held. A long enough start of a secret, cut off by
// the end of the stream, is replaced rather than shown.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return nil
	}
	out := w.r.bytes(w.buf)
	if w.r.partial(settledText(w.buf)) >= truncatedMin {
		out = []byte(Marker)
	}
	w.buf = nil
	_, err := w.to.Write(out)
	return err
}

func settledText(b []byte) []byte {
	v := scan(b)
	return v.text[:v.settled]
}

// PerCall redacts each write on its own, for a destination such as a logger
// that sends one whole line per call and must not have its end delayed.
func (r *Redactor) PerCall(w io.Writer) io.Writer {
	if !r.Active() {
		return w
	}
	return perCall{r, w}
}

type perCall struct {
	r  *Redactor
	to io.Writer
}

func (p perCall) Write(b []byte) (int, error) {
	if _, err := p.to.Write(p.r.bytes(b)); err != nil {
		return 0, err
	}
	return len(b), nil
}
