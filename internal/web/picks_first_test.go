package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// A30: "Yes" to the previous choice needs no probe: GET /api/catalog?probe=0
// offers the saved picks as the kinds, and a route that starts exactly the
// saved picks is planned from them while the catalog was never built.
func TestSavedPicksStartWithoutProbe(t *testing.T) {
	f := newRouteFixture(t)
	var calls atomic.Int32
	f.srv.catalog = newCatalogSource(fakeRunner(realOutputs(t), nil, &calls))
	st := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	if err := st.Save(
		choices.Pick{Role: "orchestrator", Runner: "herdr", Harness: "claude", Model: "fable", Effort: "medium"},
		choices.Pick{Role: "reviewer", Runner: "subagent", Harness: "codex", Model: "m-r", Effort: "low"},
	); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = st

	res := f.request(t, http.MethodGet, "/api/catalog?probe=0", "", f.authed)
	var c Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if !c.FromPicks || len(c.Kinds) != 1 || c.Kinds[0].Kind != "claude" || len(c.Kinds[0].Models) != 1 ||
		c.Kinds[0].Models[0].ID != "fable" || c.Kinds[0].Models[0].Efforts[0] != "medium" || !c.Kinds[0].Checkpointer {
		t.Fatalf("catalog from picks %+v", c)
	}
	if code, out := f.post(t, f.orchestrator()); code != 200 || !out.OK {
		t.Fatalf("%d %+v", code, out)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d probe commands ran", calls.Load())
	}
	// A request that is not the saved pick waits for the real catalog.
	req := f.orchestrator()
	req.Name, req.Effort = "orch-2", "high"
	if code, out := f.post(t, req); code != 200 || !out.OK {
		t.Fatalf("%d %+v", code, out)
	}
	if calls.Load() == 0 {
		t.Fatal("a new pick was started without the probe")
	}
}
