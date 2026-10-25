package afk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1_800_000_000, 0)

func probes(goos string, out map[string]string, fail map[string]error, env map[string]string) Probes {
	return Probes{
		GOOS: goos,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			if err := fail[key]; err != nil {
				return nil, err
			}
			return []byte(out[key]), nil
		},
		Getenv: func(k string) string { return env[k] },
		Now:    func() time.Time { return now },
		UID:    501,
	}
}

const ioregOut = `+-o IOHIDSystem  <class IOHIDSystem>
    {
      "HIDIdleTime" = 125000000000
      "Other" = 1
    }
`

func TestIoreg(t *testing.T) {
	p := probes("darwin", map[string]string{"ioreg -c IOHIDSystem": ioregOut}, nil, nil)
	if r := Check(context.Background(), p, nil); r.Line() != "idle 125 source ioreg" {
		t.Errorf("%q", r.Line())
	}
	p = probes("darwin", map[string]string{"ioreg -c IOHIDSystem": "garbage"}, nil, nil)
	if r := Check(context.Background(), p, nil); r.Line() != "unknown ioreg: no HIDIdleTime" {
		t.Errorf("%q", r.Line())
	}
	p = probes("darwin", map[string]string{"ioreg -c IOHIDSystem": `"HIDIdleTime" = 99999999999999999999999`}, nil, nil)
	if r := Check(context.Background(), p, nil); !strings.HasPrefix(r.Line(), "unknown ioreg: HIDIdleTime") {
		t.Errorf("%q", r.Line())
	}
	p = probes("darwin", nil, map[string]error{"ioreg -c IOHIDSystem": errors.New("exec: not found")}, nil)
	if r := Check(context.Background(), p, nil); r.Line() != "unknown ioreg: exec: not found" {
		t.Errorf("%q", r.Line())
	}
}

func TestLogind(t *testing.T) {
	since := now.Add(-90 * time.Second).UnixMicro()
	out := map[string]string{
		"loginctl show-session 7 -p IdleHint -p IdleSinceHint":  "IdleHint=yes\nIdleSinceHint=" + itoa(since) + "\n",
		"loginctl show-user 501 -p Display --value":             "7\n",
		"loginctl show-session c2 -p IdleHint -p IdleSinceHint": "IdleHint=no\nIdleSinceHint=0\n",
		"loginctl show-session g -p IdleHint -p IdleSinceHint":  "garbage",
		"loginctl show-session b -p IdleHint -p IdleSinceHint":  "IdleHint=yes\nIdleSinceHint=x\n",
	}
	for env, want := range map[string]string{
		"7":  "idle 90 source logind",
		"":   "idle 90 source logind", // the user's display session
		"c2": "idle 0 source logind",
		"g":  `unknown loginctl: IdleHint ""`,
		"b":  `unknown loginctl: IdleSinceHint "x"`,
		"$x": `unknown loginctl: no session (got "$x")`,
	} {
		p := probes("linux", out, nil, map[string]string{"XDG_SESSION_ID": env})
		if r := Check(context.Background(), p, nil); r.Line() != want {
			t.Errorf("%q: %q, want %q", env, r.Line(), want)
		}
	}
	p := probes("linux", out, map[string]error{"loginctl show-user 501 -p Display --value": errors.New("missing")}, nil)
	if r := Check(context.Background(), p, nil); r.Line() != "unknown loginctl: missing" {
		t.Errorf("%q", r.Line())
	}
	p = probes("linux", out, map[string]error{"loginctl show-session 7 -p IdleHint -p IdleSinceHint": errors.New("missing")}, map[string]string{"XDG_SESSION_ID": "7"})
	if r := Check(context.Background(), p, nil); r.Line() != "unknown loginctl: missing" {
		t.Errorf("%q", r.Line())
	}
	if r := Check(context.Background(), probes("plan9", nil, nil, nil), nil); r.Line() != "unknown no idle time source on plan9" {
		t.Errorf("%q", r.Line())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A33 item 5: the smaller idle time wins, with its source.
func TestMessagesAndMachine(t *testing.T) {
	p := probes("darwin", map[string]string{"ioreg -c IOHIDSystem": ioregOut}, nil, nil)
	// A message 30 s ago beats 125 s of machine idle.
	if r := Check(context.Background(), p, []time.Time{now.Add(-time.Hour), now.Add(-30 * time.Second)}); r.Line() != "idle 30 source message" {
		t.Errorf("%q", r.Line())
	}
	// The machine wins when it is the more recent.
	if r := Check(context.Background(), p, []time.Time{now.Add(-time.Hour)}); r.Line() != "idle 125 source ioreg" {
		t.Errorf("%q", r.Line())
	}
	// No machine source: the message alone; a future message reads as 0.
	q := probes("plan9", nil, nil, nil)
	if r := Check(context.Background(), q, []time.Time{now.Add(time.Minute)}); r.Line() != "idle 0 source message" {
		t.Errorf("%q", r.Line())
	}
	// The web app's record counts as a message.
	p.WebFile = filepath.Join(t.TempDir(), "herdsman", "web-last-action")
	if err := WriteWebFile(p.WebFile, now.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if r := Check(context.Background(), p, nil); r.Line() != "idle 10 source message" {
		t.Errorf("%q", r.Line())
	}
}

func TestWebFile(t *testing.T) {
	dir := t.TempDir()
	if !ReadWebFile("").IsZero() || !ReadWebFile(filepath.Join(dir, "none")).IsZero() {
		t.Error("a missing file has a time")
	}
	bad := filepath.Join(dir, "bad")
	for _, body := range []string{"x", strings.Repeat("1", 40)} {
		if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if !ReadWebFile(bad).IsZero() {
			t.Errorf("%q read", body)
		}
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteWebFile(filepath.Join(file, "x", "y"), now); err == nil {
		t.Error("written under a file")
	}
	if err := os.MkdirAll(filepath.Join(dir, "d", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteWebFile(filepath.Join(dir, "d"), now); err == nil {
		t.Error("written over a folder")
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := WriteWebFile(filepath.Join(ro, "f"), now); err == nil && os.Getuid() != 0 {
		t.Error("written into a read-only folder")
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	if p := DefaultWebFile(); filepath.Base(p) != "web-last-action" {
		t.Errorf("%q", p)
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if p := DefaultWebFile(); p != "" {
		t.Errorf("%q without a home", p)
	}
}

func TestParseUnixAndAskTimeout(t *testing.T) {
	if tm, err := ParseUnix(" 1800000000 "); err != nil || !tm.Equal(now) {
		t.Errorf("%v %v", tm, err)
	}
	for _, v := range []string{"x", "-1", "99999999999999", ""} {
		if _, err := ParseUnix(v); err == nil {
			t.Errorf("%q parsed", v)
		}
	}
	for v, want := range map[string]time.Duration{"": 5 * time.Minute, "90": 90 * time.Second, "30s": 30 * time.Second, "5m": 5 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour, "007m": 7 * time.Minute} {
		if got, err := AskTimeout(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	for v, want := range map[string]string{"x": "not a duration", "m": "not a duration", "-5m": "not a duration", "0": "out of range", "2d": "out of range", "99999999999999999999": "out of range"} {
		if _, err := AskTimeout(v); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestExecRunner(t *testing.T) {
	if out, err := ExecRunner(context.Background(), "/bin/echo", "x"); err != nil || string(out) != "x\n" {
		t.Errorf("%q %v", out, err)
	}
}

func FuzzAskTimeout(f *testing.F) {
	for _, s := range []string{"5m", "90", "1d", "x", "", "0s"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if d, err := AskTimeout(s); err == nil && (d < time.Second || d > 24*time.Hour) {
			t.Errorf("%q: %v", s, d)
		}
	})
}
