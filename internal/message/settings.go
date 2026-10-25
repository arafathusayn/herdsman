package message

import (
	"strconv"
	"time"
)

// settingDuration parses a strict duration setting (A25): decimal digits
// (leading zeros read as decimal) with an optional s, m, h or d suffix,
// in seconds, within lo..hi.
func settingDuration(v string, lo, hi int) (int, bool) {
	mult := 1
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 's':
			v = v[:n-1]
		case 'm':
			v, mult = v[:n-1], 60
		case 'h':
			v, mult = v[:n-1], 3600
		case 'd':
			v, mult = v[:n-1], 86400
		}
	}
	n, ok := settingInt(v, 0, 1<<40)
	if !ok || v == "" {
		return 0, false
	}
	n *= mult
	return n, n >= lo && n <= hi
}

// durEnv reads a duration setting ${K:-def} once, strictly (A25).
func (e *Env) durEnv(k, def string, lo, hi int) (int, error) {
	v := e.envDefault(k, def)
	n, ok := settingDuration(v, lo, hi)
	if !ok {
		return 0, die("herdsman message: %s: invalid %s, want a duration from %ds to %ds: digits with an optional s, m, h or d (got %q)", k, k, lo, hi, v)
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
type reopen struct{ d time.Duration }

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
