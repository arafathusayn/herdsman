package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
	"github.com/arafathusayn/herdsman/internal/machine"
)

// fakes are probes where every check passes; each test breaks one.
func fakes(t *testing.T, env map[string]string) Probes {
	t.Helper()
	fake := herdrtest.Start(t)
	exe := filepath.Join(t.TempDir(), "herdsman")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if env == nil {
		env = map[string]string{}
	}
	if _, ok := env["HERDR_ENV"]; !ok {
		env["HERDR_ENV"] = "1"
	}
	return Probes{
		Getenv:      func(k string) string { return env[k] },
		Herdr:       herdr.New(fake.Socket),
		LookPath:    func(name string) (string, error) { return exe, nil },
		GhAuth:      func(context.Context, string) error { return nil },
		Executable:  func() (string, error) { return exe, nil },
		GoCache:     func() (string, error) { return t.TempDir(), nil },
		WriteDir:    func(string) error { return nil },
		ReadMemory:  func() error { return nil },
		Renice:      func() error { return nil },
		ReadLoad:    func() (machine.Load, error) { return machine.Load{1.5, 1, 1}, nil },
		FreePercent: func() (int, error) { return 50, nil },
		CPUs:        func() int { return 8 },
		TempDir:     "/private/tmp",
	}
}

func run(t *testing.T, p Probes) []Check {
	t.Helper()
	cs, err := Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func find(cs []Check, name string) []Check {
	var out []Check
	for _, c := range cs {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func one(t *testing.T, cs []Check, name, level, detail string) {
	t.Helper()
	got := find(cs, name)
	if len(got) != 1 || got[0].Level != level || !strings.Contains(got[0].Detail, detail) {
		t.Errorf("%s: want one %s containing %q, got %+v", name, level, detail, got)
	}
}

func TestAllOK(t *testing.T) {
	cs := run(t, fakes(t, nil))
	for _, n := range []string{"herdr", "gh", "binary", "sandbox", "load"} {
		if got := find(cs, n); len(got) != 1 || got[0].Level != OK {
			t.Errorf("%s: %+v", n, got)
		}
	}
	if len(find(cs, "inbox")) != 0 {
		t.Error("inbox checked without HERDSMAN_INBOX")
	}
	if Failed(cs) {
		t.Error("failed")
	}
}

func TestHerdr(t *testing.T) {
	p := fakes(t, nil)
	one(t, run(t, p), "herdr", OK, "herdr 0.9.4 protocol 22 at ")

	p = fakes(t, map[string]string{"HERDR_ENV": ""})
	one(t, run(t, p), "herdr", Warn, "HERDR_ENV is not 1, so this shell is not inside herdr: a route will ask whether its agents run in Herdr panes or as background subagents")

	p = fakes(t, nil)
	p.Herdr = herdr.New(filepath.Join(t.TempDir(), "none.sock"))
	cs := run(t, p)
	one(t, cs, "herdr", Fail, "not reachable")
	if !Failed(cs) {
		t.Error("not failed")
	}

	p = fakes(t, nil)
	p.Herdr = nil
	one(t, run(t, p), "herdr", Fail, "no herdr socket")
}

func TestGh(t *testing.T) {
	p := fakes(t, nil)
	one(t, run(t, p), "gh", OK, "gh auth status: exit 0")

	p.LookPath = func(name string) (string, error) {
		if name == "gh" {
			return "", exec.ErrNotFound
		}
		return "/x/herdsman", nil
	}
	one(t, run(t, p), "gh", Warn, "gh not found on PATH")

	p = fakes(t, nil)
	p.GhAuth = func(context.Context, string) error { return errors.New("exit status 1") }
	one(t, run(t, p), "gh", Fail, "gh auth status: exit status 1")
}

// The real gh probe runs gh as a program and reads only its exit code.
func TestGhAuthProgram(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "gh-ok")
	bad := filepath.Join(dir, "gh-bad")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\n[ \"$1 $2\" = \"auth status\" ] || exit 9\necho noise\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho not logged in >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ghAuth(context.Background(), ok); err != nil {
		t.Errorf("ok: %v", err)
	}
	if err := ghAuth(context.Background(), bad); err == nil || err.Error() != "exit status 1" {
		t.Errorf("bad: %v", err)
	}
}

func TestBinary(t *testing.T) {
	p := fakes(t, nil)
	exe, _ := p.Executable()
	// sha256 of "binary"
	one(t, run(t, p), "binary", OK, exe+" sha256=9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd")

	other := filepath.Join(t.TempDir(), "herdsman")
	if err := os.WriteFile(other, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	p.LookPath = func(name string) (string, error) { return other, nil }
	realOther, _ := filepath.EvalSymlinks(other)
	one(t, run(t, p), "binary", Warn, "not the herdsman first on PATH ("+realOther+")")

	p.LookPath = func(name string) (string, error) { return "", exec.ErrNotFound }
	one(t, run(t, p), "binary", Warn, "no herdsman on PATH")

	p = fakes(t, nil)
	p.Executable = func() (string, error) { return "", errors.New("no exe") }
	one(t, run(t, p), "binary", Fail, "no exe")

	p = fakes(t, nil)
	p.Executable = func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }
	one(t, run(t, p), "binary", Fail, "no such file")
}

func TestInbox(t *testing.T) {
	c := inboxtest.New(t)
	env := map[string]string{"HERDSMAN_INBOX": c.Dir, "HERDSMAN_PREFLIGHT": c.Proof, "HERDSMAN_MESSAGES_DIR": c.Installed}
	one(t, run(t, fakes(t, env)), "inbox", OK, c.Dir)
	// Read-only: the doctor creates nothing in the inbox.
	if _, err := os.Stat(filepath.Join(c.Dir, "state")); err == nil {
		t.Error("doctor created state/")
	}

	env["HERDSMAN_MESSAGES_DIR"] = t.TempDir()
	one(t, run(t, fakes(t, env)), "inbox", Fail, "proof stale")

	env["HERDSMAN_INBOX"] = "relative"
	cs := run(t, fakes(t, env))
	one(t, cs, "inbox", Fail, "inbox must be absolute")
	if !Failed(cs) {
		t.Error("not failed")
	}
}

func TestSandbox(t *testing.T) {
	p := fakes(t, nil)
	one(t, run(t, p), "sandbox", OK, "go build cache, memory reading and setpriority allowed")

	p.WriteDir = func(string) error { return &os.PathError{Op: "open", Path: "x", Err: syscall.EPERM} }
	p.ReadMemory = func() error { return syscall.EPERM }
	p.Renice = func() error { return syscall.EACCES }
	cs := run(t, p)
	got := find(cs, "sandbox")
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	for i, want := range []string{"go build cache ", "memory reading denied: operation not permitted", "setpriority denied: permission denied", "use GOCACHE=/private/tmp/herdsman-go-build STATICCHECK_CACHE=/private/tmp/herdsman-staticcheck"} {
		if got[i].Level != Warn || !strings.Contains(got[i].Detail, want) {
			t.Errorf("%d: %+v", i, got[i])
		}
	}
	if Failed(cs) {
		t.Error("sandbox denials are warnings")
	}

	p = fakes(t, nil)
	p.GoCache = func() (string, error) { return "", errors.New("no home") }
	if got := find(run(t, p), "sandbox"); len(got) != 2 || got[0].Detail != "go build cache denied: no home" {
		t.Errorf("%+v", got)
	}
}

func TestLoad(t *testing.T) {
	p := fakes(t, nil)
	one(t, run(t, p), "load", OK, "load 1.50 (limit 8), free 50% (min 10)")

	p = fakes(t, map[string]string{"HERDSMAN_LOAD_LIMIT": "1.5", "HERDSMAN_MIN_FREE": "060"})
	one(t, run(t, p), "load", Warn, "load 1.50 (limit 1.5), free 50% (min 60)")

	p = fakes(t, nil)
	p.ReadLoad = func() (machine.Load, error) { return machine.Load{}, errors.New("no load") }
	one(t, run(t, p), "load", Warn, "cannot read the machine: no load")

	p = fakes(t, nil)
	p.FreePercent = func() (int, error) { return 0, errors.New("no mem") }
	one(t, run(t, p), "load", Warn, "cannot read the machine: no mem")
}

func TestStrictSettings(t *testing.T) {
	for _, tc := range []struct{ key, val, want string }{
		{"HERDSMAN_MIN_FREE", "ten", `HERDSMAN_MIN_FREE: not a whole number (got "ten")`},
		{"HERDSMAN_MIN_FREE", "101", `HERDSMAN_MIN_FREE: out of range 0 to 100 (got "101")`},
		{"HERDSMAN_LOAD_LIMIT", "x", `HERDSMAN_LOAD_LIMIT: not a positive number (got "x")`},
		{"HERDSMAN_LOAD_LIMIT", "0", `HERDSMAN_LOAD_LIMIT: not a positive number (got "0")`},
		{"HERDSMAN_LOAD_LIMIT", "Inf", `HERDSMAN_LOAD_LIMIT: not a positive number (got "Inf")`},
	} {
		p := fakes(t, map[string]string{tc.key: tc.val})
		_, err := Run(context.Background(), p)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s=%s: %v", tc.key, tc.val, err)
		}
	}
	for _, v := range []string{" 7 ", "+7", "007"} {
		if n, err := wholeNumber("X", v, 0, 100); err != nil || n != 7 {
			t.Errorf("%q: %d %v", v, n, err)
		}
	}
}

func FuzzSettings(f *testing.F) {
	for _, s := range []string{"10", "010", " 7", "+3", "-1", "1e3", "x", "", "99999999999999999999", "2.5"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if n, err := wholeNumber("X", s, 0, 100); err == nil && (n < 0 || n > 100) {
			t.Errorf("%q: %d out of range", s, n)
		}
		if v, err := positiveNumber("X", s); err == nil && !(v > 0) {
			t.Errorf("%q: %v", s, v)
		}
	})
}

// The real probes run without error outside a sandbox; inside one, the
// denial is the error itself.
func TestRealProbes(t *testing.T) {
	if err := renice(); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skip("sandbox: setpriority denied")
		}
		t.Fatal(err)
	}
	if err := readMemory(); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skip("sandbox: memory reading denied")
		}
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeDir(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("writeDir left %v", entries)
	}
	if err := writeDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("no error for a missing folder")
	}
	t.Setenv("GOCACHE", "/x/cache")
	if d, err := goCache(); err != nil || d != "/x/cache" {
		t.Errorf("%q %v", d, err)
	}
	t.Setenv("GOCACHE", "")
	if d, err := goCache(); err != nil || filepath.Base(d) != "go-build" {
		t.Errorf("%q %v", d, err)
	}
	d := Default()
	if d.Herdr == nil || d.TempDir == "" || d.CPUs() < 1 {
		t.Errorf("%+v", d)
	}
}

func TestFast(t *testing.T) {
	start := time.Now()
	run(t, fakes(t, nil))
	if time.Since(start) > 2*time.Second {
		t.Error("over 2 s")
	}
}

func TestBinaryUnreadable(t *testing.T) {
	p := fakes(t, nil)
	exe, _ := p.Executable()
	if err := os.Chmod(exe, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(exe); err == nil {
		f.Close()
		t.Skip("running as a user who reads mode-0 files")
	}
	one(t, run(t, p), "binary", Fail, "permission denied")

	dir := t.TempDir()
	p.Executable = func() (string, error) { return dir, nil }
	one(t, run(t, p), "binary", Fail, "is a directory")
}

func TestInboxTimingStale(t *testing.T) {
	c := inboxtest.New(t)
	if err := os.Mkdir(filepath.Join(c.Dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "state", "timing.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HERDSMAN_INBOX": c.Dir, "HERDSMAN_PREFLIGHT": c.Proof, "HERDSMAN_MESSAGES_DIR": c.Installed}
	one(t, run(t, fakes(t, env)), "inbox", Fail, "invalid or conflicting shared timing configuration")
}

func TestGoCacheNoHome(t *testing.T) {
	t.Setenv("GOCACHE", "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := goCache(); err == nil {
		t.Error("no error without a home folder")
	}
}
