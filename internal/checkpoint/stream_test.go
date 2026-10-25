package checkpoint

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// esc spells every character of s as a \u escape.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

func readLines(t *testing.T, text string) Reading {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t")
	write(t, p, text)
	return ContextTokens(p)
}

// A broken line never takes the next line with it, even when the parse
// stopped on the newline itself; the following valid record counts and the
// compact goes out.
func TestStreamRecovery(t *testing.T) {
	u := func(n int) string {
		return fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d}}}`+"\n", n)
	}
	for _, broken := range []string{`{"a":1`, `[1`, `["x"`, `"abc`, `tru`, `{"a":`, `{"a"`, `{`, `-`, `1.`, `1e`, `"a\`, `"a\u12`, `{"a":1,`, `[1,`} {
		got := readLines(t, u(7)+broken+"\n"+u(300000))
		if got.String() != "300000" || got.Skipped != 1 {
			t.Errorf("%q then a record: %+v", broken, got)
		}
		// The same broken text at the end of the file, with no newline.
		got = readLines(t, u(7)+broken)
		if got.String() != "7" || got.Skipped != 1 {
			t.Errorf("%q at the end: %+v", broken, got)
		}
	}
	// Through a cycle: the default limit, and the compact goes out.
	r := newRig(t)
	r.agent("w9:p9", "claude", "id", "s")
	tr := filepath.Join(r.dir, "t.jsonl")
	write(t, tr, u(7)+`{"a":1`+"\n"+u(300000))
	r.mark = len(r.srv.Requests())
	if code := r.checkpointer(r.config(r.dir, map[string]string{"TRANSCRIPT": tr})).Once(context.Background()); code != 0 || r.prompts() != "/memory-with-dag Cap|/compact|" {
		t.Errorf("cycle: %d %q", code, r.prompts())
	}
}

// Member names are matched as whole names at their own depth: a top-level
// member named like a nested path, or an empty name, is not the field.
func TestStreamPaths(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"assistant","message.usage.input_tokens":1,"message":{"usage":{"input_tokens":300000}}}`:               "300000",
		`{"type":"assistant","message":{"usage":{"input_tokens":300000}},"message.usage.input_tokens":1}`:               "300000",
		`{"type":"assistant","":false,"message":{"usage":{"input_tokens":300000}}}`:                                     "300000",
		`{"type":"assistant","message":{"usage":{"input_tokens":300000,"":{"input_tokens":1}}}}`:                        "300000",
		`{"type":"assistant","message":{"usage.input_tokens":1,"usage":{"input_tokens":300000}}}`:                       "300000",
		`{"type":"assistant","x":{"message":{"usage":{"input_tokens":1}}},"message":{"usage":{"input_tokens":300000}}}`: "300000",
		// A repeated member replaces the whole earlier one.
		`{"type":"assistant","message":{"usage":{"input_tokens":300000}},"message":{"usage":{}}}`:            "0",
		`{"type":"assistant","message":{"usage":{"input_tokens":300000}},"message":"x"}`:                     "",
		`{"type":"assistant","message":{"usage":{"input_tokens":1}},"message":{"usage":{"input_tokens":2}}}`: "2",
		// An empty array and arrays of values are passed over.
		`{"type":"assistant","a":[],"b":[[],{},"s",1,null],"message":{"usage":{"input_tokens":5}}}`: "5",
	} {
		if got := readLines(t, in+"\n"); got.String() != want || got.Skipped != 0 {
			t.Errorf("%s: %+v want %q", in, got, want)
		}
	}
}

// Escaped spellings of the names and values read as the names.
func TestStreamEscapes(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"assistant","message":{"usage":{"` + esc("input_tokens") + `":300000}}}`:                                                                            "300000",
		`{"` + esc("type") + `":"` + esc("assistant") + `","` + esc("message") + `":{"` + esc("usage") + `":{"` + esc("cache_creation_input_tokens") + `":300000}}}`: "300000",
		`{"type":"event_msg","payload":{"type":"` + esc("token_count") + `","info":{"` + esc("last_token_usage") + `":{"` + esc("input_tokens") + `":300000}}}}`:     "300000",
	} {
		if got := readLines(t, in+"\n"); got.String() != want || got.Skipped != 0 {
			t.Errorf("%s: %+v want %q", in, got, want)
		}
	}
	// A spelling past the kept bound never matches, and a long string that
	// ends with the file is an unreadable line.
	long := `{"type":"assistant","message":{"usage":{"` + esc(strings.Repeat("i", 65)) + `":1,"input_tokens":4}}}`
	if got := readLines(t, long+"\n"); got.String() != "4" {
		t.Errorf("long key: %+v", got)
	}
	if got := readLines(t, `{"a":"`+strings.Repeat("x", 1000)); got.Skipped != 1 {
		t.Errorf("open long string: %+v", got)
	}
	if got := readLines(t, `{"a":"`+strings.Repeat("x", 1000)+"\n"+`{"type":"assistant","message":{"usage":{"input_tokens":3}}}`); got.String() != "3" || got.Skipped != 1 {
		t.Errorf("long string cut by a newline: %+v", got)
	}
}

// A folder opens but cannot be read: no reading.
func TestStreamUnreadable(t *testing.T) {
	if got := ContextTokens(t.TempDir()); got.OK || got.Skipped != 0 {
		t.Errorf("folder: %+v", got)
	}
}
