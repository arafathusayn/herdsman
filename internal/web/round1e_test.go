package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// A30: reuse is per role. A saved adjutant pick starts as saved, even when
// the probed list lacks its model, while the orchestrator is a fresh pick
// checked against the probed list.
func TestSavedRoleAndFreshRoleTogether(t *testing.T) {
	f := newRouteFixture(t)
	f.srv.catalog = newCatalogSource(fakeRunner(realOutputs(t), nil, nil))
	st := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	if err := st.Save(choices.Pick{Role: "adjutant", Runner: "herdr", Harness: "codex", Model: "codex-gone-1", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = st
	req := f.adjutant()
	req.Kind, req.Model, req.Effort = "codex", "codex-gone-1", "low"
	req.OrchKind, req.OrchModel, req.OrchEffort = "claude", "fable", "medium"
	if code, out := f.post(t, req); code != 200 || !out.OK {
		t.Fatalf("%d %+v", code, out)
	}
	// The fresh role is still checked against the probed list.
	req.Name, req.OrchName, req.RouteName = "adj-2", "orch-2", "route-two"
	req.OrchKind, req.OrchModel, req.OrchEffort = "codex", "codex-gone-1", "low"
	if code, out := f.post(t, req); code != 400 || out.Field != "orch_model" {
		t.Fatalf("fresh role with a model the list lacks: %d %+v", code, out)
	}
}

// A saved effort of a harness whose effort option is unknown cannot start
// as saved: the saved-picks catalog leaves it out, so the form asks that
// role again.
func TestSavedEffortWithoutItsOptionIsAskedAgain(t *testing.T) {
	f := newRouteFixture(t)
	st := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	if err := st.Save(choices.Pick{Role: "orchestrator", Runner: "herdr", Harness: "opencode", Model: "p/m1", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = st
	res := f.request(t, http.MethodGet, "/api/catalog?probe=0", "", f.authed)
	defer res.Body.Close()
	var c Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	for _, k := range c.Kinds {
		for _, m := range k.Models {
			if k.Kind == "opencode" && len(m.Efforts) > 0 {
				t.Fatalf("an unusable saved effort is offered: %+v", k)
			}
		}
	}
}

// The web app's catalog commands stop waiting shortly after their deadline
// even when a child of the killed program holds its output open.
func TestWebCatalogRunnerStopsForAChildHoldingOutput(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = f.srv.catalog.run(ctx, "/bin/sh", "-c", `sleep 15 & echo up > "$0"; sleep 15`, ready)
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("the child never started")
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("waited %s for the child", d)
	}
}
