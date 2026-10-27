package route

import (
	"errors"
	"fmt"
	"io"
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

// Repeated run-state sections: only the marks the window needs are kept,
// and the oldest page reads from them.
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
	if len(tail.marks) > Window/markEvery+2 || tail.From() != 100 {
		t.Fatalf("%d marks, from %d", len(tail.marks), tail.From())
	}
	lines, err := tail.Page(101, 5)
	if err != nil || len(lines) != 1 || lines[0].Text != "r100" {
		t.Fatalf("%+v %v", lines, err)
	}
}

// A record longer than the read buffer is paged whole.
func TestPageOfALongRecord(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("y", 70000)
	body := "# R\n## Run state\n- 10:00 a\n- 10:01 " + long + "\n- 10:02 c\n"
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := tail.Page(3, 3)
	if err != nil || len(lines) != 3 || lines[1].Text != long || lines[2].Text != "c" {
		t.Fatalf("%d lines, %v", len(lines), err)
	}
}

// A line appended while a page reads: the page has all its records, but
// the file is no longer the snapshot, so it is refused.
func TestPageRefusesAnAppendDuringTheRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte("# R\n## Run state\n- 10:00 a\n- 10:01 b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRead = func() {
		f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
		_, _ = f.WriteString("- 10:02 c\n")
		_ = f.Close()
	}
	defer func() { beforeRead = func() {} }()
	if _, err := tail.Page(2, 2); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
}

// A route without run-state records: the window is empty and a grown
// file is read on.
func TestRouteWithoutRecords(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte("# R\n\n## Checklist\n\n- [ ] C1.1 one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("- [x] C1.2 two\n")
	_ = f.Close()
	next, err := LoadBounded(dir, 10, tail)
	if err != nil || next.Total != 0 || len(next.Route.Checklist) != 2 {
		t.Fatalf("%+v %v", next, err)
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// A page reads the window's run-state lines and the headers around them,
// never a Notes section between two run-state sections.
func TestPageSkipsNotesBetweenRunStateSections(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# R\n## Run state\n")
	for i := range 150 {
		fmt.Fprintf(&b, "- 10:00 a%03d\n", i)
	}
	b.WriteString("## Notes\n")
	for range 4 << 10 {
		b.WriteString(strings.Repeat("n", 255) + "\n")
	}
	b.WriteString("## Run state\n")
	for i := range 150 {
		fmt.Fprintf(&b, "- 10:01 b%03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	var read int64
	pageSource = func(r io.Reader) io.Reader { return countingReader{r, &read} }
	defer func() { pageSource = func(r io.Reader) io.Reader { return r } }()
	page, err := tail.Page(200, 100)
	if err != nil || len(page) != 100 || page[0].Text != "a100" || page[99].Text != "b049" {
		t.Fatalf("page of %d lines, %v", len(page), err)
	}
	if read >= 1<<20 {
		t.Errorf("a page read %d bytes across a 1 MB Notes section", read)
	}
}
