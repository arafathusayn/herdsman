package message

import (
	"strconv"
	"time"

	"github.com/arafathusayn/herdsman/internal/settings"
)

// durSetting reads a duration setting with the shared parser (A25):
// seconds, or a number with an s, m, h or d suffix, blanks around it
// allowed; a value over hi is clamped to hi, one under lo raised to lo
// (the command's own floor). In whole seconds.
func durSetting(k, v string, lo, hi int) (int, error) {
	d, err := settings.Duration(k, v, 0, time.Duration(hi)*time.Second)
	if err != nil {
		return 0, err
	}
	return max(int(d/time.Second), lo), nil
}

// SettingDuration is the strict duration setting (A25) for the commands
// outside this package: decimal digits (leading zeros read as decimal)
// with an optional s, m, h or d suffix, clamped to lo..hi. The digits are
// read with saturation, so a valid duration of any length is clamped, never
// refused for its size or wrapped by an overflow.
func SettingDuration(v string, lo, hi time.Duration) (time.Duration, bool) {
	unit := time.Second
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 's':
			v = v[:n-1]
		case 'm':
			v, unit = v[:n-1], time.Minute
		case 'h':
			v, unit = v[:n-1], time.Hour
		case 'd':
			v, unit = v[:n-1], 24*time.Hour
		}
	}
	if v == "" {
		return 0, false
	}
	limit := int64(hi/unit) + 1 // past the bound; no overflow below it
	var n int64
	for _, c := range []byte(v) {
		if c < '0' || c > '9' {
			return 0, false
		}
		if n < limit {
			n = n*10 + int64(c-'0')
		}
	}
	d := time.Duration(min(n, limit)) * unit
	return min(max(d, lo), hi), true
}

// intSetting reads a count setting with the shared parser (A25): a whole
// number as the scripts' test reads it, within lo..hi.
func intSetting(k, v string, lo, hi int) (int, error) {
	n, err := settings.Int(k, v, int64(lo), int64(lo), int64(hi))
	return int(n), err
}

// settingError is a rejected setting as every command prints it: one line
// "herdsman message: <VAR>: <reason> (got "<value>")", exit 2.
func settingError(err error) error { return die("herdsman message: %v", err) }

// durEnv reads a duration setting ${K:-def} once (A25).
func (e *Env) durEnv(k, def string, lo, hi int) (int, error) {
	n, err := durSetting(k, e.envDefault(k, def), lo, hi)
	if err != nil {
		return 0, settingError(err)
	}
	return n, nil
}

// commonSettings parses the settings every command reads, once, before the
// inbox is opened: HEALTH_AFTER (a duration), LOCK_WAIT (attempts) and
// EVENT_READ_MAX (a duration; "" means no cap). The inbox gets them as
// plain decimal numbers.
func (e *Env) commonSettings() error {
	health, err := e.durEnv("HEALTH_AFTER", "", 1, 86400)
	if e.env("HEALTH_AFTER") == "" {
		health, err = 0, nil
	}
	if err != nil {
		return err
	}
	lockWait := 0
	if e.env("LOCK_WAIT") != "" {
		if lockWait, err = e.uintEnv("LOCK_WAIT", "", 1, 60); err != nil {
			return err
		}
	}
	readMax := 0
	if e.env("EVENT_READ_MAX") != "" {
		if readMax, err = e.durEnv("EVENT_READ_MAX", "", 1, 3600); err != nil {
			return err
		}
	}
	e.healthAfter, e.lockWait, e.eventReadMax = "", "", readMax
	if health > 0 {
		e.healthAfter = strconv.Itoa(health)
	}
	if lockWait > 0 {
		e.lockWait = strconv.Itoa(lockWait)
	}
	e.settingsDone = true
	return nil
}

// reopen is the subscription retry delay: 1 s doubling to 60 s while
// herdr cannot be subscribed, reset by a success (A26, D4: the script
// retried every second without backoff).
// dropped: the last stream failed; the caller waits next() before a new one.
type reopen struct {
	d       time.Duration
	dropped bool
}

// after records a dropped stream: the next open waits for the backoff.
func (b *reopen) after(now int64, retryAt *int64) {
	if b.dropped {
		b.dropped = false
		*retryAt = now + int64(b.next()/time.Second)
	}
}

func (b *reopen) next() time.Duration {
	if b.d == 0 {
		b.d = time.Second
	}
	d := b.d
	if b.d *= 2; b.d > time.Minute {
		b.d = time.Minute
	}
	return d
}

func (b *reopen) reset() { b.d = 0 }
