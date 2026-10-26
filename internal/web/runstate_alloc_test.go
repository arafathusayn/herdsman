package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// refreshAlloc is the bytes allocated by one refresh, averaged over n.
func refreshAlloc(f *fixture, n int) uint64 {
	f.hub.refresh(context.Background())
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	for range n {
		f.hub.refresh(context.Background())
	}
	runtime.ReadMemStats(&b)
	return (b.TotalAlloc - a.TotalAlloc) / uint64(n)
}

func writeRunState(t *testing.T, f *fixture, lines int) {
	t.Helper()
	var b strings.Builder
	b.WriteString(routeMD)
	for i := range lines {
		fmt.Fprintf(&b, "- 11:00 line %d %s\n", i, strings.Repeat("x", 200))
	}
	if err := os.WriteFile(filepath.Join(f.routeDir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A24: an unchanged route file is not read again: a refresh allocates the
// same whether the route holds 100 run-state lines or 10,000.
func TestRefreshAllocationIsFlatAsTheRouteGrows(t *testing.T) {
	f := newFixture(t)
	writeRunState(t, f, 100)
	small := refreshAlloc(f, 10)
	writeRunState(t, f, 10000)
	large := refreshAlloc(f, 10)
	if int64(large)-int64(small) > 256<<10 {
		t.Fatalf("a refresh allocates %d bytes at 100 lines and %d at 10,000", small, large)
	}
	// A change is still read: the newest line shows.
	writeRunState(t, f, 10001)
	f.hub.refresh(context.Background())
	rs := f.hub.Current().Routes[0].RunState
	if !strings.HasPrefix(rs[len(rs)-1].Text, "line 10000 ") {
		t.Fatalf("newest %q", rs[len(rs)-1].Text)
	}
}

// A24: a refresh after one appended line reads only that line: it costs
// the same at 100 lines and at 10,000; and a page of older lines costs the
// same too. Each cost is the least of three rounds in each of three
// fixtures: the counters are the process's, and a fixture's other
// goroutines allocate too.
func TestAppendedLineAndPageCostStayFlat(t *testing.T) {
	cost := map[int][2]uint64{}
	var f *fixture
	for _, n := range []int{150, 10000} {
		best := [2]uint64{^uint64(0), ^uint64(0)}
		for fixtures := range 9 {
			round := fixtures % 3
			if round == 0 {
				f = newFixture(t)
				writeRunState(t, f, n)
				f.hub.refresh(context.Background())
			}
			fh, err := os.OpenFile(filepath.Join(f.routeDir, route.RouteFile), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(fh, "- 12:00 appended %d %d\n", n, round)
			fh.Close()
			var refresh, page runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&refresh)
			f.hub.refresh(context.Background())
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			rv := f.hub.Current().Routes[0]
			if last := rv.RunState[len(rv.RunState)-1].Text; last != fmt.Sprintf("appended %d %d", n, round) || rv.OlderRunState+len(rv.RunState) != n+2+round {
				t.Fatalf("%d: last %q, total %d", n, last, rv.OlderRunState+len(rv.RunState))
			}
			runtime.GC()
			runtime.ReadMemStats(&page)
			lines, err := rv.tail.Page(rv.OlderRunState, runStateKept)
			var pageAfter runtime.MemStats
			runtime.ReadMemStats(&pageAfter)
			if err != nil || len(lines) != min(runStateKept, rv.OlderRunState) {
				t.Fatalf("%d: page %d %v", n, len(lines), err)
			}
			best = [2]uint64{min(best[0], after.TotalAlloc-refresh.TotalAlloc), min(best[1], pageAfter.TotalAlloc-page.TotalAlloc)}
		}
		cost[n] = best
	}
	t.Logf("A24 refresh after an append: %d bytes at 150 lines, %d at 10,000; newest older page: %d and %d", cost[150][0], cost[10000][0], cost[150][1], cost[10000][1])
	for i, what := range []string{"refresh after an append", "page"} {
		if int64(cost[10000][i])-int64(cost[150][i]) > 128<<10+raceSlack {
			t.Errorf("%s costs %d bytes at 150 lines and %d at 10,000", what, cost[150][i], cost[10000][i])
		}
	}
}
