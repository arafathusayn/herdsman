package machine

import (
	"regexp"
	"testing"
)

func TestReading(t *testing.T) {
	r := Reading()
	if !regexp.MustCompile(`^[0-9]+\.[0-9]{2} [0-9]+$`).MatchString(r) {
		t.Fatalf("reading %q", r)
	}
	l, err := ReadLoad()
	if err != nil || l[1] < 0 {
		t.Fatalf("load %v %v", l, err)
	}
	if CPUs() < 1 {
		t.Fatal("no cores")
	}
	if FormatLoad(1.234) != "1.23" {
		t.Fatal("format")
	}
}
