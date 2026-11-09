package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const value = "hunter2-Sup3rS3cret"

type fakeKeychain map[string]string

func (f fakeKeychain) Load(service, account string) (string, error) {
	v, ok := f[service+"/"+account]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

func TestParseRefs(t *testing.T) {
	got, err := ParseRefs(" API_KEY=herdsman/route/key , B=s/a ")
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{{"API_KEY", "herdsman/route", "key"}, {"B", "s", "a"}}
	if !slices.Equal(got, want) {
		t.Fatalf("refs = %+v", got)
	}
	if got, err := ParseRefs("  "); err != nil || got != nil {
		t.Fatalf("empty = %v %v", got, err)
	}
	for _, bad := range []string{"NOEQUALS", "A=noslash", "1A=s/a", "A=/a", "A=s/", "A=s/a,A=s/b", "A-B=s/a"} {
		if _, err := ParseRefs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLoadBuildsEnvironmentFromKeychainOnly(t *testing.T) {
	set, err := Load(fakeKeychain{"s/a": value}, []Ref{{"TOKEN", "s", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	env := set.Environ([]string{"TOKEN=from-the-shell", "PATH=/bin", "TOKENS=kept"})
	if !slices.Equal(env, []string{"PATH=/bin", "TOKENS=kept", "TOKEN=" + value}) {
		t.Fatalf("env = %q", env)
	}
	if got := set.Redactor().String("x " + value); got != "x "+Marker {
		t.Fatalf("redacted = %q", got)
	}
}

func TestLoadFailsClosedWithoutTheValue(t *testing.T) {
	if _, err := Load(fakeKeychain{}, []Ref{{"TOKEN", "s", "a"}}); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := Load(fakeKeychain{"s/a": ""}, []Ref{{"TOKEN", "s", "a"}}); err == nil {
		t.Fatal("an empty secret was accepted")
	}
}

func TestFromEnv(t *testing.T) {
	kc := fakeKeychain{"s/a": value}
	set, err := FromEnv(func(string) string { return "T=s/a" }, kc)
	if err != nil || !set.Redactor().Active() {
		t.Fatalf("set = %+v, %v", set, err)
	}
	set, err = FromEnv(func(string) string { return "" }, kc)
	if err != nil || set.Redactor().Active() {
		t.Fatalf("unset: %+v, %v", set, err)
	}
	if _, err := FromEnv(func(string) string { return "bad" }, kc); err == nil {
		t.Fatal("bad references accepted")
	}
}

// fakeTool puts a program with the platform tool's name first on PATH.
func fakeTool(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ToolName), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestSystemKeychain(t *testing.T) {
	fakeTool(t, "printf '%s\\n' \""+value+"\"\n")
	got, err := System().Load("s", "a")
	if err != nil || got != value {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSystemKeychainKeepsItsOutputOutOfErrors(t *testing.T) {
	fakeTool(t, "echo "+value+" >&2\nexit 7\n")
	_, err := System().Load("s", "a")
	if err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("err = %v", err)
	}
}

func TestSystemKeychainNotFound(t *testing.T) {
	fakeTool(t, "exit "+notFoundExit+"\n")
	if _, err := System().Load("s", "a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestSystemKeychainMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := System().Load("s", "a"); err == nil {
		t.Fatal("no tool, no error")
	}
}

func TestRedactForms(t *testing.T) {
	r := NewRedactor(value)
	cases := map[string]string{
		"plain":      "token " + value + " end",
		"twice":      value + value,
		"ansi split": "token " + value[:4] + "\x1b[31m" + value[4:9] + "\x1b[0m" + value[9:] + " end",
		"wrapped":    "token " + value[:7] + "\r\n" + value[7:] + " end",
		"url":        "u?t=" + url.QueryEscape(value),
		"base64":     base64.StdEncoding.EncodeToString([]byte(value)),
		"title":      "\x1b]0;" + value + "\x07 text",
	}
	for name, in := range cases {
		out := r.String(in)
		for _, leak := range []string{value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value))} {
			if strings.Contains(strip(out), leak) {
				t.Errorf("%s: leaked in %q", name, out)
			}
		}
		if !strings.Contains(out, Marker) {
			t.Errorf("%s: no marker in %q", name, out)
		}
	}
}

func strip(s string) string {
	return strings.NewReplacer("\x1b[31m", "", "\x1b[0m", "", "\r", "", "\n", "").Replace(s)
}

func TestRedactJSONEscapedSecret(t *testing.T) {
	secret := `pa"ss<w>rd`
	r := NewRedactor(secret)
	b, _ := json.Marshal(map[string]string{"k": secret})
	if got := r.String(string(b)); strings.Contains(got, `pa\"ss`) {
		t.Fatalf("leaked: %s", got)
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	_ = e.Encode(map[string]string{"k": secret})
	if got := r.String(buf.String()); strings.Contains(got, `pa\"ss`) {
		t.Fatalf("leaked unescaped-HTML form: %s", got)
	}
}

func TestRedactLeavesOtherTextAlone(t *testing.T) {
	r := NewRedactor(value)
	in := "ordinary \x1b[31mtext\x1b[0m\nsecond line\r\n"
	if got := r.String(in); got != in {
		t.Fatalf("got %q", got)
	}
}

func TestRedactOverlappingSecrets(t *testing.T) {
	r := NewRedactor("abcdef", "defghi", "abc")
	if got := r.String("x abcdefghi y"); got != "x "+Marker+" y" {
		t.Fatalf("got %q", got)
	}
}

func TestMultilineSecret(t *testing.T) {
	r := NewRedactor("line-one\nline-two\n")
	if got := r.String("key: line-one\nline-two\nafter"); strings.Contains(got, "line-one") || strings.Contains(got, "line-two") {
		t.Fatalf("got %q", got)
	}
}

func TestNilAndEmptyRedactor(t *testing.T) {
	var r *Redactor
	if r.Active() || r.String("a") != "a" {
		t.Fatal("nil redactor changed text")
	}
	if NewRedactor("", "\n").Active() {
		t.Fatal("blank secrets activated redaction")
	}
	var buf bytes.Buffer
	if r.Writer(&buf) == nil || r.PerCall(&buf) != &buf {
		t.Fatal("nil redactor's writers")
	}
}

func TestUnterminatedEscapeDoesNotHideASecret(t *testing.T) {
	r := NewRedactor(value)
	for _, in := range []string{"\x1b]0;" + value, "\x1b[" + value, "\x1b"} {
		if got := r.String(in); strings.Contains(got, value) {
			t.Errorf("%q leaked as %q", in, got)
		}
	}
}

func TestWriterEverySplit(t *testing.T) {
	r := NewRedactor(value)
	in := "before " + value[:5] + "\x1b[1m" + value[5:] + " middle " + value + " after-text\n"
	for i := 0; i <= len(in); i++ {
		var buf bytes.Buffer
		w := r.Writer(&buf)
		for _, part := range []string{in[:i], in[i:]} {
			if n, err := w.Write([]byte(part)); err != nil || n != len(part) {
				t.Fatalf("write: %d %v", n, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strip(buf.String()); strings.Contains(got, value) || strings.Count(got, Marker) != 2 || !strings.HasSuffix(got, " after-text") {
			t.Fatalf("split %d: %q", i, got)
		}
	}
}

func TestWriterByteAtATime(t *testing.T) {
	r := NewRedactor(value, "abab")
	var buf bytes.Buffer
	w := r.Writer(&buf)
	for _, c := range []byte("x abab " + value + " y") {
		if _, err := w.Write([]byte{c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "x "+Marker+" "+Marker+" y" {
		t.Fatalf("got %q", got)
	}
}

func TestWriterMatchEndingInItsOwnPrefixIsNotHalfReleased(t *testing.T) {
	r := NewRedactor("abab")
	var buf bytes.Buffer
	w := r.Writer(&buf)
	_, _ = w.Write([]byte("abab"))
	_ = w.Close()
	if got := buf.String(); got != Marker {
		t.Fatalf("got %q", got)
	}
}

func TestWriterCloseHidesATruncatedSecret(t *testing.T) {
	r := NewRedactor(value)
	var buf bytes.Buffer
	w := r.Writer(&buf)
	_, _ = w.Write([]byte("x " + value[:8]))
	_ = w.Close()
	if got := buf.String(); strings.Contains(got, value[:8]) || got != "x "+Marker {
		t.Fatalf("got %q", got)
	}
	buf.Reset()
	w = r.Writer(&buf)
	_, _ = w.Write([]byte("x hu"))
	_ = w.Close()
	if got := buf.String(); got != "x hu" {
		t.Fatalf("short ordinary tail: %q", got)
	}
}

func TestWriterBoundsAnUnterminatedEscape(t *testing.T) {
	r := NewRedactor(value)
	var buf bytes.Buffer
	w := r.Writer(&buf)
	_, _ = w.Write([]byte("\x1b]0;" + strings.Repeat("a", escapeLimit+10)))
	if buf.Len() == 0 {
		t.Fatal("an unterminated escape was held without limit")
	}
	_ = w.Close()
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("full") }

func TestWriterReportsFailure(t *testing.T) {
	r := NewRedactor(value)
	w := r.Writer(failWriter{})
	if _, err := w.Write([]byte("text")); err == nil {
		t.Fatal("write error dropped")
	}
	w = r.Writer(failWriter{})
	if err := w.Close(); err != nil {
		t.Fatalf("nothing held, %v", err)
	}
}

func TestPerCall(t *testing.T) {
	r := NewRedactor(value)
	var buf bytes.Buffer
	w := r.PerCall(&buf)
	line := "2026 token " + value + " ends a\n"
	if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("%d %v", n, err)
	}
	if got := buf.String(); got != "2026 token "+Marker+" ends a\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := r.PerCall(failWriter{}).Write([]byte("x")); err == nil {
		t.Fatal("error dropped")
	}
}

func TestEscapeForms(t *testing.T) {
	r := NewRedactor(value)
	cut := 6
	for _, esc := range []string{"\x1b]8;;\x1b\\", "\x1bM", "\x1b]0;t\x07"} {
		in := value[:cut] + esc + value[cut:]
		if got := r.String(in); strings.Contains(strip(got), value) || !strings.Contains(got, Marker) {
			t.Errorf("%q gave %q", esc, got)
		}
	}
	if escapeLen([]byte("\x1b]0;x\x1b")) != 0 || escapeLen([]byte("\x1b[12")) != 0 {
		t.Error("an unfinished escape was measured as finished")
	}
}

func TestInactiveWriterPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	w := NewRedactor().Writer(&buf)
	if _, err := w.Write([]byte("text")); err != nil || buf.String() != "text" {
		t.Fatalf("%q %v", buf.String(), err)
	}
}

func TestWriterCloseAfterUnfinishedEscapeHidesTheStart(t *testing.T) {
	r := NewRedactor(value)
	var buf bytes.Buffer
	w := r.Writer(&buf)
	_, _ = w.Write([]byte(value[:8] + "\x1b["))
	_ = w.Close()
	if strings.Contains(buf.String(), value[:8]) {
		t.Fatalf("got %q", buf.String())
	}
}

func TestEscapesWithIntermediateBytesAndControls(t *testing.T) {
	r := NewRedactor(value)
	for _, sep := range []string{"\x1b(B", "\x1b%G", "\x1b#8", "\x1bPq\x1b\\", "\x1b_x\x07", "\x0f", "\x00"} {
		in := "a " + value[:6] + sep + value[6:] + " z"
		if got := r.String(in); got != "a "+Marker+" z" {
			t.Errorf("%q gave %q", sep, got)
		}
		for i := 0; i <= len(in); i++ {
			var buf bytes.Buffer
			w := r.Writer(&buf)
			_, _ = w.Write([]byte(in[:i]))
			_, _ = w.Write([]byte(in[i:]))
			_ = w.Close()
			if got := buf.String(); got != "a "+Marker+" z" {
				t.Fatalf("%q split %d gave %q", sep, i, got)
			}
		}
	}
	for _, unfinished := range []string{"\x1b(", "\x1bP", "\x1b_abc", "\x1bPq\x1b"} {
		if escapeLen([]byte(unfinished)) != 0 {
			t.Errorf("%q measured as finished", unfinished)
		}
	}
	if escapeLen([]byte("\x1b(\x01")) != 1 {
		t.Error("a malformed escape must give back everything after ESC")
	}
}

func TestQuotedSecretsKeepJSONValid(t *testing.T) {
	for _, secret := range []string{`secret"`, `"secret"`, `"secret`, `back\`, `\lead`, `a"b\`} {
		b, _ := json.Marshal(map[string]string{"token": secret, "other": "kept"})
		got := NewRedactor(secret).String(string(b))
		var decoded map[string]string
		if err := json.Unmarshal([]byte(got), &decoded); err != nil {
			t.Errorf("%q: %v in %s", secret, err, got)
			continue
		}
		if decoded["token"] != Marker || decoded["other"] != "kept" {
			t.Errorf("%q: decoded %v", secret, decoded)
		}
	}
}

func TestJSONFormDoesNotEatADelimiter(t *testing.T) {
	r := NewRedactor(`secret"`)
	body := `{"a":"secret","b":1}`
	if got := r.JSON().String(body); got != body {
		t.Fatalf("got %s", got)
	}
	var nilRedactor *Redactor
	if nilRedactor.JSON() != nil {
		t.Fatal("nil must stay nil")
	}
}
