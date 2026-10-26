// Package settings reads the numeric, duration, decimal and size settings
// the commands take from the environment or an option, once, at start.
// A value it rejects is an *Error, which a command prints as one line
// "herdsman <command>: <VARIABLE>: <reason> (got "<value>")" before it
// exits 2 and runs nothing. An empty value is the setting's default, as the
// scripts' ${VAR:-default} reads it.
package settings

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/shnum"
)

// ExitInvalid is the exit code of a command whose setting is invalid.
const ExitInvalid = 2

// Error is a rejected setting.
type Error struct {
	Var    string // the variable or option, e.g. HERDSMAN_GATE_POLL
	Reason string
	Got    string // the value as given
}

// Missing is a required setting that is not set: a settings error, so
// the command prints it as one line and exits 2 (A25).
func Missing(name string) *Error { return &Error{name, "required, not set", ""} }

func (e *Error) Error() string { return fmt.Sprintf("%s: %s (got %q)", e.Var, e.Reason, e.Got) }

// Int reads a whole number as the scripts' test builtin does: blanks
// around it, an optional sign, decimal digits (leading zeros are decimal).
// It must lie within [lo, hi].
func Int(name, val string, def, lo, hi int64) (int64, error) {
	if val == "" {
		return def, nil
	}
	n, ok := shnum.Test(val)
	if !ok {
		return 0, &Error{name, "not a whole number", val}
	}
	if n < lo || n > hi {
		return 0, &Error{name, fmt.Sprintf("must be from %d to %d", lo, hi), val}
	}
	return n, nil
}

// Duration reads plain seconds or a number with an s, m, h or d suffix.
// A negative or malformed value is rejected; a value over max is clamped
// to max, so no later multiplication overflows.
func Duration(name, val string, def, max time.Duration) (time.Duration, error) {
	if val == "" {
		return min(def, max), nil
	}
	s := strings.Trim(val, " \t\n")
	unit := time.Second
	if s != "" {
		switch s[len(s)-1] {
		case 's':
			s = s[:len(s)-1]
		case 'm':
			unit, s = time.Minute, s[:len(s)-1]
		case 'h':
			unit, s = time.Hour, s[:len(s)-1]
		case 'd':
			unit, s = 24*time.Hour, s[:len(s)-1]
		}
	}
	n, ok := digits(s)
	if !ok {
		return 0, &Error{name, "not a duration (seconds, or a number with s, m, h or d)", val}
	}
	if n > uint64(max/unit) {
		return max, nil
	}
	return time.Duration(n) * unit, nil
}

// Size reads a byte count: a plain number or one with a k or m suffix,
// at most hi.
func Size(name, val string, def, hi int64) (int64, error) {
	if val == "" {
		return def, nil
	}
	s := strings.Trim(val, " \t\n")
	mult := int64(1)
	if s != "" {
		switch s[len(s)-1] {
		case 'k':
			mult, s = 1<<10, s[:len(s)-1]
		case 'm':
			mult, s = 1<<20, s[:len(s)-1]
		}
	}
	n, ok := digits(s)
	if !ok {
		return 0, &Error{name, "not a size (bytes, or a number with k or m)", val}
	}
	if n > uint64(hi/mult) {
		return 0, &Error{name, fmt.Sprintf("must be at most %d bytes", hi), val}
	}
	return int64(n) * mult, nil
}

// Decimal reads a positive decimal number, such as a load limit. Empty
// returns 0: the caller's default applies.
func Decimal(name, val string) (float64, error) {
	if val == "" {
		return 0, nil
	}
	s := strings.Trim(val, " \t\n")
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return 0, &Error{name, "not a positive decimal number", val}
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) {
		return 0, &Error{name, "not a positive decimal number", val}
	}
	return f, nil
}

// digits reads decimal digits only, saturating at the largest uint64.
func digits(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (math.MaxUint64-9)/10 {
			n = math.MaxUint64
			continue
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// Reader reads settings from an environment and keeps the first error, so
// a command reads them all and then checks Err once.
type Reader struct {
	Getenv func(string) string
	Err    error
}

func (r *Reader) keep(err error) bool {
	if err != nil && r.Err == nil {
		r.Err = err
	}
	return err != nil
}

// Int is Int on the variable's value.
func (r *Reader) Int(name string, def, lo, hi int64) int64 {
	n, err := Int(name, r.Getenv(name), def, lo, hi)
	if r.keep(err) {
		return def
	}
	return n
}

// Duration is Duration on the variable's value.
func (r *Reader) Duration(name string, def, max time.Duration) time.Duration {
	d, err := Duration(name, r.Getenv(name), def, max)
	if r.keep(err) {
		return def
	}
	return d
}

// Decimal is Decimal on the variable's value.
func (r *Reader) Decimal(name string) float64 {
	f, err := Decimal(name, r.Getenv(name))
	if r.keep(err) {
		return 0
	}
	return f
}
