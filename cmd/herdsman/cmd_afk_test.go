package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	regexpPkg "regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/afk"
)

// fakeIdlePrograms puts fake ioreg and loginctl programs first on PATH,
// printing out, and points the web record at a temp file.
func fakeIdlePrograms(t *testing.T, out string) {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"ioreg", "loginctl"} {
		script := "#!/bin/sh\nprintf '%s\\n' '" + out + "'\n"
		if out == "" {
			script = "#!/bin/sh\nexit 1\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("XDG_SESSION_ID", "7")
	t.Setenv("HERDSMAN_ASK_TIMEOUT", "")
	web := filepath.Join(t.TempDir(), "web-last-action")
	afkProbes = func() afk.Probes {
		return afk.Probes{GOOS: runtime.GOOS, Run: afk.ExecRunner, Getenv: os.Getenv, Now: time.Now, UID: os.Getuid(), WebFile: web}
	}
	t.Cleanup(func() { afkProbes = defaultAfkProbes })
}

// The fake prints both programs' answers; each OS reads its own lines.
const idleOut = `"HIDIdleTime" = 600000000000
IdleHint=yes
IdleSinceHint=`

func TestAfkCommand(t *testing.T) {
	since := strconv.FormatInt(time.Now().Add(-600*time.Second).UnixMicro(), 10)
	fakeIdlePrograms(t, idleOut+since)
	src := map[string]string{"darwin": "ioreg", "linux": "logind"}[runtime.GOOS]
	out, errOut, code := call(t, "afk")
	if code != 0 || errOut != "" || !matches(`^idle 60[01] source `+src+`\n$`, out) {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	recent := strconv.FormatInt(time.Now().Add(-20*time.Second).Unix(), 10)
	out, _, _ = call(t, "afk", "--last-message", "1", "--last-message", recent)
	if !matches(`^idle (19|20|21) source message\n$`, out) {
		t.Errorf("%q", out)
	}
	out, _, _ = call(t, "afk", "--json", "--last-message", recent)
	var r struct {
		Idle   *int64 `json:"idle_seconds"`
		Source string `json:"source"`
		Ask    int64  `json:"ask_timeout_seconds"`
	}
	if json.Unmarshal([]byte(out), &r) != nil || r.Idle == nil || r.Source != "message" || r.Ask != 300 {
		t.Errorf("%q", out)
	}
}

func TestAfkMissingAndGarbage(t *testing.T) {
	fakeIdlePrograms(t, "")
	out, _, code := call(t, "afk")
	if code != 0 || !strings.HasPrefix(out, "unknown ") {
		t.Errorf("failing tools: %d %q", code, out)
	}
	fakeIdlePrograms(t, "garbage")
	out, _, code = call(t, "afk", "--json")
	if code != 0 || !strings.Contains(out, `"unknown":`) {
		t.Errorf("garbage: %d %q", code, out)
	}
	fakeIdlePrograms(t, "garbage")
	t.Setenv("PATH", t.TempDir()) // no tools at all
	out, _, code = call(t, "afk")
	if code != 0 || !strings.HasPrefix(out, "unknown ") {
		t.Errorf("missing: %d %q", code, out)
	}
}

func TestAfkUsage(t *testing.T) {
	fakeIdlePrograms(t, "garbage")
	out, _, code := call(t, "afk", "--help")
	if code != 0 || !strings.Contains(out, "Exit codes:") {
		t.Errorf("%d %q", code, out)
	}
	for _, args := range [][]string{{"afk", "x"}, {"afk", "--bad"}} {
		if _, errOut, code := call(t, args...); code != 2 || !strings.HasPrefix(errOut, "usage: herdsman afk") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	if _, errOut, code := call(t, "afk", "--last-message", "soon"); code != 2 || errOut != "herdsman afk: --last-message: not a unix time in seconds (got \"soon\")\n" {
		t.Errorf("%d %q", code, errOut)
	}
	t.Setenv("HERDSMAN_ASK_TIMEOUT", "later")
	if _, errOut, code := call(t, "afk"); code != 2 || errOut != "herdsman afk: HERDSMAN_ASK_TIMEOUT: not a duration (got \"later\")\n" {
		t.Errorf("%d %q", code, errOut)
	}
}

func matches(re, s string) bool { return regexpPkg.MustCompile(re).MatchString(s) }

func TestDefaultAfkProbes(t *testing.T) {
	if p := defaultAfkProbes(); p.GOOS != runtime.GOOS || p.Run == nil {
		t.Errorf("%+v", p)
	}
}
