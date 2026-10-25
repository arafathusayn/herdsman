package machine

import (
	"encoding/binary"
	"testing"
)

func TestParseLoadavg(t *testing.T) {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 2048)
	binary.LittleEndian.PutUint32(b[4:], 1024)
	binary.LittleEndian.PutUint32(b[8:], 512)
	binary.LittleEndian.PutUint64(b[16:], 2048)
	l, err := parseLoadavg(b)
	if err != nil || l != (Load{1, 0.5, 0.25}) {
		t.Fatalf("load %v %v", l, err)
	}
	if _, err := parseLoadavg(b[:10]); err == nil {
		t.Fatal("short")
	}
	if _, err := parseLoadavg(make([]byte, 24)); err == nil {
		t.Fatal("no scale")
	}
}
