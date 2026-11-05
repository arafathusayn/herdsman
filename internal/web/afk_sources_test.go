package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
)

// A33: `herdsman afk` reports min(machine idle, the paired browser's last
// action, the newest supplied message), whichever source is the most
// recent, with all three present.
func TestAfkTakesTheMostRecentOfThreeSources(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(t.TempDir(), "herdsman", "web-last-action")
	f.srv.cfg.ActivityFile = file
	start := time.Unix(1_800_000_000, 0)
	f.srv.now = func() time.Time { return start }
	// The browser navigates (a GET outside the polled list) at start.
	f.request(t, http.MethodGet, "/api/catalog", "", f.authed)
	if got := afk.ReadWebFile(file); !got.Equal(start) {
		t.Fatalf("browser action recorded at %v", got)
	}
	probe := func(machine time.Duration, now time.Time) afk.Probes {
		return afk.Probes{
			GOOS: "darwin",
			Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name+" "+strings.Join(args, " ") != "ioreg -c IOHIDSystem" {
					t.Fatalf("ran %s %v", name, args)
				}
				return []byte(`"HIDIdleTime" = ` + strconv.FormatInt(int64(machine), 10) + "\n"), nil
			},
			Getenv:  func(string) string { return "" },
			Now:     func() time.Time { return now },
			WebFile: file,
		}
	}
	cases := []struct {
		name    string
		machine time.Duration
		now     time.Time
		message time.Time
		want    string
	}{
		{"machine newest", 5 * time.Second, start.Add(60 * time.Second), start.Add(-time.Hour), "idle 5 source ioreg"},
		{"browser newest", 300 * time.Second, start.Add(20 * time.Second), start.Add(-time.Hour), "idle 20 source message"},
		{"message newest", 300 * time.Second, start.Add(60 * time.Second), start.Add(50 * time.Second), "idle 10 source message"},
	}
	for _, c := range cases {
		r := afk.Check(context.Background(), probe(c.machine, c.now), []time.Time{c.message})
		if r.Line() != c.want {
			t.Errorf("%s: %q, want %q", c.name, r.Line(), c.want)
		}
	}
}
