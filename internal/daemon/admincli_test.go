package daemon

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// adminRun runs `herdsman daemon` against a harness with a fake
// supervisor, stdin and environment.
func adminRun(h *harness, sup *Supervisor, stdin io.Reader, env map[string]string, attended bool, args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	a := &AdminEnv{Getenv: func(k string) string {
		if k == "HERDSMAN_PRIVATE_HOME" {
			return h.ph
		}
		return env[k]
	}, Stdin: stdin, Stdout: &out, Stderr: &errb, Version: testVersion, Supervisor: sup, Attended: attended}
	code := RunDaemon(a, args)
	return out.String(), errb.String(), code
}

// TestHelpIsARouteNameAfterTheCommand: help in the command's place prints
// the help; after a command it is a value, so a route named help is
// removed, not answered with the help text.
func TestHelpIsARouteNameAfterTheCommand(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	if out, _, code := adminRun(h, sup, nil, nil, false, "help"); code != 0 || !strings.HasPrefix(out, "usage:") {
		t.Fatalf("help: %d %q", code, out)
	}
	if out, _, code := adminRun(h, sup, nil, nil, false, "route", "create", "--repo", "repo", "--route", "help"); code != 0 || !strings.Contains(out, "repo-help") {
		t.Fatalf("create help: %d %q", code, out)
	}
	if out, errOut, code := adminRun(h, sup, nil, nil, false, "route", "remove", "help"); code != 0 || out != "removed help; inbox kept\n" {
		t.Fatalf("remove help: %d %q %q", code, out, errOut)
	}
}

// TestLifecycleRefusesExtraArguments: stop --bogus and the other
// lifecycle commands with an unexpected argument exit 2 before the
// supervisor is called.
func TestLifecycleRefusesExtraArguments(t *testing.T) {
	h, f, sup := newSup(t, "darwin")
	for _, cmd := range []string{"install", "uninstall", "start", "stop", "restart", "status"} {
		_, errOut, code := adminRun(h, sup, nil, nil, false, cmd, "--bogus")
		if code != 2 || !strings.Contains(errOut, cmd+" takes no arguments") {
			t.Errorf("%s --bogus: %d %q", cmd, code, errOut)
		}
	}
	if len(f.calls) != 0 || !lockHeld(h.home) {
		t.Fatalf("the supervisor acted: %v", f.calls)
	}
}

// TestSetupBoundaryStrict: --boundary null, a list or duplicate keys is
// refused with exit 2 before any question, and nothing is recorded.
func TestSetupBoundaryStrict(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	adminRun(h, sup, nil, nil, false, "route", "create", "--repo", "repo", "--route", "r")
	for i, body := range []string{"null", "[]", `{"human_checked":true,"human_checked":false}`} {
		p := writeFile(t, h.dir, "b"+string(rune('0'+i)), body)
		out, errOut, code := adminRun(h, sup, strings.NewReader(strings.Repeat("y\n", 6)), nil, true, "setup", "r", "--rule", h.rules, "--boundary", p)
		if code != 2 || out != "" || !strings.Contains(errOut, "--boundary") {
			t.Fatalf("boundary %s: %d %q %q", body, code, out, errOut)
		}
	}
	dup := writeFile(t, h.dir, "wt", `{"a":1,"a":2}`)
	if _, errOut, code := adminRun(h, sup, strings.NewReader(""), nil, true, "setup", "r", "--rule", h.rules, "--watch-targets", dup); code != 2 || !strings.Contains(errOut, "duplicate keys") {
		t.Fatalf("watch targets duplicate keys: %d %q", code, errOut)
	}
	if exists(filepath.Join(h.ph, "repo-r", "setup.json")) {
		t.Fatal("a refused setup recorded a proof")
	}
}

// TestSetupQuestionDeadline: a question with no answer by
// HERDSMAN_ASK_TIMEOUT, or input that ends, fails closed with exit 2 and
// records nothing (A33); an invalid timeout is refused before asking.
func TestSetupQuestionDeadline(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	adminRun(h, sup, nil, nil, false, "route", "create", "--repo", "repo", "--route", "r")
	pr, pw := io.Pipe()
	defer pw.Close()
	start := time.Now()
	_, errOut, code := adminRun(h, sup, pr, map[string]string{"HERDSMAN_ASK_TIMEOUT": "1s"}, true, "setup", "r", "--rule", h.rules)
	if code != 2 || !strings.Contains(errOut, "no answer within 1s") || time.Since(start) > 30*time.Second {
		t.Fatalf("silent terminal: %d %q", code, errOut)
	}
	if _, errOut, code := adminRun(h, sup, strings.NewReader("y\ny\n"), nil, true, "setup", "r", "--rule", h.rules); code != 2 || !strings.Contains(errOut, "no answer") {
		t.Fatalf("input ended: %d %q", code, errOut)
	}
	if exists(filepath.Join(h.ph, "repo-r", "setup.json")) {
		t.Fatal("an unanswered setup recorded a proof")
	}
	for _, v := range []string{"5x", "0", "-1", "s"} {
		out, errOut, code := adminRun(h, sup, strings.NewReader(strings.Repeat("y\n", 6)), map[string]string{"HERDSMAN_ASK_TIMEOUT": v}, true, "setup", "r", "--rule", h.rules)
		if code != 2 || out != "" || !strings.Contains(errOut, "HERDSMAN_ASK_TIMEOUT") {
			t.Fatalf("timeout %q: %d %q %q", v, code, out, errOut)
		}
	}
}

// TestSetupSameFileTargets: one file given as both watch and resume
// targets is sent as both, so the server judges each (an object is not a
// watch list); before, the map keyed by file name kept only one.
func TestSetupSameFileTargets(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	adminRun(h, sup, nil, nil, false, "route", "create", "--repo", "repo", "--route", "r")
	p := writeFile(t, h.dir, "targets", `{}`)
	for range 3 {
		_, errOut, code := adminRun(h, sup, strings.NewReader(strings.Repeat("y\n", 6)), nil, true, "setup", "r", "--rule", h.rules, "--watch-targets", p, "--resume-targets", p)
		if code != 2 || !strings.Contains(errOut, "watch targets") {
			t.Fatalf("same file: %d %q", code, errOut)
		}
	}
}

// TestAdminJSONErrorShape: with --json every failure, a supervisor or OS
// error too, prints one error object.
func TestAdminJSONErrorShape(t *testing.T) {
	h, _, sup := newSup(t, "linux")
	sup.Run = func(string, ...string) error { return os.ErrPermission }
	h.stop()
	out, _, code := adminRun(h, sup, nil, nil, false, "--json", "start")
	if code != 1 || !strings.HasPrefix(out, `{"error":{"code":"failed","message":`) {
		t.Fatalf("%d %q", code, out)
	}
}

// TestDaemonLogBounded: the daemon log keeps at most logMax bytes plus
// one previous file, however many lines are written.
