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

// A24: the hub keeps the newest run-state lines only; older ones are paged
// from the route file on request, so a long route never grows the live heap.
func TestRunStateIsBoundedAndPaged(t *testing.T) {
	f := newFixture(t)
	var b strings.Builder
	b.WriteString(routeMD)
	for i := range 1000 {
		fmt.Fprintf(&b, "- 11:00 line %d %s\n", i, strings.Repeat("x", 2048))
	}
	if err := os.WriteFile(filepath.Join(f.routeDir, route.RouteFile), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	rv := f.hub.Current().Routes[0]
	if len(rv.RunState) != runStateKept || rv.OlderRunState != 1001-runStateKept {
		t.Fatalf("kept %d, older %d", len(rv.RunState), rv.OlderRunState)
	}
	if !strings.HasPrefix(rv.RunState[len(rv.RunState)-1].Text, "line 999 ") {
		t.Fatalf("newest line %q", rv.RunState[len(rv.RunState)-1].Text)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 50 {
		f.hub.refresh(context.Background())
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 1<<20 {
		t.Fatalf("heap grew %d bytes over 50 refreshes", grow)
	}

	// The page before the kept lines: the oldest lines are reachable.
	id := routeID(f.routeDir, "t-2026-10-24")
	res := f.request(t, http.MethodGet, fmt.Sprintf("/api/route/runstate?route=%s&before=%d", id, rv.OlderRunState), "", f.authed)
	var page struct {
		Lines []route.LogLine `json:"lines"`
		Older int             `json:"older"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&page) != nil {
		t.Fatalf("status %d", res.StatusCode)
	}
	if len(page.Lines) != runStateKept || page.Older != rv.OlderRunState-runStateKept {
		t.Fatalf("page %d older %d", len(page.Lines), page.Older)
	}
	res = f.request(t, http.MethodGet, "/api/route/runstate?route="+id+"&before=1", "", f.authed)
	if json.NewDecoder(res.Body).Decode(&page) != nil || len(page.Lines) != 1 || page.Lines[0].Text != "started" || page.Older != 0 {
		t.Fatalf("first page %+v", page)
	}
	for _, q := range []string{"route=nope&before=1", "route=" + id + "&before=x", "route=" + id + "&before=-1"} {
		if res := f.request(t, http.MethodGet, "/api/route/runstate?"+q, "", f.authed); res.StatusCode == http.StatusOK {
			t.Fatalf("%s accepted", q)
		}
	}
}
