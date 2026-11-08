package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParserKeys(t *testing.T) {
	cases := []struct {
		name, in string
		want     []Msg
	}{
		{"rune", "a", []Msg{Key{Type: KeyRune, Rune: 'a'}}},
		{"utf8", "é", []Msg{Key{Type: KeyRune, Rune: 'é'}}},
		{"enter", "\r", []Msg{Key{Type: KeyEnter}}},
		{"newline", "\n", []Msg{Key{Type: KeyEnter}}},
		{"tab", "\t", []Msg{Key{Type: KeyTab}}},
		{"backspace", "\x7f", []Msg{Key{Type: KeyBackspace}}},
		{"control h", "\x08", []Msg{Key{Type: KeyBackspace}}},
		{"control c", "\x03", []Msg{Key{Type: KeyCtrl, Rune: 'C', Ctrl: true}}},
		{"up", "\x1b[A", []Msg{Key{Type: KeyUp}}},
		{"down", "\x1b[B", []Msg{Key{Type: KeyDown}}},
		{"right", "\x1b[C", []Msg{Key{Type: KeyRight}}},
		{"left", "\x1b[D", []Msg{Key{Type: KeyLeft}}},
		{"home", "\x1b[H", []Msg{Key{Type: KeyHome}}},
		{"end", "\x1b[F", []Msg{Key{Type: KeyEnd}}},
		{"home tilde", "\x1b[1~", []Msg{Key{Type: KeyHome}}},
		{"home rxvt", "\x1b[7~", []Msg{Key{Type: KeyHome}}},
		{"end tilde", "\x1b[4~", []Msg{Key{Type: KeyEnd}}},
		{"end rxvt", "\x1b[8~", []Msg{Key{Type: KeyEnd}}},
		{"insert", "\x1b[2~", []Msg{Key{Type: KeyInsert}}},
		{"delete", "\x1b[3~", []Msg{Key{Type: KeyDelete}}},
		{"page up", "\x1b[5~", []Msg{Key{Type: KeyPageUp}}},
		{"page down", "\x1b[6~", []Msg{Key{Type: KeyPageDown}}},
		{"shift tab", "\x1b[Z", []Msg{Key{Type: KeyShiftTab}}},
		{"ss3 up", "\x1bOA", []Msg{Key{Type: KeyUp}}},
		{"ss3 home", "\x1bOH", []Msg{Key{Type: KeyHome}}},
		{"control right", "\x1b[1;5C", []Msg{Key{Type: KeyRight, Ctrl: true}}},
		{"shift up", "\x1b[1;2A", []Msg{Key{Type: KeyUp, Shift: true}}},
		{"alt delete", "\x1b[3;3~", []Msg{Key{Type: KeyDelete, Alt: true}}},
		{"bad modifier", "\x1b[1;:C", []Msg{Key{Type: KeyRight}}},
		{"alt letter", "\x1bx", []Msg{Key{Type: KeyRune, Rune: 'x', Alt: true}}},
		{"two escapes", "\x1b\x1b[A", []Msg{Key{Type: KeyEsc}, Key{Type: KeyUp}}},
		{"unknown final", "\x1b[Q", []Msg{Key{Type: KeyUnknown}}},
		{"unknown tilde", "\x1b[99~", []Msg{Key{Type: KeyUnknown}}},
		{"ss3 unknown", "\x1bOQ", []Msg{Key{Type: KeyUnknown}}},
		{"ss3 cut by escape", "\x1bO\x1b[A", []Msg{Key{Type: KeyUnknown}, Key{Type: KeyUp}}},
		{"control byte in sequence", "\x1b[\x01", []Msg{Key{Type: KeyUnknown}, Key{Type: KeyCtrl, Rune: 'A', Ctrl: true}}},
		{"sequence too long", "\x1b[" + strings.Repeat("1", 40), []Msg{
			Key{Type: KeyUnknown}, Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'},
			Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'},
			Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'},
			Key{Type: KeyRune, Rune: '1'}, Key{Type: KeyRune, Rune: '1'},
		}},
		{"invalid byte", "\xff", []Msg{Key{Type: KeyUnknown}}},
		{"c1 control", "\u009b", []Msg{Key{Type: KeyUnknown}}},
		{"paste", "\x1b[200~a\x1b[Ab\r\x1b[201~", []Msg{Paste{Text: "a\x1b[Ab\r"}}},
		{"empty paste", "\x1b[200~\x1b[201~", nil},
		{"stray paste end", "\x1b[201~", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Parser
			if got := p.Feed([]byte(c.in)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestParserFlush(t *testing.T) {
	cases := []struct {
		name, in string
		want     Key
	}{
		{"lone escape", "\x1b", Key{Type: KeyEsc}},
		{"partial sequence", "\x1b[1", Key{Type: KeyUnknown}},
		{"partial rune", "\xe2\x82", Key{Type: KeyUnknown}},
		{"partial ss3", "\x1bO", Key{Type: KeyUnknown}},
		{"alt partial rune", "\x1b\xc3", Key{Type: KeyUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p Parser
			if msgs := p.Feed([]byte(c.in)); msgs != nil || !p.Pending() {
				t.Fatalf("expected a wait, got %#v pending %v", msgs, p.Pending())
			}
			if got := p.Flush(); !reflect.DeepEqual(got, []Msg{c.want}) {
				t.Errorf("got %#v", got)
			}
			if p.Pending() || p.Flush() != nil {
				t.Error("flush left state behind")
			}
		})
	}
}

func TestPasteWaitsAcrossReadsAndIsNotFlushed(t *testing.T) {
	var p Parser
	if msgs := p.Feed([]byte("\x1b[200~ab\x1b[20")); msgs != nil {
		t.Fatalf("got %#v", msgs)
	}
	if p.Pending() || p.Flush() != nil {
		t.Fatal("a paste in progress must not time out")
	}
	got := p.Feed([]byte("1~z"))
	want := []Msg{Paste{Text: "ab"}, Key{Type: KeyRune, Rune: 'z'}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestPasteWithoutEndIsReleasedAtTheCap(t *testing.T) {
	var p Parser
	body := strings.Repeat("é", maxPaste)
	msgs := p.Feed([]byte("\x1b[200~" + body))
	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
	text := msgs[0].(Paste).Text
	if !strings.HasPrefix(body, text) || strings.Contains(text, "�") {
		t.Error("the released text was cut inside a character")
	}
	if len(p.buf) > len(pasteEnd) {
		t.Errorf("%d bytes still held", len(p.buf))
	}
	rest := p.Feed([]byte("\x1b[201~"))
	if joined := text + rest[0].(Paste).Text; joined != body {
		t.Error("the paste lost text across the cap")
	}
}

func TestStyle(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if !NewStyle(env(nil)).Color {
		t.Error("color off by default")
	}
	if NewStyle(env(map[string]string{"NO_COLOR": "1"})).Color {
		t.Error("NO_COLOR ignored")
	}
	if !NewStyle(env(map[string]string{"NO_COLOR": ""})).Color {
		t.Error("an empty NO_COLOR must not count")
	}
	if NewStyle(env(map[string]string{"TERM": "dumb"})).Color {
		t.Error("dumb terminal has color")
	}
	on, off := Style{Color: true}, Style{}
	if got := on.Fg(Red, "x"); got != "\x1b[31mx\x1b[0m" {
		t.Errorf("%q", got)
	}
	if got := off.Fg(Red, "x"); got != "x" {
		t.Errorf("%q", got)
	}
	if got := off.Bold("x"); got != "\x1b[1mx\x1b[0m" {
		t.Errorf("%q", got)
	}
	if got := off.Dim("x"); got != "\x1b[2mx\x1b[0m" {
		t.Errorf("%q", got)
	}
	if got := off.Reverse("x"); got != "\x1b[7mx\x1b[0m" {
		t.Errorf("%q", got)
	}
}

func TestCleanRemovesControls(t *testing.T) {
	if got := Clean("a\x1b[2Jb\tc\nd\u009be"); got != "a[2Jb c\nde" {
		t.Errorf("%q", got)
	}
}

func TestWidthAndTruncate(t *testing.T) {
	cases := []struct {
		in    string
		width int
	}{
		{"abc", 3}, {"日本", 4}, {"é", 1}, {"\x1b[31mab\x1b[0m", 2}, {"🙂", 2}, {"\x1b[", 0},
	}
	for _, c := range cases {
		if got := Width(c.in); got != c.width {
			t.Errorf("Width(%q) = %d", c.in, got)
		}
	}
	if got := Truncate("abc", 3); got != "abc" {
		t.Errorf("%q", got)
	}
	if got := Truncate("abcd", 2); got != "ab" {
		t.Errorf("%q", got)
	}
	if got := Truncate("日本語", 3); got != "日" {
		t.Errorf("a wide character was split: %q", got)
	}
	if got := Truncate("\x1b[31mabcd\x1b[0m", 2); got != "\x1b[31mab\x1b[0m" {
		t.Errorf("style not reset: %q", got)
	}
	if got := Truncate("\x1b[31ma\x1b[0mbcd", 2); got != "\x1b[31ma\x1b[0mb" {
		t.Errorf("an extra reset was added: %q", got)
	}
}

func paintAll(t *testing.T, r *renderer, frames ...string) []string {
	t.Helper()
	var out []string
	for _, frame := range frames {
		r.w.(*bytes.Buffer).Reset()
		if err := r.paint(frame); err != nil {
			t.Fatal(err)
		}
		out = append(out, r.w.(*bytes.Buffer).String())
	}
	return out
}

func TestRendererPaintsOnlyChangedLines(t *testing.T) {
	r := &renderer{w: &bytes.Buffer{}, cols: 80, rows: 24}
	got := paintAll(t, r, "a\nb\nc", "a\nB\nc", "a\nB\nc")
	want := []string{
		"\ra\x1b[K\r\nb\x1b[K\r\nc\x1b[K",
		"\x1b[2A\r\r\nB\x1b[K\r\n",
		"\x1b[2A\r\r\n\r\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRendererShrinkAndGrow(t *testing.T) {
	r := &renderer{w: &bytes.Buffer{}, cols: 80, rows: 24}
	got := paintAll(t, r, "a\nb\nc", "a", "a\nz")
	want := []string{
		"\ra\x1b[K\r\nb\x1b[K\r\nc\x1b[K",
		"\x1b[2A\r\r\n\x1b[J\x1b[1A",
		"\r\r\nz\x1b[K",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRendererFitsTheScreen(t *testing.T) {
	r := &renderer{w: &bytes.Buffer{}, cols: 3, rows: 3}
	got := paintAll(t, r, "abcdef\nb\nc\r\nd")
	if want := "\rc\x1b[K\r\nd\x1b[K"; got[0] != want {
		t.Errorf("got %q, want %q", got[0], want)
	}
	r = &renderer{w: &bytes.Buffer{}, cols: 3, rows: 0}
	if got := paintAll(t, r, "a\nb"); got[0] != "\ra\x1b[K\r\nb\x1b[K" {
		t.Errorf("an unknown height must not clip: %q", got[0])
	}
}

func TestRendererResize(t *testing.T) {
	buf := &bytes.Buffer{}
	r := &renderer{w: buf, cols: 10, rows: 24}
	paintAll(t, r, "aaaaaaaaaa\nb")
	buf.Reset()
	if err := r.resize(5, 24); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "\x1b[2A\r\x1b[J" {
		t.Errorf("rewrapped height wrong: %q", got)
	}
	if r.prev != nil || r.cols != 5 {
		t.Error("state not reset")
	}

	paintAll(t, r, "a\nb\nc\nd")
	buf.Reset()
	if err := r.resize(5, 2); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "\x1b[1A\r\x1b[J" {
		t.Errorf("height must clamp to the screen: %q", got)
	}

	paintAll(t, r, "a")
	buf.Reset()
	if err := r.resize(0, 0); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "\r\x1b[J" {
		t.Errorf("zero size: %q", got)
	}
}

type failWriter struct{ after int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.after <= 0 {
		return 0, errors.New("write failed")
	}
	w.after--
	return len(p), nil
}

func TestRendererWriteErrors(t *testing.T) {
	r := &renderer{w: &failWriter{}, cols: 5, rows: 5}
	if r.paint("a") == nil {
		t.Error("paint hid the error")
	}
	if r.resize(4, 4) == nil {
		t.Error("resize hid the error")
	}
}

func TestBatch(t *testing.T) {
	if Batch(nil, nil) != nil {
		t.Error("empty batch must be nil")
	}
	cmd := Batch(nil, Quit)
	if got := cmd().(batchMsg); len(got) != 1 {
		t.Errorf("got %d", len(got))
	}
}

// fakeTerm records raw mode and answers size queries.
type fakeTerm struct {
	mu         sync.Mutex
	raw, reset int
	cols, rows int
	rawErr     error
	resetErr   error
	sizeErrAt  int
	sizes      int
}

func (f *fakeTerm) MakeRaw() (func() error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rawErr != nil {
		return nil, f.rawErr
	}
	f.raw++
	return func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reset++
		return f.resetErr
	}, nil
}

func (f *fakeTerm) Size() (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes++
	if f.sizes == f.sizeErrAt {
		return 0, 0, errors.New("no size")
	}
	return f.cols, f.rows, nil
}

// script is a model that records messages and runs a function for each.
type script struct {
	seen []Msg
	view string
	on   func(Msg) Cmd
}

func (s script) Update(msg Msg) (Model, Cmd) {
	s.seen = append(s.seen[:len(s.seen):len(s.seen)], msg)
	if key, ok := msg.(Key); ok && key.Rune != 0 {
		s.view += string(key.Rune)
	}
	if s.on != nil {
		return s, s.on(msg)
	}
	return s, nil
}

func (s script) View() string { return s.view }

func quitOn(r rune) func(Msg) Cmd {
	return func(msg Msg) Cmd {
		if key, ok := msg.(Key); ok && key.Rune == r {
			return Quit
		}
		return nil
	}
}

type blockReader struct{ gate chan []byte }

func (b blockReader) Read(p []byte) (int, error) {
	data, ok := <-b.gate
	if !ok {
		return 0, io.EOF
	}
	return copy(p, data), nil
}

func newPipe(t *testing.T) (*Input, chan []byte) {
	t.Helper()
	gate := make(chan []byte, 8)
	t.Cleanup(func() { close(gate) })
	return NewInput(blockReader{gate}), gate
}

func TestRunShowsFramesAndRestores(t *testing.T) {
	in, gate := newPipe(t)
	term := &fakeTerm{cols: 80, rows: 24}
	var out bytes.Buffer
	gate <- []byte("ab")
	gate <- []byte("q")
	final, err := Run(context.Background(), script{on: quitOn('q')}, Options{In: in, Out: &out, Term: term})
	if err != nil {
		t.Fatal(err)
	}
	s := final.(script)
	if !reflect.DeepEqual(s.seen[0], Msg(Resize{Cols: 80, Rows: 24})) {
		t.Errorf("first message %#v", s.seen[0])
	}
	if s.view != "abq" {
		t.Errorf("view %q", s.view)
	}
	if term.raw != 1 || term.reset != 1 {
		t.Errorf("raw %d reset %d", term.raw, term.reset)
	}
	text := out.String()
	if !strings.HasPrefix(text, enterScreen) || !strings.HasSuffix(text, leaveScreen) {
		t.Errorf("screen setup not balanced: %q", text)
	}
	if strings.Contains(text, "\x1b[?1049") {
		t.Error("forms must not use the alternate screen")
	}
}

func TestRunLoneEscapeBecomesAKey(t *testing.T) {
	in, gate := newPipe(t)
	gate <- []byte("\x1b")
	m := script{on: func(msg Msg) Cmd {
		if key, ok := msg.(Key); ok && key.Type == KeyEsc {
			return Quit
		}
		return nil
	}}
	if _, err := Run(context.Background(), m, Options{In: in, Out: io.Discard, Term: &fakeTerm{cols: 80, rows: 24}, EscDelay: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
}

func TestRunDefaultEscDelayLetsASplitSequenceFinish(t *testing.T) {
	in, gate := newPipe(t)
	gate <- []byte("\x1b")
	gate <- []byte("[A")
	var got []Msg
	m := script{on: func(msg Msg) Cmd {
		if key, ok := msg.(Key); ok {
			got = append(got, key)
			return Quit
		}
		return nil
	}}
	if _, err := Run(context.Background(), m, Options{In: in, Out: io.Discard, Term: &fakeTerm{cols: 80, rows: 24}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Msg{Key{Type: KeyUp}}) {
		t.Errorf("got %#v", got)
	}
}

func TestRunCommandResultsAndBatch(t *testing.T) {
	in, _ := newPipe(t)
	type ping struct{}
	type pong struct{}
	send := func(v Msg) Cmd { return func() Msg { return v } }
	count := 0
	m := script{on: func(msg Msg) Cmd {
		switch msg.(type) {
		case Resize:
			return Batch(send(ping{}), send(pong{}))
		case ping, pong:
			count++
			if count == 2 {
				return Quit
			}
		}
		return nil
	}}
	if _, err := Run(context.Background(), m, Options{In: in, Out: io.Discard, Term: &fakeTerm{cols: 80, rows: 24}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunResize(t *testing.T) {
	in, _ := newPipe(t)
	term := &fakeTerm{cols: 80, rows: 24}
	resize := make(chan struct{}, 1)
	resize <- struct{}{}
	n := 0
	m := script{on: func(msg Msg) Cmd {
		if _, ok := msg.(Resize); ok {
			n++
			if n == 2 {
				return Quit
			}
		}
		return nil
	}}
	if _, err := Run(context.Background(), m, Options{In: in, Out: io.Discard, Term: term, Resize: resize}); err != nil {
		t.Fatal(err)
	}
}

func TestRunEndsWithTheContext(t *testing.T) {
	in, _ := newPipe(t)
	term := &fakeTerm{cols: 80, rows: 24}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, script{}, Options{In: in, Out: io.Discard, Term: term})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if term.reset != 1 {
		t.Error("terminal not restored")
	}
}

func TestRunInputClosed(t *testing.T) {
	_, err := Run(context.Background(), script{}, Options{
		In: NewInput(strings.NewReader("")), Out: io.Discard, Term: &fakeTerm{cols: 80, rows: 24},
	})
	if !errors.Is(err, ErrInputClosed) || !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}

func TestRunRestoresAfterAPanic(t *testing.T) {
	in, _ := newPipe(t)
	term := &fakeTerm{cols: 80, rows: 24}
	var out bytes.Buffer
	defer func() {
		if recover() == nil {
			t.Fatal("the panic was swallowed")
		}
		if term.reset != 1 || !strings.HasSuffix(out.String(), leaveScreen) {
			t.Errorf("not restored: reset %d, %q", term.reset, out.String())
		}
	}()
	m := script{on: func(Msg) Cmd { panic("boom") }}
	_, _ = Run(context.Background(), m, Options{In: in, Out: &out, Term: term})
}

func TestRunErrors(t *testing.T) {
	in, gate := newPipe(t)
	base := func() *fakeTerm { return &fakeTerm{cols: 80, rows: 24} }
	bad := errors.New("restore failed")

	term := base()
	term.rawErr = errors.New("not a terminal")
	if _, err := Run(context.Background(), script{}, Options{In: in, Out: io.Discard, Term: term}); !errors.Is(err, term.rawErr) {
		t.Errorf("raw: %v", err)
	}

	term = base()
	term.sizeErrAt = 1
	if _, err := Run(context.Background(), script{}, Options{In: in, Out: io.Discard, Term: term}); err == nil || term.reset != 1 {
		t.Errorf("size: %v reset %d", err, term.reset)
	}

	if _, err := Run(context.Background(), script{}, Options{In: in, Out: &failWriter{}, Term: base()}); err == nil {
		t.Error("the first write failure was hidden")
	}

	if _, err := Run(context.Background(), script{view: "x"}, Options{In: in, Out: &failWriter{after: 1}, Term: base()}); err == nil {
		t.Error("the first paint failure was hidden")
	}

	term = base()
	term.sizeErrAt = 2
	resize := make(chan struct{}, 1)
	resize <- struct{}{}
	if _, err := Run(context.Background(), script{}, Options{In: in, Out: io.Discard, Term: term, Resize: resize}); err == nil {
		t.Error("the size failure on resize was hidden")
	}

	resize = make(chan struct{}, 1)
	resize <- struct{}{}
	if _, err := Run(context.Background(), script{view: "x"}, Options{In: in, Out: &failWriter{after: 2}, Term: base(), Resize: resize}); err == nil {
		t.Error("the erase failure on resize was hidden")
	}

	gate <- []byte("x")
	if _, err := Run(context.Background(), script{view: "x", on: func(Msg) Cmd { return nil }}, Options{In: in, Out: &failWriter{after: 2}, Term: base()}); err == nil {
		t.Error("the paint failure was hidden")
	}

	term = base()
	term.resetErr = bad
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, script{}, Options{In: in, Out: io.Discard, Term: term}); !errors.Is(err, bad) {
		t.Errorf("restore: %v", err)
	}
}

// TestInputCarriesOverBetweenPrograms proves a read pending when one program
// ends reaches the next one.
func TestInputCarriesOverBetweenPrograms(t *testing.T) {
	in, gate := newPipe(t)
	term := &fakeTerm{cols: 80, rows: 24}
	gate <- []byte("q")
	if _, err := Run(context.Background(), script{on: quitOn('q')}, Options{In: in, Out: io.Discard, Term: term}); err != nil {
		t.Fatal(err)
	}
	gate <- []byte("r")
	final, err := Run(context.Background(), script{on: quitOn('r')}, Options{In: in, Out: io.Discard, Term: term})
	if err != nil {
		t.Fatal(err)
	}
	if final.(script).view != "r" {
		t.Errorf("view %q", final.(script).view)
	}
}

func TestDispatchAfterTheProgramEnded(t *testing.T) {
	l := &loop{msgs: make(chan Msg), done: make(chan struct{})}
	release := make(chan struct{})
	finished := make(chan struct{})
	l.dispatch(func() Msg {
		defer close(finished)
		<-release
		return nil
	})
	close(l.done)
	close(release)
	<-finished
}

func TestPrompter(t *testing.T) {
	var out bytes.Buffer
	p := NewPrompter(strings.NewReader("one\r\nfirst\nsecond\n.\nlast"), &out)
	if got, err := p.Line("Name"); err != nil || got != "one" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := p.Block("Prompt"); err != nil || got != "first\nsecond" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := p.Line("Tail"); err != nil || got != "last" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := p.Line("Gone"); !errors.Is(err, ErrCanceled) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(out.String(), "Name: ") || !strings.Contains(out.String(), "only a period") {
		t.Errorf("%q", out.String())
	}
	if err := p.Say("\x1b[2Jhi"); err != nil || !strings.Contains(out.String(), "[2Jhi\n") {
		t.Errorf("%v %q", err, out.String())
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestPrompterErrors(t *testing.T) {
	bad := errors.New("read failed")
	p := NewPrompter(errReader{bad}, io.Discard)
	if _, err := p.Line("x"); !errors.Is(err, bad) {
		t.Errorf("got %v", err)
	}
	if _, err := p.Block("x"); !errors.Is(err, bad) {
		t.Errorf("got %v", err)
	}
	p = NewPrompter(strings.NewReader(""), &failWriter{})
	if _, err := p.Line("x"); err == nil {
		t.Error("line write error hidden")
	}
	if _, err := p.Block("x"); err == nil {
		t.Error("block write error hidden")
	}
}
