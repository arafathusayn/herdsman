package route

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file cut between a page's snapshot check and its reads gives
// ErrChanged, not a page from the wrong bytes.
func TestPageOfAFileCutWhileReading(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# Route\n\n## Run state\n\n")
	for i := range 200 {
		fmt.Fprintf(&b, "- 10:00 r%d\n", i)
	}
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRead = func() { _ = os.Truncate(file, 10) }
	defer func() { beforeRead = func() {} }()
	if _, err := tail.Page(150, 100); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
}

// A page whose oldest record is the first line of a run-state range.
func TestPageStartsAtARangeStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte("# R\n## Run state\n- 10:00 a\n- 10:01 b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := tail.Page(2, 2)
	if err != nil || len(lines) != 2 || lines[0].Text != "a" || lines[1].Text != "b" {
		t.Fatalf("%+v %v", lines, err)
	}
}

// Records rewritten in place at the same size and time between the check
// and the read give ErrChanged, not a short page.
func TestPageOfAFileEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	body := "# R\n## Run state\n- 10:00 a\n- 10:01 b\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRead = func() {
		_ = os.WriteFile(file, []byte(strings.ReplaceAll(body, "- 10:0", "x 10:0")), 0o644)
	}
	defer func() { beforeRead = func() {} }()
	if _, err := tail.Page(2, 2); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
}

// Repeated run-state sections: only the ranges that hold the newest
// Window records are kept, and the oldest page reads from them.
func TestWindowKeepsOnlyTheNewestRanges(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# R\n")
	for i := range Window + 100 {
		fmt.Fprintf(&b, "## Run state\n- 10:00 r%d\n## Notes\nx\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.runs) != Window || tail.From() != 100 {
		t.Fatalf("%d ranges, from %d", len(tail.runs), tail.From())
	}
	lines, err := tail.Page(101, 5)
	if err != nil || len(lines) != 1 || lines[0].Text != "r100" {
		t.Fatalf("%+v %v", lines, err)
	}
}
