package checkpoint

import (
	"strconv"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/settings"
)

// The checkpointer reads every numeric and duration setting with the shared
// parser (internal/settings), so it accepts exactly the forms the other
// commands accept; a refused value is a *settings.Error, printed as one
// line before the command exits 2.

// maxInterval is the longest interval: one day, so no wait overflows.
const maxInterval = 24 * time.Hour

// interval reads INTERVAL (default 900 s), clamped to one day. It must be
// at least one second: a zero interval is a busy loop.
func interval(v string) (time.Duration, error) {
	d, err := settings.Duration("INTERVAL", v, 900*time.Second, maxInterval)
	if err != nil {
		return 0, err
	}
	if d < time.Second {
		return 0, &settings.Error{Var: "INTERVAL", Reason: "must be at least 1s", Got: v}
	}
	return d, nil
}

// InboxSettings reads the inbox settings the checkpointer and its guard
// pass to the inbox, checked at start and in canonical form (LOCK_WAIT=06
// becomes 6): HERDSMAN_INBOX, HERDSMAN_PREFLIGHT, HERDSMAN_MESSAGES_DIR,
// HEALTH_AFTER (1 to 86400, unset keeps the stored value) and LOCK_WAIT (1
// to 60, default 6).
func InboxSettings(getenv func(string) string) (inbox.Options, error) {
	o := inbox.Options{Dir: getenv("HERDSMAN_INBOX"), Preflight: getenv("HERDSMAN_PREFLIGHT"), Installed: getenv("HERDSMAN_MESSAGES_DIR")}
	if v := getenv("HEALTH_AFTER"); v != "" {
		n, err := settings.Int("HEALTH_AFTER", v, 0, 1, 86400)
		if err != nil {
			return o, err
		}
		o.HealthAfter = strconv.FormatInt(n, 10)
	}
	n, err := settings.Int("LOCK_WAIT", getenv("LOCK_WAIT"), 6, 1, 60)
	if err != nil {
		return o, err
	}
	o.LockWait = strconv.FormatInt(n, 10)
	return o, nil
}
