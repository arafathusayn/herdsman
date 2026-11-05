package route_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/route"
)

func shortRoute(t *testing.T, dir string, n int) {
	t.Helper()
	var b strings.Builder
	b.WriteString(head)
	for i := range n {
		fmt.Fprintf(&b, "- 10:00 r%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A24: the state kept between refreshes does not grow with history: a
// refresh after one appended record allocates the same at 10,000 and
// 300,000 records, and the oldest page the view offers costs the same at
// 2,500, 10,000 and 300,000 (each past the 2,000-record window).
func TestRefreshAndPageFlatAtAnyHistory(t *testing.T) {
	sizes := []int{2500, 10000, 300000}
	refresh, page := map[int]uint64{}, map[int]uint64{}
	for _, n := range sizes {
		dir := t.TempDir()
		shortRoute(t, dir, n)
		tail, err := route.LoadBounded(dir, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		appendLine(t, dir, n)
		var next *route.Tail
		start := time.Now()
		refresh[n] = allocs(func() { next, err = route.LoadBounded(dir, 100, tail) })
		took := time.Since(start)
		if err != nil || next.Total != n+1 {
			t.Fatalf("%d: %v", n, err)
		}
		oldest := max(0, next.Total-2000)
		page[n] = allocs(func() { _, err = next.Page(oldest+100, 100) })
		if err != nil {
			t.Fatalf("%d: page %v", n, err)
		}
		t.Logf("A24 %d records: refresh after an append %d bytes in %s; oldest window page %d bytes", n, refresh[n], took, page[n])
	}
	// The window is full from 2,500 records on: the page costs the same
	// from there; a refresh's cost is the same from 10,000 records on (at
	// 2,500 the kept newest records are fewer and cheaper to copy).
	if int64(page[10000])-int64(page[2500]) > 16<<10 || int64(page[300000])-int64(page[2500]) > 16<<10 {
		t.Errorf("a page costs %d, %d and %d bytes at 2,500, 10,000 and 300,000 records", page[2500], page[10000], page[300000])
	}
	if d := int64(refresh[300000]) - int64(refresh[10000]); d > 16<<10 || d < -16<<10 {
		t.Errorf("a refresh costs %d bytes at 10,000 records and %d at 300,000", refresh[10000], refresh[300000])
	}
}

// A page reads the run state only: another section between run-state
// sections is not read, however long it is.
func TestPageSkipsOtherSections(t *testing.T) {
	cost := map[int]uint64{}
	for _, n := range []int{100, 10000} {
		dir := t.TempDir()
		var b strings.Builder
		b.WriteString(head)
		for i := range n {
			fmt.Fprintf(&b, "- 10:00 r%d\n", i)
		}
		b.WriteString("\n## Notes\n\n")
		for range n {
			b.WriteString(strings.Repeat("n", 200) + "\n")
		}
		b.WriteString("\n## Run state\n\n- 10:01 last\n")
		if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		tail, err := route.LoadBounded(dir, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		var lines []route.LogLine
		cost[n] = allocs(func() { lines, err = tail.Page(tail.Total, 2) })
		if err != nil || len(lines) != 2 || lines[1].Text != "last" || lines[0].Text != fmt.Sprintf("r%d", n-1) {
			t.Fatalf("%d: %+v %v", n, lines, err)
		}
	}
	if int64(cost[10000])-int64(cost[100]) > 64<<10 {
		t.Fatalf("a page costs %d bytes at 100 records and %d at 10,000", cost[100], cost[10000])
	}
}

// A file rewritten at the same size (the oldest record dropped, one
// appended) since the snapshot gives no page: the view reloads.
func TestPageRefusesARotatedFile(t *testing.T) {
	dir := t.TempDir()
	write := func(from int) {
		var b strings.Builder
		b.WriteString(head)
		for i := from; i < from+150; i++ {
			fmt.Fprintf(&b, "- 10:00 line %03d\n", i)
		}
		if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(0)
	tail, err := route.LoadBounded(dir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(1)
	if lines, err := tail.Page(140, 100); !errors.Is(err, route.ErrChanged) {
		t.Fatalf("rotated: %d lines, %v", len(lines), err)
	}
}

// A24 over time: 1000 appends, each with a refresh and a page, leave the
// heap where it was.
func TestAppendRefreshPageLoopIsFlat(t *testing.T) {
	dir := t.TempDir()
	shortRoute(t, dir, 3000)
	tail, err := route.LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	for i := range 1000 {
		appendLine(t, dir, 3000+i)
		if tail, err = route.LoadBounded(dir, 100, tail); err != nil {
			t.Fatal(err)
		}
		if _, err = tail.Page(tail.Total-100, 100); err != nil {
			t.Fatal(err)
		}
		if i == 99 {
			runtime.GC()
			runtime.ReadMemStats(&before)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if int64(after.HeapAlloc)-int64(before.HeapAlloc) > 256<<10 {
		t.Fatalf("heap grew from %d to %d over 900 appends", before.HeapAlloc, after.HeapAlloc)
	}
}
