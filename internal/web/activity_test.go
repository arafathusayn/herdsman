package web

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
)

// A33: a paired browser's action is recorded for `herdsman afk`, at most
// every activityEvery; the app's own polling (GET) is not an action.
func TestActivityRecordsUserActions(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(t.TempDir(), "herdsman", "web-last-action")
	f.srv.cfg.ActivityFile = file
	clock := time.Unix(1_800_000_000, 0)
	f.srv.now = func() time.Time { return clock }

	f.request(t, http.MethodGet, "/api/state", "", f.authed)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("polling counted as activity")
	}
	f.request(t, http.MethodPost, "/api/actions", `{"type":"nope"}`, f.authed)
	if got := afk.ReadWebFile(file); !got.Equal(clock) {
		t.Fatalf("recorded %v", got)
	}
	clock = clock.Add(5 * time.Second)
	f.request(t, http.MethodPost, "/api/actions", `{"type":"nope"}`, f.authed)
	if got := afk.ReadWebFile(file); !got.Equal(clock.Add(-5 * time.Second)) {
		t.Fatalf("written again within %s: %v", activityEvery, got)
	}
	clock = clock.Add(activityEvery)
	f.request(t, http.MethodPost, "/api/actions", `{"type":"nope"}`, f.authed)
	if got := afk.ReadWebFile(file); !got.Equal(clock) {
		t.Fatalf("not written after %s: %v", activityEvery, got)
	}
	// An unpaired request records nothing.
	clock = clock.Add(time.Hour)
	f.request(t, http.MethodPost, "/api/actions", `{}`, nil)
	if got := afk.ReadWebFile(file); got.Equal(clock) {
		t.Fatal("an unpaired request was recorded")
	}
	// A file that cannot be written loses the record, never the request.
	block := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.ActivityFile = filepath.Join(block, "x")
	if res := f.request(t, http.MethodPost, "/api/actions", `{"type":"nope"}`, f.authed); res.StatusCode == http.StatusUnauthorized {
		t.Fatal("request refused")
	}
	f.srv.cfg.ActivityFile = ""
	f.srv.recordActivity()
}
