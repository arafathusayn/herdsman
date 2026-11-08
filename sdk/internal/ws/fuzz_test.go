package ws

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unicode/utf8"
)

// The seeds live in testdata/corpus/<target>/ as raw bytes, so they can be read
// and added to without Go's fuzz file encoding.
func addCorpus(f *testing.F, target string) {
	f.Helper()
	dir := filepath.Join("testdata", "corpus", target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Fatal(err)
	}
	if len(entries) == 0 {
		f.Fatalf("%s has no seeds", dir)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}

// A header that parses must re-encode to exactly the bytes it came from: the
// parser accepts one encoding per header.
func FuzzParseHeader(f *testing.F) {
	addCorpus(f, "FuzzParseHeader")
	f.Fuzz(func(t *testing.T, data []byte) {
		h, n, err := parseHeader(data)
		if err != nil {
			return
		}
		if n > len(data) {
			t.Fatalf("consumed %d of %d bytes", n, len(data))
		}
		if h.op.control() && (h.length > maxControlPayload || !h.fin) {
			t.Fatalf("accepted a bad control header: %+v", h)
		}
		if h.length>>63 != 0 {
			t.Fatalf("accepted a 64-bit length: %d", h.length)
		}
		if got := appendHeader(nil, h.fin, h.op, h.masked, h.key, h.length); !bytes.Equal(got, data[:n]) {
			t.Fatalf("re-encoded %x from %x", got, data[:n])
		}
	})
}

const (
	fuzzMaxMessage   = 4096
	fuzzMaxFragments = 8
)

// The first input byte picks the role; the rest is what the peer sends.
func FuzzReadMessage(f *testing.F) {
	addCorpus(f, "FuzzReadMessage")
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		client := data[0]&1 == 1
		stream := data[1:]
		s := newScript(stream)
		c := testConn(s, client, Options{
			MaxMessageSize: fuzzMaxMessage,
			MaxFragments:   fuzzMaxFragments,
			CloseTimeout:   time.Millisecond,
		})

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var final error
		for first := true; ; first = false {
			op, msg, err := c.ReadMessage()
			if first {
				if h, _, perr := parseHeader(stream); perr == nil && h.masked == client {
					wantProtocol(t, err, CloseProtocolError)
				}
			}
			if err != nil {
				final = err
				break
			}
			if len(msg) > fuzzMaxMessage {
				t.Fatalf("message of %d bytes passed the limit", len(msg))
			}
			if op != OpText && op != OpBinary {
				t.Fatalf("delivered opcode %d", op)
			}
			if op == OpText && !utf8.Valid(msg) {
				t.Fatalf("delivered invalid text %q", msg)
			}
		}
		runtime.ReadMemStats(&after)

		// Every message costs at most a small buffer plus its doubling growth,
		// so memory follows the input and never a claimed length.
		if grew, bound := after.TotalAlloc-before.TotalAlloc, uint64(1<<20+256*len(data)); grew > bound {
			t.Fatalf("allocated %d bytes for %d input bytes, bound %d", grew, len(data), bound)
		}
		if _, _, err := c.ReadMessage(); !errors.Is(err, final) {
			t.Fatalf("read after the end = %v, want %v", err, final)
		}
		if !s.closed {
			t.Fatal("connection left open after the final error")
		}
	})
}

// Whatever one role writes, the other must read back unchanged.
func FuzzRoundTrip(f *testing.F) {
	addCorpus(f, "FuzzRoundTrip")
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, client := range []bool{false, true} {
			w := newScript(nil)
			if err := testConn(w, client, Options{}).WriteMessage(OpBinary, data); err != nil {
				t.Fatal(err)
			}
			r := newScript(w.written())
			op, got, err := testConn(r, !client, Options{}).ReadMessage()
			if err != nil || op != OpBinary || !bytes.Equal(got, data) {
				t.Fatalf("client=%v: got %v %x %v, want %x", client, op, got, err, data)
			}
		}
	})
}

// The first input byte picks where the text is split across two fragments.
func FuzzUTF8(f *testing.F) {
	addCorpus(f, "FuzzUTF8")
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		text := data[1:]
		split := int(data[0]) % (len(text) + 1)
		var v utf8Validator
		got := v.feed(text[:split]) && v.feed(text[split:]) && v.complete()
		if want := utf8.Valid(text); got != want {
			t.Fatalf("validator(%q split %d) = %v, want %v", text, split, got, want)
		}
	})
}
