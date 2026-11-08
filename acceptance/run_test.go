package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func eventLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestJudgeEvents(t *testing.T) {
	gate := Gate{Name: "g", Test: "TestG"}
	pass := `{"Action":"pass","Test":"TestG"}`
	for _, c := range []struct {
		name   string
		stream string
		runErr error
		status Status
		reason string
	}{
		{name: "the named test passed", stream: eventLines(`{"Action":"run","Test":"TestG"}`, pass), status: StatusPass},
		{name: "a test that matched nothing fails", stream: eventLines(`{"Action":"pass"}`), status: StatusFail, reason: "no result"},
		{name: "a skip inside the gate fails", stream: eventLines(`{"Action":"skip","Test":"TestG"}`), status: StatusFail, reason: "skipped"},
		{name: "a skipped subtest fails", stream: eventLines(`{"Action":"skip","Test":"TestG/sub"}`, pass), status: StatusFail, reason: "TestG/sub"},
		{name: "a failed test fails", stream: eventLines(`{"Action":"output","Test":"TestG","Output":"boom\n"}`, `{"Action":"fail","Test":"TestG"}`), runErr: errors.New("exit 1"), status: StatusFail, reason: "boom"},
		{name: "a build failure fails", stream: "# pkg\nbad syntax\n", runErr: errors.New("exit 1"), status: StatusFail, reason: "bad syntax"},
		{name: "a nonzero exit fails despite a pass", stream: eventLines(pass), runErr: errors.New("exit 1"), status: StatusFail, reason: "exit 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := judge(gate, c.stream, c.runErr)
			if got.Status != c.status {
				t.Fatalf("status %q (%s), want %q", got.Status, got.Reason, c.status)
			}
			if !strings.Contains(got.Reason, c.reason) {
				t.Errorf("reason %q lacks %q", got.Reason, c.reason)
			}
		})
	}
}

func suiteOf(goos string, probe Probe, gates ...Gate) (*Suite, *[]string) {
	reqs, err := ParseRequirements(sampleTable)
	if err != nil {
		panic(err)
	}
	ran := &[]string{}
	return &Suite{
		Requirements: reqs,
		GOOS:         goos,
		Probes:       map[string]Probe{"lsof": probe, "docker": probe},
		Gates:        gates,
		Exec: func(_ context.Context, g Gate) Result {
			*ran = append(*ran, g.Name)
			return Result{Gate: g.Name, Status: StatusPass}
		},
	}, ran
}

func TestAPlantedMissingToolFailsTheRunAndNeverStartsTheGate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	reqs, err := ParseRequirements(sampleTable)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	suite := &Suite{
		Requirements: reqs, GOOS: "linux", Probes: probes(),
		Gates: []Gate{{Name: "needs-lsof", Needs: []string{"lsof"}}},
		Exec:  func(context.Context, Gate) Result { started = true; return Result{Status: StatusPass} },
	}
	results := suite.Run(context.Background())
	if started {
		t.Fatal("the gate started without its tool")
	}
	if len(results) != 1 || results[0].Status != StatusFail || !strings.Contains(results[0].Reason, "lsof") {
		t.Fatalf("results %+v", results)
	}
	if Passed(results) {
		t.Fatal("the run passed")
	}
}

func TestADeclaredAbsentCapabilitySkipsOnlyThatGate(t *testing.T) {
	suite, ran := suiteOf("darwin", func() error { return nil },
		Gate{Name: "container", Needs: []string{"docker"}},
		Gate{Name: "plain", Needs: []string{"lsof"}})
	results := suite.Run(context.Background())
	if !Passed(results) {
		t.Fatalf("results %+v", results)
	}
	if len(*ran) != 1 || (*ran)[0] != "plain" {
		t.Fatalf("ran %v", *ran)
	}
	if results[0].Status != StatusSkip || !strings.Contains(results[0].Reason, "docker") {
		t.Fatalf("results %+v", results)
	}
}

func TestAnEmptySelectionIsNotAPass(t *testing.T) {
	if Passed(nil) {
		t.Fatal("no results passed")
	}
}

func TestSelect(t *testing.T) {
	gates := []Gate{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got, err := Select(gates, "")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	got, err = Select(gates, "c,a")
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := Select(gates, "a,nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown gate: %v", err)
	}
}

func TestTheGateTableIsUniqueAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Gates {
		if g.Name == "" || g.Package == "" || g.Test == "" {
			t.Errorf("incomplete gate %+v", g)
		}
		if seen[g.Name] {
			t.Errorf("duplicate gate %s", g.Name)
		}
		seen[g.Name] = true
	}
}

func TestGateEnvironmentCarriesNoHostIdentity(t *testing.T) {
	host := []string{
		"PATH=/usr/bin", "HOME=/somewhere/real", "GH_TOKEN=x", "GITHUB_TOKEN=x", "GH_ENTERPRISE_TOKEN=x",
		"ANTHROPIC_API_KEY=x", "CLAUDE_CONFIG_DIR=x", "OPENAI_API_KEY=x", "HERDR_SOCKET_PATH=x", "HERDR_ENV=1",
		"HERDSMAN_INBOX=x", "GOFLAGS=-mod=vendor", "TMPDIR=/t", "DOCKER_HOST=unix:///d",
	}
	env := gateEnv(host, "/poison", "/fresh-home", map[string]string{"GOCACHE": "/gc", "GOMODCACHE": "/gm"})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Errorf("duplicate %s", k)
		}
		got[k] = v
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "OPENAI_API_KEY", "HERDR_SOCKET_PATH", "HERDR_ENV", "HERDSMAN_INBOX"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s reached the gate", k)
		}
	}
	for k, want := range map[string]string{
		"PATH": "/poison:/usr/bin", "HOME": "/fresh-home", "GOCACHE": "/gc", "GOMODCACHE": "/gm",
		"GOFLAGS": "-mod=vendor", "TMPDIR": "/t", "DOCKER_HOST": "unix:///d",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestPoisonRecordsAnyUseOfAHostTool(t *testing.T) {
	dir := t.TempDir()
	marker, err := plantPoison(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range hostTools {
		if info, err := os.Stat(filepath.Join(dir, tool)); err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s: %v", tool, err)
		}
	}
	if used := poisonUse(marker); used != "" {
		t.Fatalf("before any call: %q", used)
	}
	err = exec.Command(filepath.Join(dir, "gh"), "auth", "status").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("the stand-in answered: %v", err)
	}
	if used := poisonUse(marker); used != "gh auth status" {
		t.Fatalf("used %q", used)
	}
}

// gateModule writes a throwaway module whose tests behave as named, and makes
// it the current directory.
func gateModule(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	body := `package probe

import (
	"os/exec"
	"testing"
)

func TestGood(t *testing.T) {}
func TestBad(t *testing.T)  { t.Fatal("planted failure") }
func TestSkips(t *testing.T) { t.Skip("host lacks it") }
func TestCallsGh(t *testing.T) { _ = exec.Command("gh", "auth", "status").Run() }
`
	for name, text := range map[string]string{"go.mod": "module probe\n\ngo 1.26.0\n", "probe_test.go": body} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("GOFLAGS", "")
}

func TestExecGateJudgesARealGoTestRun(t *testing.T) {
	gateModule(t)
	goEnv, err := goEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		test   string
		status Status
		reason string
	}{
		{"TestGood", StatusPass, ""},
		{"TestBad", StatusFail, "planted failure"},
		{"TestSkips", StatusFail, "skipped inside the gate"},
		{"TestNotWritten", StatusFail, "no result"},
		{"TestCallsGh", StatusFail, "gh auth status"},
	} {
		t.Run(c.test, func(t *testing.T) {
			got := execGate(context.Background(), Gate{Name: "g", Package: ".", Test: c.test}, goEnv)
			if got.Status != c.status || !strings.Contains(got.Reason, c.reason) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestRealMainExitCodes(t *testing.T) {
	gateModule(t)
	saved := Gates
	t.Cleanup(func() { Gates = saved })
	Gates = []Gate{{Name: "good", Package: ".", Test: "TestGood"}, {Name: "bad", Package: ".", Test: "TestBad"}}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-list"}, 0, "good\nbad\n"},
		{[]string{"-gate", "good"}, 0, "PASS good"},
		{[]string{"-gate", "bad"}, 1, "FAIL bad"},
		{nil, 1, "1 passed, 0 skipped, 1 failed"},
		{[]string{"-gate", "nope"}, 2, ""},
		{[]string{"-unknown-flag"}, 2, ""},
	} {
		var out, errOut strings.Builder
		if code := realMain(c.args, &out, &errOut); code != c.code || !strings.Contains(out.String(), c.want) {
			t.Errorf("%v: code %d\n%s%s", c.args, code, out.String(), errOut.String())
		}
	}
}
