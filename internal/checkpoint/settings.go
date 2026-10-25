package checkpoint

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
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

// InboxSettings reads the inbox settings the checkpointer and its guard
// pass to the inbox, checked at start and in canonical form (LOCK_WAIT=06
// becomes 6): HERDSMAN_INBOX, HERDSMAN_PREFLIGHT, HERDSMAN_MESSAGES_DIR,
// HEALTH_AFTER (1 to 86400, unset keeps the stored value) and LOCK_WAIT (1
// to 60, default 6).
func InboxSettings(getenv func(string) string) (inbox.Options, error) {
	o := inbox.Options{Dir: getenv("HERDSMAN_INBOX"), Preflight: getenv("HERDSMAN_PREFLIGHT"), Installed: getenv("HERDSMAN_MESSAGES_DIR")}
	if v := getenv("HEALTH_AFTER"); v != "" {
		n, err := Integer("HEALTH_AFTER", v, 1, 86400)
		if err != nil {
			return o, err
		}
		o.HealthAfter = strconv.FormatInt(n, 10)
	}
	v := getenv("LOCK_WAIT")
	if v == "" {
		v = "6"
	}
	n, err := Integer("LOCK_WAIT", v, 1, 60)
	if err != nil {
		return o, err
	}
	o.LockWait = strconv.FormatInt(n, 10)
	return o, nil
}
