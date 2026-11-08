package ws

import (
	"bytes"
	"errors"
	"testing"
	"unicode/utf8"
)

func TestParseHeaderRejectsWhatRFC6455Forbids(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"reserved bit 1", []byte{0xC1, 0x00}},
		{"reserved bit 2", []byte{0xA1, 0x00}},
		{"reserved bit 3", []byte{0x91, 0x00}},
		{"reserved data opcode", []byte{0x83, 0x00}},
		{"reserved control opcode", []byte{0x8B, 0x00}},
		{"fragmented control", []byte{0x09, 0x00}},
		{"long control", []byte{0x89, 126, 0x00, 0x7E}},
		{"16-bit length not minimal", []byte{0x82, 126, 0x00, 0x7D}},
		{"64-bit length not minimal", []byte{0x82, 127, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF}},
		{"64-bit length top bit set", []byte{0x82, 127, 0x80, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseHeader(tt.in)
			var pe *ProtocolError
			if !errors.As(err, &pe) || pe.Code != CloseProtocolError {
				t.Fatalf("parseHeader(%x) error = %v, want a protocol error", tt.in, err)
			}
		})
	}
}

func TestParseHeaderAsksForMoreBytes(t *testing.T) {
	for _, in := range [][]byte{
		{},
		{0x82},
		{0x82, 126, 0x01},
		{0x82, 127, 0, 0},
		{0x82, 0x81, 1, 2, 3},
	} {
		if _, _, err := parseHeader(in); !errors.Is(err, errShortHeader) {
			t.Fatalf("parseHeader(%x) error = %v, want errShortHeader", in, err)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	key := [4]byte{1, 2, 3, 4}
	for _, length := range []uint64{0, 1, 125, 126, 65535, 65536, 1 << 40} {
		for _, masked := range []bool{false, true} {
			buf := appendHeader(nil, true, OpBinary, masked, key, length)
			h, n, err := parseHeader(buf)
			if err != nil {
				t.Fatalf("length %d: %v", length, err)
			}
			if n != len(buf) {
				t.Fatalf("length %d: consumed %d of %d", length, n, len(buf))
			}
			if !h.fin || h.op != OpBinary || h.masked != masked || h.length != length {
				t.Fatalf("length %d: header = %+v", length, h)
			}
			if masked && h.key != key {
				t.Fatalf("length %d: key = %v", length, h.key)
			}
		}
	}
}

func TestMaskContinuesAcrossChunks(t *testing.T) {
	key := [4]byte{0xAA, 0xBB, 0xCC, 0xDD}
	whole := []byte("the quick brown fox")
	mask(whole, key, 0)

	chunked := []byte("the quick brown fox")
	pos := mask(chunked[:5], key, 0)
	mask(chunked[5:], key, pos)
	if !bytes.Equal(whole, chunked) {
		t.Fatalf("chunked mask = %x, want %x", chunked, whole)
	}
	mask(whole, key, 0)
	if string(whole) != "the quick brown fox" {
		t.Fatalf("masking twice = %q", whole)
	}
}

func TestOpcodeKinds(t *testing.T) {
	for _, op := range []Opcode{OpClose, OpPing, OpPong} {
		if !op.control() || !op.known() {
			t.Fatalf("opcode %d should be a known control opcode", op)
		}
	}
	for _, op := range []Opcode{OpContinuation, OpText, OpBinary} {
		if op.control() || !op.known() {
			t.Fatalf("opcode %d should be a known data opcode", op)
		}
	}
	if Opcode(3).known() || Opcode(0xF).known() {
		t.Fatal("reserved opcodes must be unknown")
	}
}

func TestUTF8ValidatorMatchesStandardLibrary(t *testing.T) {
	for _, s := range []string{
		"", "plain", "κόσμε", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xc0\xaf",
		"\xe0\x80\xaf", "\xf0\x80\x80\xaf", "\xf4\x8f\xbf\xbf", "\xe2\x82", "\xff",
	} {
		var v utf8Validator
		ok := v.feed([]byte(s)) && v.complete()
		if ok != utf8.ValidString(s) {
			t.Fatalf("validator(%q) = %v, want %v", s, ok, !ok)
		}
	}
}

func TestUTF8ValidatorSplitsAnywhere(t *testing.T) {
	s := []byte("a€𝄞é")
	for i := range s {
		var v utf8Validator
		if !v.feed(s[:i]) || !v.feed(s[i:]) || !v.complete() {
			t.Fatalf("split at %d rejected valid text", i)
		}
	}
	var v utf8Validator
	if !v.feed([]byte{0xE2, 0x82}) || v.complete() {
		t.Fatal("a truncated sequence is incomplete, not invalid")
	}
}

func TestCloseCodeValidity(t *testing.T) {
	valid := []uint16{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014, 3000, 3999, 4000, 4999}
	for _, code := range valid {
		if !validCloseCode(code) {
			t.Fatalf("code %d should be valid", code)
		}
	}
	invalid := []uint16{0, 999, 1004, 1005, 1006, 1015, 1016, 1100, 2000, 2999, 5000}
	for _, code := range invalid {
		if validCloseCode(code) {
			t.Fatalf("code %d should be invalid", code)
		}
	}
}

func TestErrorStrings(t *testing.T) {
	if got := (&CloseError{Code: 1000, Reason: "bye"}).Error(); got != "ws: peer closed: 1000 bye" {
		t.Fatalf("CloseError = %q", got)
	}
	if got := (&ProtocolError{Code: 1002, Reason: "bad"}).Error(); got != "ws: protocol error 1002: bad" {
		t.Fatalf("ProtocolError = %q", got)
	}
	if got := (&HandshakeError{Reason: "no"}).Error(); got != "ws: handshake: no" {
		t.Fatalf("HandshakeError = %q", got)
	}
}
