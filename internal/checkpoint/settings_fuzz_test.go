package checkpoint

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/settings"
)

// FuzzSettings drives the checkpointer's settings through the shared parser
// (internal/settings): every value is either accepted in range or refused
// with one quoted line naming the variable; accepted inbox values are
// canonical, and an accepted interval keeps the one-second floor and the
// one-day clamp.
func FuzzSettings(f *testing.F) {
	for _, s := range []string{"", "0", "1", "06", " 06 ", "+6", "0900", "60", "61", "86400", "86401", "2m", " 2m ", "1d", "2d",
		"99999999999999999999", "1e3", "+5", "-1", " 5", "5 ", "1.5", "x\ny", "\"", "0s", "s", "0x10", "+0", "-0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		oneLine := func(err error, name string) {
			var se *settings.Error
			if !errors.As(err, &se) || se.Var != name || strings.Contains(err.Error(), "\n") ||
				!strings.HasSuffix(err.Error(), " (got "+strconv.Quote(v)+")") {
				t.Fatalf("%s %q: %v", name, v, err)
			}
		}
		canonical := func(s string, lo, hi int64) {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != s || n < lo || n > hi {
				t.Fatalf("%q: %q not canonical in %d..%d", v, s, lo, hi)
			}
		}
		if d, err := interval(v); err != nil {
			oneLine(err, "INTERVAL")
		} else if d < time.Second || d > maxInterval {
			t.Fatalf("INTERVAL %q: %v out of range", v, d)
		}
		o, err := InboxSettings(func(k string) string {
			if k == "HEALTH_AFTER" || k == "LOCK_WAIT" {
				return v
			}
			return ""
		})
		if err != nil {
			var se *settings.Error
			if !errors.As(err, &se) {
				t.Fatalf("inbox %q: %v", v, err)
			}
			oneLine(err, se.Var)
		} else {
			canonical(o.LockWait, 1, 60)
			if v != "" {
				canonical(o.HealthAfter, 1, 86400)
				if o.HealthAfter != o.LockWait {
					t.Fatalf("inbox %q: %+v", v, o)
				}
			}
		}
		c, err := FromEnv(func(k string) string {
			switch k {
			case "TARGET":
				return "w1:p1"
			case "INTERVAL", "LIMIT", "LOCK_WAIT":
				return v
			}
			return ""
		}, "/")
		if err != nil {
			var se *settings.Error
			if !errors.As(err, &se) {
				t.Fatalf("env %q: %v", v, err)
			}
			oneLine(err, se.Var)
			return
		}
		if c.Interval < time.Second || c.Interval > maxInterval || c.Limit < 0 || c.LockWait < 1 || c.LockWait > 60 ||
			strconv.Itoa(c.LockWait) != c.Inbox.LockWait {
			t.Fatalf("env %q: %+v", v, c)
		}
	})
}
