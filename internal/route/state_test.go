package route

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func appendTo(t *testing.T, file, s string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func allocsOf(fn func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// A24 with repeated sections: every record in its own Run state section
// followed by a Notes section. What a refresh keeps and reads per append
// stays flat from 100 to 100,000 records.
func TestRepeatedSectionsStayFlat(t *testing.T) {
	cost := map[int]uint64{}
	for _, n := range []int{100, 100000} {
		dir := t.TempDir()
		file := filepath.Join(dir, RouteFile)
		var b strings.Builder
		b.WriteString("# R\n\n## Checklist\n\n- [ ] C1.1 one\n\n")
		for i := range n {
			fmt.Fprintf(&b, "## Run state\n\n- 10:00 r%d\n\n## Notes\n\nnote %d\n\n", i, i)
		}
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		tail, err := LoadBounded(dir, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		appendTo(t, file, fmt.Sprintf("## Run state\n\n- 10:01 r%d\n\n## Notes\n\nnote\n\n", n))
		var next *Tail
		start := time.Now()
		cost[n] = allocsOf(func() { next, err = LoadBounded(dir, 100, tail) })
		took := time.Since(start)
		if err != nil || next.Total != n+1 || next.Route.Checklist[0].ID != "C1.1" {
			t.Fatalf("%d: %v", n, err)
		}
		t.Logf("A24 repeated sections, %d records: one append costs %d bytes in %s; %d metadata ranges, %d window marks kept", n, cost[n], took, len(next.meta), len(next.marks))
		if len(next.meta) > 8 || len(next.marks) > Window/markEvery+2 {
			t.Errorf("%d records: %d metadata ranges and %d window marks kept", n, len(next.meta), len(next.marks))
		}
	}
	if int64(cost[100000])-int64(cost[100]) > 64<<10 {
		t.Errorf("one append costs %d bytes at 100 records and %d at 100,000", cost[100], cost[100000])
	}
}

// Run state sections with no record keep nothing.
func TestEmptySectionsKeepNoRange(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("# R\n\n## Run state\n\n- 10:00 a\n\n")
	for range 10001 {
		b.WriteString("## Run state\n\n")
	}
	b.WriteString("## Run state\n\n- 10:01 b\n")
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.marks) > 1 {
		t.Fatalf("%d marks for 2 records", len(tail.marks))
	}
	lines, err := tail.Page(2, 2)
	if err != nil || len(lines) != 2 || lines[0].Text != "a" || lines[1].Text != "b" {
		t.Fatalf("%+v %v", lines, err)
	}
}

// A same-size edit of a shown record far from the end is read: what the
// view shows and what a page reads come from one version of the file.
func TestEditInTheWindowIsRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	var b strings.Builder
	b.WriteString("# R\n\n## Run state\n\n")
	// Records of about 100 bytes: the edited one, 50 from the end, lies
	// more than 4 KiB before it.
	for i := range 3000 {
		fmt.Fprintf(&b, "- 10:00 r%04d %s\n", i, strings.Repeat("x", 86))
	}
	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(b.String(), "r2950", "R2950", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	next, err := LoadBounded(dir, 100, tail)
	if err != nil {
		t.Fatal(err)
	}
	shown := next.Route.RunState[2950-2900].Text
	page, err := next.Page(2951, 1)
	if err != nil || !strings.HasPrefix(shown, "R2950 ") || !strings.HasPrefix(page[0].Text, "R2950 ") {
		t.Fatalf("shown %q, page %+v, %v", shown, page, err)
	}
}

// A record that stops being one while a page reads gives ErrChanged in
// every layout, never a shifted page.
func TestPageRefusesAChangeDuringTheRead(t *testing.T) {
	layouts := map[string]func(i int) string{
		"one section": func(i int) string {
			if i == 0 {
				return "## Run state\n\n" + fmt.Sprintf("- 10:00 r%03d\n", i)
			}
			return fmt.Sprintf("- 10:00 r%03d\n", i)
		},
		"a section after": func(i int) string {
			s := fmt.Sprintf("- 10:00 r%03d\n", i)
			if i == 0 {
				s = "## Run state\n\n" + s
			}
			if i == 149 {
				s += "\n## Notes\n\nlater\n"
			}
			return s
		},
		"repeated sections": func(i int) string {
			return fmt.Sprintf("## Run state\n\n- 10:00 r%03d\n\n## Notes\n\nn\n\n", i)
		},
	}
	for name, rec := range layouts {
		dir := t.TempDir()
		file := filepath.Join(dir, RouteFile)
		var b strings.Builder
		b.WriteString("# R\n\n")
		for i := range 150 {
			b.WriteString(rec(i))
		}
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		tail, err := LoadBounded(dir, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		beforeRead = func() {
			_ = os.WriteFile(file, []byte(strings.Replace(b.String(), "- 10:00 r149", "x 10:00 r149", 1)), 0o644)
		}
		lines, err := tail.Page(150, 100)
		beforeRead = func() {}
		if !errors.Is(err, ErrChanged) {
			t.Errorf("%s: %d lines, %v", name, len(lines), err)
		}
	}
}
