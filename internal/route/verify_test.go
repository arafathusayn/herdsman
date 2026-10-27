package route

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cpuNow is this process's user and system CPU time.
func cpuNow() time.Duration {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

// notesRoute is a route with one or two records and rows Notes lines of
// 1 KiB: after the run state, or with a second run state after them.
func notesRoute(t *testing.T, dir, layout string, rows int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# R\n## Run state\n- 10:00 first\n## Notes\n")
	row := strings.Repeat("x", 1023) + "\n"
	for range rows {
		b.WriteString(row)
	}
	if layout == "repeated" {
		b.WriteString("## Run state\n- 10:00 last\n## Notes\n")
	}
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// A24: a refresh after an append costs the same CPU whatever the size of
// the other sections: 8 MiB and 32 MiB of Notes, in both layouts.
func TestRefreshCostDoesNotGrowWithNotes(t *testing.T) {
	for _, layout := range []string{"nonfinal", "repeated"} {
		cost := map[int]time.Duration{}
		for _, rows := range []int{8 << 10, 32 << 10} {
			dir := t.TempDir()
			file := notesRoute(t, dir, layout, rows)
			tail, err := LoadBounded(dir, 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			best := time.Hour
			for range 7 {
				appendTo(t, file, "note\n")
				start := cpuNow()
				tail, err = LoadBounded(dir, 100, tail)
				best = min(best, cpuNow()-start)
				if err != nil {
					t.Fatal(err)
				}
			}
			cost[rows] = best
		}
		t.Logf("%s: refresh CPU %v at 8 MiB, %v at 32 MiB of Notes", layout, cost[8<<10], cost[32<<10])
		if cost[32<<10] > 2*cost[8<<10]+time.Millisecond {
			t.Errorf("%s: refresh CPU grows with the Notes: %v at 8 MiB, %v at 32 MiB", layout, cost[8<<10], cost[32<<10])
		}
	}
}

// A page across a 10 MB Notes line holds a fixed amount of it, not the
// line.
func TestPageOfALongNotesLineIsBounded(t *testing.T) {
	dir := t.TempDir()
	body := "# R\n## Run state\n- 10:00 first\n## Notes\n" + strings.Repeat("x", 10<<20) + "\n## Run state\n- 10:01 last\n"
	if err := os.WriteFile(filepath.Join(dir, RouteFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	var page []LogLine
	cost := allocsOf(func() { page, err = tail.Page(2, 2) })
	if err != nil || len(page) != 2 || page[0].Text != "first" || page[1].Text != "last" {
		t.Fatalf("page %v, %v", page, err)
	}
	if cost > 1<<20 {
		t.Errorf("a page across a 10 MB Notes line allocated %d bytes", cost)
	}
}

// A file edited in place with its time put back is not the snapshot: a
// page that ends before the snapshot's total is refused too.
func TestPageRefusesAnEditWithItsTimeRestored(t *testing.T) {
	for _, layout := range []string{"single", "nonfinal", "repeated"} {
		t.Run(layout, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, RouteFile)
			var b strings.Builder
			b.WriteString("# R\n## Run state\n")
			for i := range 150 {
				if layout == "repeated" && i == 75 {
					b.WriteString("## Notes\nx\n## Run state\n")
				}
				fmt.Fprintf(&b, "- 10:00 r%03d\n", i)
			}
			if layout == "nonfinal" {
				b.WriteString("## Notes\nx\n")
			}
			body := b.String()
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			tail, err := LoadBounded(dir, 100, nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeRead = func() {
				_ = os.WriteFile(file, []byte(strings.Replace(body, "- 10:00 r001", "x 10:00 r001", 1)), 0o600)
				_ = os.Chtimes(file, tail.info.ModTime(), tail.info.ModTime())
			}
			defer func() { beforeRead = func() {} }()
			page, err := tail.Page(140, 100)
			if !errors.Is(err, ErrChanged) {
				t.Errorf("got %d lines and %v, want ErrChanged", len(page), err)
			}
		})
	}
}

// A Notes line edited into a pane line, with an append: the refresh finds
// the pane, as a full parse does.
func TestPaneAddedInNotesIsFound(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	old := "ordinary note padded.........."
	repl := "Orchestrator: pane wS:p1" + strings.Repeat(" ", len(old)-len("Orchestrator: pane wS:p1"))
	var b strings.Builder
	b.WriteString("# R\n## Notes\n" + old + "\n## Run state\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "- 10:00 r%04d\n", i)
	}
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(body, old, repl, 1)+"- 10:01 appended\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := LoadBounded(dir, 100, tail)
	if err != nil {
		t.Fatal(err)
	}
	if next.Route.OrchestratorPane != "wS:p1" {
		t.Errorf("refresh pane %q, want wS:p1", next.Route.OrchestratorPane)
	}
}

// A record that set a pane is metadata too: editing it outside the window
// changes the pane.
func TestPaneEditInAnOldRecordIsFound(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	var b strings.Builder
	b.WriteString("# R\n## Run state\n- 10:00 Orchestrator: pane wS:p1\n")
	for i := 1; i < 3000; i++ {
		fmt.Fprintf(&b, "- 10:00 r%04d\n", i)
	}
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Route.OrchestratorPane != "wS:p1" {
		t.Fatalf("pane %q", tail.Route.OrchestratorPane)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(body, "wS:p1", "wS:p2", 1)+"- 10:01 appended\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := LoadBounded(dir, 100, tail)
	if err != nil {
		t.Fatal(err)
	}
	if next.Route.OrchestratorPane != "wS:p2" {
		t.Errorf("refresh pane %q, want wS:p2", next.Route.OrchestratorPane)
	}
}

// In a large file, an edit far from the end that no tracked line shows is
// found within one verification cycle of refreshes, and the route is then
// what a full parse gives.
func TestOldEditIsFoundWithinACycle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	old := "ordinary note padded.........."
	repl := "Orchestrator: pane wS:p9" + strings.Repeat(" ", len(old)-len("Orchestrator: pane wS:p9"))
	var b strings.Builder
	b.WriteString("# R\n## Notes\n")
	for range 6 << 10 {
		b.WriteString(strings.Repeat("y", 1023) + "\n")
	}
	b.WriteString(old + "\n")
	for range 2 << 10 {
		b.WriteString(strings.Repeat("y", 1023) + "\n")
	}
	b.WriteString("## Run state\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "- 10:00 r%04d\n", i)
	}
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(repl), int64(strings.Index(body, old))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cycle := len(body)/(1<<20)/2 + 2
	for i := range cycle {
		appendTo(t, file, fmt.Sprintf("- 10:01 more %d\n", i))
		if tail, err = LoadBounded(dir, 100, tail); err != nil {
			t.Fatal(err)
		}
	}
	full, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Route.OrchestratorPane != "wS:p9" || tail.Total != full.Total ||
		!slices.Equal(tail.Route.RunState, full.Route.RunState) {
		t.Errorf("after %d refreshes: pane %q, total %d; a full parse: pane %q, total %d",
			cycle, tail.Route.OrchestratorPane, tail.Total, full.Route.OrchestratorPane, full.Total)
	}
}

// A file of more empty run-state sections than the window keeps ranges
// for is checked as one range: an edit in it is still found.
func TestManyEmptySectionsAreCheckedWhole(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	var b strings.Builder
	b.WriteString("# R\n## Run state\n- 10:00 first\n")
	for range maxSpans + 10 {
		b.WriteString("## Run state\n## Notes\nx\n")
	}
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.win) > maxSpans {
		t.Fatalf("%d window ranges", len(tail.win))
	}
	appendTo(t, file, "## Run state\n- 10:01 second\n")
	if tail, err = LoadBounded(dir, 100, tail); err != nil || tail.Total != 2 {
		t.Fatalf("total %d, %v", tail.Total, err)
	}
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("FIRST"), int64(strings.Index(body, "first"))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	appendTo(t, file, "- 10:02 third\n")
	if tail, err = LoadBounded(dir, 100, tail); err != nil || tail.Route.RunState[0].Text != "FIRST" {
		t.Fatalf("%v, %v", tail.Route.RunState, err)
	}
}

// A file cut inside a long line while a page reads it is refused.
func TestPageOfAFileCutInALongLine(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	body := "# R\n## Run state\n- 10:00 a\n## Notes\n" + strings.Repeat("z", 200<<10) + "\n## Run state\n- 10:01 b\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRead = func() { _ = os.Truncate(file, int64(len(body)-(100<<10))) }
	defer func() { beforeRead = func() {} }()
	if _, err := tail.Page(2, 2); !errors.Is(err, ErrChanged) {
		t.Errorf("got %v, want ErrChanged", err)
	}
}

// Reads past the end and a saved state that is not one are safe: a range
// past the end hashes what is there, and a bad state starts empty.
func TestHashHelpersAtTheEdges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 16)
	if got, want := sumOf(f, 0, 100, buf), sumOf(f, 0, 3, buf); !slices.Equal(got, want) {
		t.Errorf("a range past the end: %x, want %x", got, want)
	}
	if got := restore([]byte("not a state")).Sum(nil); !slices.Equal(got, sumOf(f, 0, 0, buf)) {
		t.Errorf("a bad state: %x", got)
	}
	if !changeTime(fakeInfo{}).IsZero() {
		t.Error("a file without system data has a change time")
	}
}

type fakeInfo struct{ os.FileInfo }

func (fakeInfo) Sys() any { return nil }

// After a change a parse owes a cycle of block checks; loads of the same
// file pay it two blocks at a time; a full parse owes none.
func TestBlockChecksOwedAfterAChange(t *testing.T) {
	dir := t.TempDir()
	file := notesRoute(t, dir, "nonfinal", 5<<10)
	tail, err := LoadBounded(dir, 100, nil)
	if err != nil || tail.Unverified() {
		t.Fatalf("a full parse owes checks: %v", err)
	}
	appendTo(t, file, "note\n")
	if tail, err = LoadBounded(dir, 100, tail); err != nil || !tail.Unverified() {
		t.Fatalf("a change owes no checks: %v", err)
	}
	loads := 0
	for tail.Unverified() {
		if tail, err = LoadBounded(dir, 100, tail); err != nil {
			t.Fatal(err)
		}
		if loads++; loads > 5 {
			t.Fatalf("still owed after %d loads of a 5 MiB file", loads)
		}
	}
	if loads != 2 {
		t.Errorf("%d loads paid a 5 MiB file's cycle, want 2", loads)
	}
}

// The history generation stays while the file only grows, so the browser
// keeps the pages it loaded, and changes once an edit of an old record is
// found, so it drops them.
func TestHistoryChangesOnAnEditOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, RouteFile)
	var b strings.Builder
	b.WriteString("# R\n## Run state\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "- 10:00 r%04d\n", i)
	}
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := LoadBounded(dir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	appendTo(t, file, "- 10:01 more\n")
	grown, err := LoadBounded(dir, 100, first)
	if err != nil {
		t.Fatal(err)
	}
	if first.History() == "" || grown.History() != first.History() {
		t.Fatalf("after an append: %q, before: %q", grown.History(), first.History())
	}
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("- 10:00 e2500"), int64(strings.Index(body, "- 10:00 r2500"))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tail := grown
	for i := range 3 {
		appendTo(t, file, fmt.Sprintf("- 10:02 again %d\n", i))
		if tail, err = LoadBounded(dir, 100, tail); err != nil {
			t.Fatal(err)
		}
	}
	if tail.History() == first.History() {
		t.Fatalf("the edit of r2500 kept the history %q", tail.History())
	}
}
