package checkpoint

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FuzzSettings: every value is either accepted in range or refused with
// one quoted line naming the variable; accepted values are canonical.
func FuzzSettings(f *testing.F) {
	for _, s := range []string{"", "0", "1", "06", "0900", "60", "61", "86400", "86401", "2m", "1d", "2d", "99999999999999999999",
		"1e3", "+5", "-1", " 5", "5 ", "1.5", "x\ny", "\"", "0s", "s", "0x10"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		oneLine := func(err error, name string) {
			var se *SettingError
			if !errors.As(err, &se) || se.Var != name || strings.Contains(err.Error(), "\n") ||
				!strings.HasSuffix(err.Error(), " (got "+strconv.Quote(v)+")") {
				t.Fatalf("%s %q: %v", name, v, err)
			}
		}
		if d, err := Duration("INTERVAL", v); err != nil {
			oneLine(err, "INTERVAL")
		} else if d < time.Second || d > maxInterval {
			t.Fatalf("INTERVAL %q: %v out of range", v, d)
		}
		if n, err := Integer("LOCK_WAIT", v, 1, 60); err != nil {
			oneLine(err, "LOCK_WAIT")
		} else if n < 1 || n > 60 {
			t.Fatalf("LOCK_WAIT %q: %d", v, n)
		}
		o, err := InboxSettings(func(k string) string {
			if k == "HEALTH_AFTER" || k == "LOCK_WAIT" {
				return v
			}
			return ""
		})
		if err != nil {
			var se *SettingError
			if !errors.As(err, &se) {
				t.Fatalf("inbox %q: %v", v, err)
			}
			oneLine(err, se.Var)
			return
		}
		if v != "" && (o.LockWait != strings.TrimLeft(v, "0") || o.HealthAfter != o.LockWait) {
			t.Fatalf("inbox %q not canonical: %+v", v, o)
		}
	})
}
