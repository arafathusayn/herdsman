package daemon

import (
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/settings"
)

// TestDurationBlanksAsFileMode: the daemon path reads a duration with the
// same parser as file mode (A25), so blanks around a value are allowed on
// a daemon-owned inbox too, and both clamp alike.
func TestDurationBlanksAsFileMode(t *testing.T) {
	for _, v := range []string{" 30", "30 ", "\t5m", "45s\n", "2h", "99999999999h"} {
		want, err := settings.Duration("HEALTH_GRACE", v, 0, 24*time.Hour)
		if err != nil {
			t.Fatalf("file mode refused %q: %v", v, err)
		}
		if got, err := parseDuration(v, time.Second, 24*time.Hour); err != nil || got != want {
			t.Errorf("daemon path %q: %s %v, want %s", v, got, err, want)
		}
	}
	for _, v := range []string{" ", "1 s", "3 0"} {
		if _, err := parseDuration(v, time.Second, time.Hour); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
