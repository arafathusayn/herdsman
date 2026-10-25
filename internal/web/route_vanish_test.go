package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// A route file that disappears between refreshes leaves the routes and the
// cache.
func TestVanishedRouteLeavesTheCache(t *testing.T) {
	f := newFixture(t)
	if len(f.hub.Current().Routes) != 1 {
		t.Fatal("no route")
	}
	if err := os.Remove(filepath.Join(f.routeDir, route.RouteFile)); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	if len(f.hub.Current().Routes) != 0 || len(f.hub.routes) != 0 {
		t.Fatalf("routes %d, cache %d", len(f.hub.Current().Routes), len(f.hub.routes))
	}
}

// When another section follows the run state, a page comes from a whole
// read; a file gone since the refresh answers 404.
func TestRunStatePageWhenNotLast(t *testing.T) {
	f := newFixture(t)
	body := routeMD + "- 10:01 second\n\n## Notes\n\nfree text\n"
	if err := os.WriteFile(filepath.Join(f.routeDir, route.RouteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	id := routeID(f.routeDir, "t-2026-10-24")
	res := f.request(t, http.MethodGet, "/api/route/runstate?route="+id+"&before=1", "", f.authed)
	var page struct {
		Lines []route.LogLine `json:"lines"`
		Older int             `json:"older"`
	}
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil || len(page.Lines) != 1 || page.Lines[0].Text != "started" || page.Older != 0 {
		t.Fatalf("%+v %v", page, err)
	}
	if err := os.Remove(filepath.Join(f.routeDir, route.RouteFile)); err != nil {
		t.Fatal(err)
	}
	if res := f.request(t, http.MethodGet, "/api/route/runstate?route="+id+"&before=1", "", f.authed); res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}
