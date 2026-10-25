package checkpoint

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SettingError is a setting the checkpointer refuses (A25 strict
// settings): the command prints "herdsman checkpointer: <Error()>" and
// exits 2 before anything runs.
type SettingError struct {
	Var    string
	Reason string
	Got    string
}

func (e *SettingError) Error() string {
	return fmt.Sprintf("%s: %s (got %q)", e.Var, e.Reason, e.Got)
}

// maxInterval is the longest interval: one day, so no wait overflows.
const maxInterval = 24 * time.Hour

// Duration reads a duration setting: whole seconds, or a whole number with
// an s, m, h or d suffix; leading zeros are decimal. It must be at least
// one second (a zero interval is a busy loop) and is clamped to one day.
func Duration(name, v string) (time.Duration, error) {
	const reason = "a whole number of seconds, with an optional s, m, h or d suffix, at least 1s"
	digits, unit := v, time.Second
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 's':
			digits = v[:n-1]
		case 'm':
			digits, unit = v[:n-1], time.Minute
		case 'h':
			digits, unit = v[:n-1], time.Hour
		case 'd':
			digits, unit = v[:n-1], 24*time.Hour
		}
	}
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, &SettingError{name, reason, v}
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if n == 0 {
		return 0, &SettingError{name, reason, v}
	}
	if err != nil || n > uint64(maxInterval/unit) {
		return maxInterval, nil
	}
	return time.Duration(n) * unit, nil
}

// Integer reads a whole decimal number from lo to hi (leading zeros are
// decimal, as test reads them).
func Integer(name, v string, lo, hi int64) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || strings.HasPrefix(v, "+") || n < lo || n > hi {
		return 0, &SettingError{name, fmt.Sprintf("a whole number from %d to %d", lo, hi), v}
	}
	return n, nil
}
