package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/doctor"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/machine"
)

func TestHelpListsTools(t *testing.T) {
	out, _, _ := call(t, "help")
	for _, name := range []string{"doctor", "review", "models"} {
		if !regexp.MustCompile(`(?m)^  ` + name + ` `).MatchString(out) {
			t.Errorf("help does not list %s", name)
		}
	}
}

// fakeDoctor points the doctor at fakes; fail makes the herdr check fail.
func fakeDoctor(t *testing.T, fail bool) {
	t.Helper()
	fake := herdrtest.Start(t)
	sock := fake.Socket
	if fail {
		sock = filepath.Join(t.TempDir(), "none.sock")
	}
	exe := filepath.Join(t.TempDir(), "herdsman")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDSMAN_INBOX", "")
	t.Setenv("HERDSMAN_LOAD_LIMIT", "")
	t.Setenv("HERDSMAN_MIN_FREE", "")
	doctorProbes = func() doctor.Probes {
		return doctor.Probes{
			Getenv: os.Getenv, Herdr: herdr.New(sock),
			LookPath:   func(string) (string, error) { return exe, nil },
			GhAuth:     func(context.Context, string) error { return nil },
			Executable: func() (string, error) { return exe, nil },
			GoCache:    func() (string, error) { return t.TempDir(), nil },
			WriteDir:   func(string) error { return nil }, ReadMemory: func() error { return nil }, Renice: func() error { return nil },
			ReadLoad:    func() (machine.Load, error) { return machine.Load{1, 1, 1}, nil },
			FreePercent: func() (int, error) { return 50, nil }, CPUs: func() int { return 4 }, TempDir: "/private/tmp",
		}
	}
	t.Cleanup(func() { doctorProbes = doctor.Default })
}

func TestDoctorCommand(t *testing.T) {
	fakeDoctor(t, false)
	out, errOut, code := call(t, "doctor")
	if code != 0 || errOut != "" || !regexp.MustCompile(`(?m)^ok herdr herdr 0\.9\.4 protocol 22 at .*\nok gh gh auth status: exit 0 .*\nok binary .*\nok sandbox .*\nok load load 1\.00 \(limit 4\), free 50% \(min 10\)\n$`).MatchString(out) {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	out, _, code = call(t, "doctor", "--json")
	var rep struct {
		OK     bool `json:"ok"`
		Checks []struct{ Level, Check, Detail string }
	}
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || !rep.OK || len(rep.Checks) != 5 || rep.Checks[0].Check != "herdr" || strings.Count(out, "\n") != 1 {
		t.Errorf("%d %q", code, out)
	}

	fakeDoctor(t, true)
	out, _, code = call(t, "doctor")
	if code != 1 || !strings.HasPrefix(out, "fail herdr ") {
		t.Errorf("%d %q", code, out)
	}
	out, _, code = call(t, "doctor", "--json")
	if code != 1 || !strings.HasPrefix(out, `{"ok":false,`) {
		t.Errorf("%d %q", code, out)
	}
}

func TestDoctorUsage(t *testing.T) {
	fakeDoctor(t, false)
	out, _, code := call(t, "doctor", "--help")
	if code != 0 || !strings.Contains(out, "Exit codes:\n  0  no check failed") {
		t.Errorf("%d %q", code, out)
	}
	for _, args := range [][]string{{"doctor", "x"}, {"doctor", "--nope"}} {
		_, errOut, code := call(t, args...)
		if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman doctor") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	t.Setenv("HERDSMAN_MIN_FREE", "lots")
	out, errOut, code := call(t, "doctor")
	if code != 2 || out != "" || errOut != "herdsman doctor: HERDSMAN_MIN_FREE: not a whole number (got \"lots\")\n" {
		t.Errorf("%d %q %q", code, out, errOut)
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestReviewStartCommand(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	gitT(t, base, "init", "-q", repo)
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	head := gitT(t, repo, "rev-parse", "HEAD")
	wt := filepath.Join(base, "rv")
	caches := filepath.Join(base, "caches")

	out, errOut, code := call(t, "review", "start", "--repo", repo, "--worktree", wt, "--head", "HEAD", "--cache-root", caches)
	want := "worktree=" + wt + "\nhead=" + head + "\nGOCACHE=" + caches + "/rv-go-build\nSTATICCHECK_CACHE=" + caches + "/rv-staticcheck\n"
	if code != 0 || out != want {
		t.Errorf("%d %q %q", code, out, errOut)
	}
	out, _, code = call(t, "review", "start", "--repo", repo, "--worktree", wt, "--head", head, "--cache-root", caches, "--json")
	if code != 0 || out != `{"worktree":"`+wt+`","head":"`+head+`","gocache":"`+caches+`/rv-go-build","staticcheck_cache":"`+caches+`/rv-staticcheck"}`+"\n" {
		t.Errorf("%d %q", code, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = call(t, "review", "start", "--repo", repo, "--worktree", wt, "--head", head, "--cache-root", caches)
	if code != 1 || errOut != "herdsman review start: worktree "+wt+" has changes: x\n" {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestReviewUsage(t *testing.T) {
	for _, args := range [][]string{{"review", "--help"}, {"review", "start", "--help"}} {
		out, _, code := call(t, args...)
		if code != 0 || !strings.Contains(out, "Exit codes:") {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
	for _, args := range [][]string{{"review"}, {"review", "stop"}, {"review", "start"}, {"review", "start", "--head", "x"}, {"review", "start", "--worktree", "w", "--head", "h", "extra"}, {"review", "start", "--bad"}} {
		_, errOut, code := call(t, args...)
		if code != 2 || !strings.HasPrefix(errOut, "usage: herdsman review start") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
}
