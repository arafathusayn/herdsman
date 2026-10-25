package shnum

import "testing"

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
