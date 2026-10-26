package daemon

import (
	"encoding/binary"
	"slices"
	"testing"
)

func TestParseProcArgs(t *testing.T) {
	blk := func(argc uint32, rest string) []byte {
		b := binary.LittleEndian.AppendUint32(nil, argc)
		return append(b, rest...)
	}
	p, ok := parseProcArgs(blk(2, "/bin/x\x00\x00\x00a b\x00c\x00K=v w\x00\x00junk"))
	if !ok || !slices.Equal(p.Args, []string{"a b", "c"}) || !slices.Equal(p.Env, []string{"K=v w"}) {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, b := range [][]byte{nil, blk(1, "/bin/x"), blk(3, "/bin/x\x00a\x00")} {
		if _, ok := parseProcArgs(b); ok {
			t.Errorf("%q parsed", b)
		}
	}
}
