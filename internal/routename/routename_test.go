package routename

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	for _, s := range []string{"r", "v1.2", "Web", "1x", "state", "alerts", "a_b-c", strings.Repeat("a", 64)} {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", ".r", "-r", "a..b", "a/b", "a b", strings.Repeat("a", 65), "é"} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}
