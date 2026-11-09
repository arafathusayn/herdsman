// Package up proves `herdsman up` end to end: the real binary and daemon, a
// fake herdr, and a scripted controlling terminal.
package up

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

type machine struct {
	t       *testing.T
	bin     string
	home    string
	repo    string
	private string
	path    string
	srv     *herdrtest.Server
	world   *herdrtest.World
}

func (m *machine) env() []string {
	return []string{
		"HOME=" + m.home, "PATH=" + m.path, "HERDSMAN_PRIVATE_HOME=" + m.private, "HERDR_SOCKET_PATH=" + m.srv.Socket,
		"TMPDIR=" + os.TempDir(), "GOCACHE=" + filepath.Join(m.home, "gocache"),
	}
}

func setup(t *testing.T) *machine {
	t.Helper()
	// A short root: the daemon's socket path has a small limit on macOS.
	root, err := os.MkdirTemp("", "up")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	m := &machine{t: t, home: filepath.Join(root, "h"), repo: filepath.Join(root, "repo"), private: filepath.Join(root, "p"), bin: filepath.Join(root, "herdsman")}
	build := exec.Command("go", "build", "-o", m.bin, "github.com/arafathusayn/herdsman/cmd/herdsman")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	tools := filepath.Join(root, "bin")
	for _, dir := range []string{m.home, m.repo, tools, filepath.Join(m.home, ".codex")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(m.private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.home, ".codex", "config.toml"), []byte("sandbox_mode = \"workspace-write\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nif [ \"$1\" = agent ]; then echo '[possible values: claude, codex]'; fi\n"
	if err := os.WriteFile(filepath.Join(tools, "herdr"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	// doctor stops up when gh is installed and not logged in. The test must not
	// depend on the host's gh, so a gh that reports a login comes first on PATH.
	// The named harnesses get the same treatment: a machine without them, or
	// with a logged-in copy, must see the same result.
	for _, tool := range []string{"gh", "claude", "codex"} {
		if err := os.WriteFile(filepath.Join(tools, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	m.path = tools + ":" + filepath.Dir(git) + ":/usr/bin:/bin"
	if out, err := exec.Command("git", "-C", m.repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	m.srv = herdrtest.Start(t)
	fix := herdrtest.Serve(m.srv)
	m.world = herdrtest.ServeWorld(fix)
	m.startDaemon()
	return m
}

func (m *machine) startDaemon() {
	m.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, m.bin, "daemon", "run")
	cmd.Env = m.env()
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if m.t.Failed() {
			m.t.Logf("daemon log:\n%s", log.String())
		}
	})
	sock := filepath.Join(m.private, "run", "daemon.sock")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.t.Fatalf("the daemon did not come up:\n%s", log.String())
}

func (m *machine) up(args ...string) (string, error) {
	cmd := exec.Command(m.bin, append([]string{"up"}, args...)...)
	cmd.Dir = m.repo
	cmd.Env = m.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDryRunPlansAndStartsNothing(t *testing.T) {
	m := setup(t)
	out, err := m.up("harbor", "-p", "ship it", "-a", "claude doctor && claude", "-o", "codex", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"claude doctor", "workspace: repo-harbor", "agent: 'codex'"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan misses %q:\n%s", want, out)
		}
	}
	if len(m.world.Inputs("p1")) != 0 || m.world.PaneByLabel("adjutant") != "" {
		t.Fatal("a dry run touched herdr")
	}
	if _, err := os.Stat(filepath.Join(m.repo, ".herdsman")); !os.IsNotExist(err) {
		t.Fatalf("a dry run made the folder: %v", err)
	}
}

func TestMissingFlagsAreAUsageError(t *testing.T) {
	m := setup(t)
	out, err := m.up("harbor", "-p", "x")
	var exit *exec.ExitError
	if err == nil || !asExit(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(out, "usage: herdsman up") {
		t.Fatalf("%v: %s", err, out)
	}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestManualPairOnAScriptedTerminal(t *testing.T) {
	m := setup(t)
	master, slave := openPTY(t)
	defer master.Close()
	outPath := filepath.Join(m.home, "up.out")
	output, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	// The up process must have no coding agent among its ancestors, and this
	// test may itself run under one. A helper starts it and exits at once, so
	// it is orphaned before it asks the daemon anything.
	helper := exec.Command(os.Args[0])
	helper.Dir = m.repo
	helper.Env = append(m.env(), orphanEnv+"=1")
	helper.Stdin, helper.Stdout, helper.Stderr = slave, output, output
	helper.Args = append([]string{os.Args[0]}, m.bin, "up", "harbor", "-p", "ship it", "-a", "claude", "-o", "codex", "--pair", "manual", "--no-attach")
	if err := helper.Run(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	slave.Close()
	var screen syncBuffer
	go func() { _, _ = io.Copy(&screen, master) }()
	readOutput := func() string { b, _ := os.ReadFile(outPath); return string(b) }
	deadline := time.Now().Add(60 * time.Second)
	for !strings.Contains(screen.String(), "approve this pair?") {
		if time.Now().After(deadline) {
			t.Fatalf("no question on the terminal:\n%s\n%s", screen.String(), readOutput())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := master.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(readOutput(), "route harbor is up") {
		if time.Now().After(deadline) {
			t.Fatalf("up did not finish:\n%s", readOutput())
		}
		time.Sleep(20 * time.Millisecond)
	}
	out := outputText(readOutput())
	if !strings.Contains(out.String(), "route harbor is up") {
		t.Fatalf("output:\n%s", out.String())
	}
	for _, name := range []string{"repo-harbor-adjutant", "repo-harbor-orchestrator"} {
		if n := m.world.Starts(name); n != 1 {
			t.Fatalf("%s started %d times", name, n)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(m.private, "repo-harbor", "pair-approval.json"))
	if len(matches) != 1 {
		t.Fatal("no approval record")
	}
	record, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(record), `"manual":true`) {
		t.Fatalf("record: %s", record)
	}
	// A second up finds the route already up and starts nothing.
	second, err := m.up("harbor", "-p", "ship it", "-a", "claude", "-o", "codex", "--pair", "manual", "--no-attach")
	if err != nil {
		t.Fatalf("second up: %v\n%s", err, second)
	}
	if n := m.world.Starts("repo-harbor-adjutant"); n != 1 {
		t.Fatalf("second up started the agent again: %d", n)
	}
}

// outputText exists so the final assertions read like the others.
type outputText string

func (o outputText) String() string { return string(o) }

const orphanEnv = "HERDSMAN_ACCEPTANCE_ORPHAN"

// TestMain doubles as the helper that starts a command in a session of its
// own, on the terminal it was given as standard input, and exits.
func TestMain(m *testing.M) {
	if os.Getenv(orphanEnv) != "1" {
		os.Exit(m.Run())
	}
	cmd := exec.Command(os.Args[1], os.Args[2:]...)
	env := os.Environ()
	cmd.Env = env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, orphanEnv+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
