package shnum

import (
	"strconv"
	"testing"
)

func TestTest(t *testing.T) {
	for s, want := range map[string]int64{"30": 30, " 30": 30, "30 ": 30, "\t30\t": 30, "\n30": 30, "00": 0, "030": 30, "+30": 30, "-5": -5,
		"08": 8, "9223372036854775807": 9223372036854775807} {
		if n, ok := Test(s); !ok || n != want {
			t.Errorf("Test(%q) = %d %v, want %d", s, n, ok, want)
		}
	}
	for _, s := range []string{"", " ", "x", "30x", "3 0", "30\n", "+", "0x10", "1.5", "9223372036854775808", "+-5", "++5"} {
		if _, ok := Test(s); ok {
			t.Errorf("Test(%q) passed", s)
		}
	}
}

func FuzzTest(f *testing.F) {
	for _, s := range []string{"", "0", " 08 ", "-1", "+-1", "9223372036854775807", "9223372036854775808", "x", "1 2"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok := Test(s)
		if !ok {
			return
		}
		if m, ok2 := Test(strconv.FormatInt(n, 10)); !ok2 || m != n {
			t.Errorf("%q: %d does not read back", s, n)
		}
	})
}
