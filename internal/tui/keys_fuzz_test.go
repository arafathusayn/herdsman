package tui

import (
	"reflect"
	"strings"
	"testing"
)

// mergePastes joins neighbouring pastes, which a split read may deliver apart.
func mergePastes(msgs []Msg) []Msg {
	var out []Msg
	for _, msg := range msgs {
		if p, ok := msg.(Paste); ok && len(out) > 0 {
			if last, ok := out[len(out)-1].(Paste); ok {
				out[len(out)-1] = Paste{Text: last.Text + p.Text}
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}

// FuzzParser holds the parser to three rules on any input: it never panics, the
// way a read splits the bytes never changes the result, and a flush leaves
// nothing waiting.
func FuzzParser(f *testing.F) {
	for _, seed := range []string{
		"", "a", "\x1b", "\x1b\x1b", "\x1b[", "\x1b[A", "\x1b[1;5C", "\x1bO", "\x1bOA", "\x1bO\x1b",
		"\x1b[3~", "\x1b[200~hello\x1b[201~", "\x1b[200~\x1b[20", "\x1b[201~", "\x1bx", "\x1b\r",
		"é", "\xc3", "\xff", "\xe2\x82", "\x1b\xc3", "\u009b", "\x01\x1f\x7f\t\r\n",
		"\x1b[" + strings.Repeat("1", 40) + "A", "\x1b[99999999999999999999~", "\x1b[1;x~", "\x1b[\x01",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var whole, split Parser
		got := whole.Feed(data)
		var parts []Msg
		for i := range data {
			parts = append(parts, split.Feed(data[i:i+1])...)
		}
		if !reflect.DeepEqual(mergePastes(got), mergePastes(parts)) {
			t.Fatalf("split changed the result for %q:\nwhole %#v\nsplit %#v", data, got, parts)
		}
		if whole.Pending() != split.Pending() {
			t.Fatalf("pending differs for %q", data)
		}
		whole.Flush()
		if whole.Pending() {
			t.Fatalf("flush left bytes waiting for %q", data)
		}
	})
}
