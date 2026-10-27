// Package afk reports how long the herdsman user has been away (A33): the
// smaller of the machine's input idle time (macOS: ioreg's HIDIdleTime;
// Linux: logind's IdleHint and IdleSinceHint) and the time since the user's
// newest message through any channel (given as unix times, and the web
// app's last user action, which herdsman web records in a small file).
// The programs run once, with a fixed argv; their output is parsed in Go.
package afk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner runs a program and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the program with no standard input.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Probes are what Check reads; tests replace each one.
type Probes struct {
	GOOS   string
	Run    Runner
	Getenv func(string) string
	Now    func() time.Time
	UID    int
	// WebFile is where herdsman web records the user's last action, "" for none.
	WebFile string
}

// Report is the result.
//
// JSON fields: idle_seconds and source ("ioreg", "logind" or "message")
// when known; unknown (the reason) otherwise; ask_timeout_seconds
// (HERDSMAN_ASK_TIMEOUT) always.
type Report struct {
	IdleSeconds *int64 `json:"idle_seconds,omitempty"`
	Source      string `json:"source,omitempty"`
	Unknown     string `json:"unknown,omitempty"`
	AskTimeout  int64  `json:"ask_timeout_seconds"`
}

// Line is the text output: `idle <seconds> source <name>` or `unknown <reason>`.
func (r Report) Line() string {
	if r.IdleSeconds == nil {
		return "unknown " + r.Unknown
	}
	return fmt.Sprintf("idle %d source %s", *r.IdleSeconds, r.Source)
}

var hidIdle = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

// machineIdle is the input idle time and its source.
func machineIdle(ctx context.Context, p Probes) (time.Duration, string, error) {
	switch p.GOOS {
	case "darwin":
		out, err := p.Run(ctx, "ioreg", "-c", "IOHIDSystem")
		if err != nil {
			return 0, "ioreg", fmt.Errorf("ioreg: %v", err)
		}
		m := hidIdle.FindSubmatch(out)
		if m == nil {
			return 0, "ioreg", errors.New("ioreg: no HIDIdleTime")
		}
		ns, err := strconv.ParseInt(string(m[1]), 10, 64)
		if err != nil {
			return 0, "ioreg", fmt.Errorf("ioreg: HIDIdleTime %s: %v", m[1], err)
		}
		return time.Duration(ns), "ioreg", nil
	case "linux":
		return logindIdle(ctx, p)
	}
	return 0, "", fmt.Errorf("no idle time source on %s", p.GOOS)
}

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func logindIdle(ctx context.Context, p Probes) (time.Duration, string, error) {
	session := p.Getenv("XDG_SESSION_ID")
	if session == "" {
		out, err := p.Run(ctx, "loginctl", "show-user", strconv.Itoa(p.UID), "-p", "Display", "--value")
		if err != nil {
			return 0, "logind", fmt.Errorf("loginctl: %v", err)
		}
		session = strings.TrimSpace(string(out))
	}
	if !sessionID.MatchString(session) {
		return 0, "logind", fmt.Errorf("loginctl: no session (got %q)", session)
	}
	out, err := p.Run(ctx, "loginctl", "show-session", session, "-p", "IdleHint", "-p", "IdleSinceHint")
	if err != nil {
		return 0, "logind", fmt.Errorf("loginctl: %v", err)
	}
	var hint, since string
	for _, l := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "IdleHint":
			hint = v
		case "IdleSinceHint":
			since = v
		}
	}
	switch hint {
	case "no":
		return 0, "logind", nil
	case "yes":
		us, err := strconv.ParseInt(since, 10, 64)
		if err != nil || us <= 0 {
			return 0, "logind", fmt.Errorf("loginctl: IdleSinceHint %q", since)
		}
		return max(0, p.Now().Sub(time.UnixMicro(us))), "logind", nil
	}
	return 0, "logind", fmt.Errorf("loginctl: IdleHint %q", hint)
}

// ParseUnix reads a --last-message value: a unix time in seconds.
func ParseUnix(v string) (time.Time, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 || n > 1<<40 {
		return time.Time{}, errors.New("not a unix time in seconds")
	}
	return time.Unix(n, 0), nil
}

// ReadWebFile reads the web app's last user action, or the zero time.
func ReadWebFile(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 32 {
		return time.Time{}
	}
	t, err := ParseUnix(string(b))
	if err != nil {
		return time.Time{}
	}
	return t
}

// WriteWebFile records t as the web app's last user action, atomically.
func WriteWebFile(path string, t time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".web-last-*")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(strconv.FormatInt(t.Unix(), 10) + "\n")
	err = errors.Join(werr, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// DefaultWebFile is the web app's record in the user's config folder.
func DefaultWebFile() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "herdsman", "web-last-action")
}

// AskTimeout parses HERDSMAN_ASK_TIMEOUT (default 5m). It is the shared
// duration form of A25 (Duration): plain seconds or a number with s, m, h
// or d, decimals allowed, leading zeros read as decimal.
func AskTimeout(v string) (time.Duration, error) {
	if v == "" {
		return 5 * time.Minute, nil
	}
	d, err := Duration(v)
	if err != nil {
		return 0, fmt.Errorf("HERDSMAN_ASK_TIMEOUT: %v (got %q)", err, v)
	}
	return d, nil
}

// Duration reads a duration setting: a positive decimal number of seconds,
// or one with an s, m, h or d suffix. A value over one day is clamped to
// one day before it is multiplied, so nothing overflows; a value under a
// second reads as one second.
func Duration(v string) (time.Duration, error) {
	s := strings.TrimSpace(v)
	unit := 1.0
	if n := len(s); n > 0 {
		if u, ok := map[byte]float64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[s[n-1]]; ok {
			unit, s = u, s[:n-1]
		}
	}
	if s == "" || strings.Trim(s, "0123456789.") != "" || strings.Count(s, ".") > 1 || s == "." {
		return 0, errors.New("not a duration")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, errors.New("not a positive duration")
	}
	secs := min(f*unit, 86400)
	return max(time.Duration(secs*float64(time.Second)), time.Second), nil
}

// Check reports the smaller of the machine's idle time and the time since
// the newest message.
func Check(ctx context.Context, p Probes, messages []time.Time) Report {
	var r Report
	idle, source, err := machineIdle(ctx, p)
	known := err == nil
	newest := ReadWebFile(p.WebFile)
	for _, m := range messages {
		if m.After(newest) {
			newest = m
		}
	}
	if !newest.IsZero() {
		since := max(0, p.Now().Sub(newest))
		if !known || since < idle {
			idle, source, known = since, "message", true
		}
	}
	if !known {
		r.Unknown = err.Error()
		return r
	}
	secs := int64(idle / time.Second)
	r.IdleSeconds, r.Source = &secs, source
	return r
}
