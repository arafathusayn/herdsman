package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// A30: the saved picks, runner included, are read without asking any
// harness, so the form can show them before the catalog's probes.
func TestSavedPicksComeBeforeProbes(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	f.srv.catalog = newCatalogSource(fakeRunner(nil, nil, &calls))
	st := &choices.Store{Path: filepath.Join(t.TempDir(), "herdsman", "choices.json")}
	if err := st.Save(choices.Pick{Role: "implementer", Runner: "subagent", Harness: "claude", Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.Choices = st
	res := f.request(t, http.MethodGet, "/api/choices", "", f.authed)
	var got choices.File
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&got) != nil {
		t.Fatalf("status %d", res.StatusCode)
	}
	if len(got.Picks) != 1 || got.Picks[0].Runner != "subagent" || got.Picks[0].Model != "model-b" {
		t.Fatalf("picks %+v", got.Picks)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d harness commands ran", calls.Load())
	}
	f.srv.cfg.Choices = nil
	res = f.request(t, http.MethodGet, "/api/choices", "", f.authed)
	if json.NewDecoder(res.Body).Decode(&got) != nil || got.Picks == nil || len(got.Picks) != 0 {
		t.Fatalf("no store: %+v", got)
	}
}
