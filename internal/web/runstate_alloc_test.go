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
