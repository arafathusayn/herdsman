package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

func writeLongRunState(t *testing.T, f *fixture, lines, width int, after string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(routeMD)
	for i := range lines {
		fmt.Fprintf(&b, "- 11:00 line %d %s\n", i, strings.Repeat("x", width))
	}
	b.WriteString(after)
	if err := os.WriteFile(filepath.Join(f.routeDir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

type runStatePage struct {
	Lines []route.LogLine `json:"lines"`
	Older int             `json:"older"`
}

func pageAt(t *testing.T, f *fixture, before int) (int, runStatePage) {
	t.Helper()
	id := f.hub.Current().Routes[0].ID
	res := f.request(t, http.MethodGet, fmt.Sprintf("/api/route/runstate?route=%s&before=%d", id, before), "", f.authed)
	defer res.Body.Close()
	var p runStatePage
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode, p
}

// A24: the oldest page costs the same at 100 lines and at 10,000, whether
// the run state is the last section or another section follows it.
func TestOldestPageCostIsFlat(t *testing.T) {
	for _, after := range []string{"", "\n## Notes\n\nlater\n"} {
		cost := map[int]uint64{}
		for _, n := range []int{100, 10000} {
			f := newFixture(t)
			writeLongRunState(t, f, n, 200, after)
			f.hub.refresh(context.Background())
			pageAt(t, f, 1)
			var a, b runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&a)
			code, p := pageAt(t, f, 1)
			runtime.ReadMemStats(&b)
			// The fixture's own "started" line is index 0.
			if code != http.StatusOK || len(p.Lines) != 1 || p.Lines[0].Text != "started" || p.Older != 0 {
				t.Fatalf("after %q, %d lines: %d %+v", after, n, code, p)
			}
			cost[n] = b.TotalAlloc - a.TotalAlloc
		}
		if int64(cost[10000])-int64(cost[100]) > 256<<10 {
			t.Errorf("after %q: the oldest page costs %d bytes at 100 lines and %d at 10,000", after, cost[100], cost[10000])
		}
	}
}

// A page is read from the snapshot the hub showed: a line appended since
// does not shift it.
func TestPageIsAnchoredToTheSnapshot(t *testing.T) {
	f := newFixture(t)
	writeLongRunState(t, f, 150, 2048, "")
	f.hub.refresh(context.Background())
	fh, err := os.OpenFile(filepath.Join(f.routeDir, route.RouteFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(fh, "- 12:00 appended\n")
	fh.Close()
	// Index 0 is "started"; "line k" is index k+1.
	code, p := pageAt(t, f, 141)
	if code != http.StatusOK || len(p.Lines) != 100 || p.Older != 41 {
		t.Fatalf("%d: %d lines, older %d", code, len(p.Lines), p.Older)
	}
	if first, last := p.Lines[0].Text, p.Lines[99].Text; !strings.HasPrefix(first, "line 40 ") || !strings.HasPrefix(last, "line 139 ") {
		t.Fatalf("page %q .. %q", first[:8], last[:9])
	}
}

// A file rewritten shorter since the snapshot gives no page from the wrong
// place: the app is told to reload.
func TestPageAfterTheFileShrinks(t *testing.T) {
	f := newFixture(t)
	writeLongRunState(t, f, 300, 200, "")
	f.hub.refresh(context.Background())
	writeLongRunState(t, f, 10, 200, "")
	if code, p := pageAt(t, f, 150); code != http.StatusConflict {
		t.Fatalf("%d %+v", code, p)
	}
}
