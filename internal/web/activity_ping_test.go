package web

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
)

// A33: navigation that reuses a polled endpoint (Changes, a terminal) or
// makes no request (a route tab) reaches the server as the app's explicit
// POST /api/activity, which records the paired user's activity; the
// polling itself still records nothing.
func TestChangesNavigationRecordsActivity(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(t.TempDir(), "herdsman", "web-last-action")
	f.srv.cfg.ActivityFile = file
	clock := time.Unix(1_800_000_000, 0)
	f.srv.now = func() time.Time { return clock }
	// Opening Changes polls the diff: not activity on its own.
	f.request(t, http.MethodGet, "/api/route/diff?route=x", "", f.authed)
	if !afk.ReadWebFile(file).IsZero() {
		t.Fatal("polling counted")
	}
	// The click that opened it sends the explicit activity.
	if res := f.request(t, http.MethodPost, "/api/activity", "", f.authed); res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got := afk.ReadWebFile(file); !got.Equal(clock) {
		t.Fatalf("recorded %v", got)
	}
	// An unpaired browser cannot claim the user is here.
	clock = clock.Add(time.Hour)
	if res := f.request(t, http.MethodPost, "/api/activity", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unpaired status %d", res.StatusCode)
	}
	if got := afk.ReadWebFile(file); got.Equal(clock) {
		t.Fatal("unpaired activity recorded")
	}
}
