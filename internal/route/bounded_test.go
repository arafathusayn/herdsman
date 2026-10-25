package route_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

const head = "# Route big\n\nOrchestrator: this session, pane w1:p1 (tab w1:t1).\n\n" +
	"## Task table\n\n| Task | Agent | Pane |\n| --- | --- | --- |\n| a | im-a | w1:p2 |\n\n## Run state\n\n"

func logLine(i int) string { return fmt.Sprintf("- 11:00 line %d %s\n", i, strings.Repeat("x", 200)) }

func writeLines(t *testing.T, dir string, n int) {
	t.Helper()
	var b strings.Builder
	b.WriteString(head)
	for i := range n {
		b.WriteString(logLine(i))
	}
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, dir string, i int) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, route.RouteFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(logLine(i)); err != nil {
		t.Fatal(err)
	}
}

func allocs(fn func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	fn()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// A24: a route file that only grew is read from where the last parse
// ended; the run state keeps the newest lines and counts all of them.
func TestLoadBoundedReadsOnlyTheGrowth(t *testing.T) {
	grow := map[int]uint64{}
	for _, n := range []int{100, 10000} {
		dir := t.TempDir()
		writeLines(t, dir, n)
		tl, err := route.LoadBounded(dir, 50, nil)
		if err != nil || tl.Total != n || len(tl.Route.RunState) != 50 || len(tl.Route.Tasks) != 1 {
			t.Fatalf("%d: %+v %v", n, tl, err)
		}
		appendLine(t, dir, n)
		var next *route.Tail
		grow[n] = allocs(func() { next, err = route.LoadBounded(dir, 50, tl) })
		if err != nil || next.Total != n+1 || !strings.HasPrefix(next.Route.RunState[49].Text, fmt.Sprintf("line %d ", n)) || len(next.Route.Tasks) != 1 {
			t.Fatalf("%d grown: %+v %v", n, next, err)
		}
		if len(tl.Route.RunState) != 50 || tl.Total != n {
			t.Fatal("the earlier parse was changed")
		}
	}
	if int64(grow[10000])-int64(grow[100]) > 64<<10 {
		t.Fatalf("an appended line costs %d bytes at 100 lines and %d at 10,000", grow[100], grow[10000])
	}
}

// A file that shrank, or was replaced at the same size, is parsed again.
func TestLoadBoundedReparsesAReplacedFile(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, 300)
	tl, _ := route.LoadBounded(dir, 50, nil)
	writeLines(t, dir, 10)
	tl2, err := route.LoadBounded(dir, 50, tl)
	if err != nil || tl2.Total != 10 || len(tl2.Route.RunState) != 10 {
		t.Fatalf("shrunk: %+v %v", tl2, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, route.RouteFile))
	same := strings.Replace(string(b), "line 9 ", "LINE 9 ", 1)
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(same), 0o644); err != nil {
		t.Fatal(err)
	}
	tl3, err := route.LoadBounded(dir, 50, tl2)
	if err != nil || !strings.HasPrefix(tl3.Route.RunState[9].Text, "LINE 9 ") {
		t.Fatalf("replaced: %+v %v", tl3, err)
	}
	// A partial last line is left for the next read.
	f, _ := os.OpenFile(filepath.Join(dir, route.RouteFile), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("- 12:00 half")
	f.Close()
	tl4, _ := route.LoadBounded(dir, 50, tl3)
	if tl4.Total != 10 {
		t.Fatalf("a partial line counted: %d", tl4.Total)
	}
	appendLine(t, dir, 99) // completes the half line
	tl5, _ := route.LoadBounded(dir, 50, tl4)
	if tl5.Total != 11 || !strings.HasPrefix(tl5.Route.RunState[10].Text, "half- 11:00 line 99 ") {
		t.Fatalf("total %d", tl5.Total)
	}
	if _, err := route.LoadBounded(filepath.Join(dir, "none"), 50, nil); err == nil {
		t.Error("a missing route loaded")
	}
}

// A24: older run-state lines are paged by reading backward from the end
// in bounded chunks: a page costs the same at 100 and 10,000 lines.
func TestPageRunStateReadsBackward(t *testing.T) {
	cost := map[int]uint64{}
	for _, n := range []int{150, 10000} {
		dir := t.TempDir()
		writeLines(t, dir, n)
		var lines []route.LogLine
		var err error
		cost[n] = allocs(func() { lines, err = route.PageRunState(dir, n-50, 50, n) })
		if err != nil || len(lines) != 50 || !strings.HasPrefix(lines[0].Text, fmt.Sprintf("line %d ", n-100)) || !strings.HasPrefix(lines[49].Text, fmt.Sprintf("line %d ", n-51)) {
			t.Fatalf("%d: %d lines %v", n, len(lines), err)
		}
		first, _ := route.PageRunState(dir, 3, 50, n)
		if len(first) != 3 || !strings.HasPrefix(first[0].Text, "line 0 ") {
			t.Fatalf("first page %+v", first)
		}
	}
	if int64(cost[10000])-int64(cost[150]) > 64<<10 {
		t.Fatalf("a page costs %d bytes at 150 lines and %d at 10,000", cost[150], cost[10000])
	}
}

func TestTailSectionAndFileList(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("00-route.md", head+"- 10:00 one\n")
	tail, err := route.LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tail.RunStateLast() {
		t.Fatal("the run state is the last section")
	}
	write("00-route.md", head+"- 10:00 one\n\n## Notes\n\nlater\n")
	tail, err = route.LoadBounded(dir, 100, tail)
	if err != nil {
		t.Fatal(err)
	}
	if tail.RunStateLast() {
		t.Fatal("a section after the run state")
	}
	write("notes.md", "n\n")
	names := []string{}
	for _, f := range route.ListFiles(dir) {
		names = append(names, f.Path)
	}
	if !strings.Contains(strings.Join(names, ","), "notes.md") {
		t.Fatalf("files %v lack notes.md", names)
	}
}
