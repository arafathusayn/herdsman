package machine

import (
	"errors"
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

// An unknown load or free memory makes the reading unknown.
func TestReadingUnknown(t *testing.T) {
	t.Cleanup(func() { readLoad, freePercent = ReadLoad, FreePercent })
	readLoad = func() (Load, error) { return Load{}, errors.New("no load") }
	if Reading() != "" {
		t.Error("an unknown load reads")
	}
	readLoad = ReadLoad
	freePercent = func() (int, error) { return 0, errors.New("no memory") }
	if Reading() != "" {
		t.Error("an unknown free memory reads")
	}
}
