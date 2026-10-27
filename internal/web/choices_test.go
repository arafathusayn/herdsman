package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// A30: a route that starts saves the picks it launched with, per role, and
// the catalog offers them to the form as the previous choice.
func TestRouteSavesPicksForThePreviousChoice(t *testing.T) {
	f := newRouteFixture(t)
	store := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	f.srv.cfg.Choices = store

	// A route that fails saves nothing.
	f.failAt, f.failCode = "tab.create", "boom"
	if code, _ := f.post(t, f.adjutant()); code == 200 {
		t.Fatal("the failing route succeeded")
	}
	if got, _ := store.Load(); len(got.Picks) != 0 {
		t.Fatalf("a failed route saved %+v", got.Picks)
	}
	f.failAt = ""

	req := f.adjutant()
	if code, res := f.post(t, req); code != 200 || !res.OK {
		t.Fatalf("status %d: %+v", code, res)
	}
	got, err := store.Load()
	if err != nil || len(got.Picks) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	adj, orch := got.Picks[0], got.Picks[1]
	if adj.Role != "adjutant" || adj.Runner != "herdr" || adj.Harness != req.Kind || adj.Model != req.Model || adj.Effort != req.Effort || adj.Route != req.RouteName {
		t.Errorf("adjutant pick %+v for %+v", adj, req)
	}
	if orch.Role != "orchestrator" || orch.Harness != req.OrchKind || orch.Model != req.OrchModel || orch.Effort != req.OrchEffort {
		t.Errorf("orchestrator pick %+v", orch)
	}

	res := f.request(t, http.MethodGet, "/api/catalog", "", f.authed)
	var c Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil || len(c.Previous) != 2 || c.Previous[0] != adj {
		t.Fatalf("catalog previous = %+v, %v", c.Previous, err)
	}

	// An unreadable store offers no previous choice; it fails nothing.
	if err := os.WriteFile(store.Path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = f.request(t, http.MethodGet, "/api/catalog", "", f.authed)
	c = Catalog{}
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil || c.Previous == nil || len(c.Previous) != 0 {
		t.Fatalf("catalog previous = %+v, %v", c.Previous, err)
	}
	if code, res := f.post(t, f.adjutant()); code != 200 || !res.OK {
		t.Fatalf("a bad store failed the route: %d %+v", code, res)
	}
}

// An orchestrator route saves the orchestrator's pick only.
func TestOrchestratorRouteSavesItsPick(t *testing.T) {
	f := newRouteFixture(t)
	store := &choices.Store{Path: filepath.Join(t.TempDir(), "choices.json")}
	f.srv.cfg.Choices = store
	req := f.orchestrator()
	if code, res := f.post(t, req); code != 200 || !res.OK {
		t.Fatalf("status %d: %+v", code, res)
	}
	got, _ := store.Load()
	if len(got.Picks) != 1 || got.Picks[0].Role != "orchestrator" || got.Picks[0].Harness != req.Kind || got.Picks[0].Route != "" {
		t.Fatalf("%+v", got)
	}
}

// A store that cannot be written loses the picks, never the route.
func TestUnwritableStoreKeepsTheRoute(t *testing.T) {
	f := newRouteFixture(t)
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = &choices.Store{Path: filepath.Join(file, "choices.json")}
	if code, res := f.post(t, f.orchestrator()); code != 200 || !res.OK {
		t.Fatalf("status %d: %+v", code, res)
	}
}
