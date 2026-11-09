package up

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func (m *machine) verb(args ...string) (string, error) {
	cmd := exec.Command(m.bin, args...)
	cmd.Dir = m.repo
	cmd.Env = m.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (m *machine) must(args ...string) string {
	m.t.Helper()
	out, err := m.verb(args...)
	if err != nil {
		m.t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func TestALeftPendingPairIsReportedAndTheRouteCanBeDeleted(t *testing.T) {
	m := setup(t)
	// Nothing attests this process, so up leaves the pair pending and exits.
	out, err := m.up("harbor", "-p", "ship it", "-a", "claude", "-o", "codex", "--no-attach")
	if err == nil || !strings.Contains(out, "left pending") {
		t.Fatalf("%v\n%s", err, out)
	}
	status := m.must("status", "--route", "harbor")
	if !strings.Contains(status, "<-- PAIR PENDING") {
		t.Fatalf("status does not report the pair:\n%s", status)
	}
	// Nothing in this build can free the route's tasks, so delete must be told
	// to leave them; until then it refuses before it stops anything.
	if out, err := m.verb("delete", "harbor", "--force", "--grace", "1", "-y"); err == nil || !strings.Contains(out, "keep-worktrees") {
		t.Fatalf("delete that cannot run down: %v\n%s", err, out)
	}
	before := m.must("status", "--route", "harbor")
	if !strings.Contains(before, "route harbor: running") {
		t.Fatalf("a refused delete changed the route:\n%s", before)
	}
	folder := strings.TrimSpace(before[strings.Index(before, "folder: ")+len("folder: ") : strings.Index(before, "\nreason")])
	done, err := m.verb("delete", "harbor", "--force", "--keep-worktrees", "--grace", "1", "-y")
	if err != nil || !strings.Contains(done, "route harbor is deleted") {
		t.Fatalf("delete: %v\n%s", err, done)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the route folder stayed: %v", err)
	}
	_, id, ok := strings.Cut(done, "herdsman undo ")
	if !ok {
		t.Fatalf("delete does not say how to undo it:\n%s", done)
	}
	m.must("undo", strings.TrimSpace(id))
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("undo did not bring the route folder back: %v", err)
	}
}

func TestPauseResumeAndStopFromTheCommandLine(t *testing.T) {
	m := setup(t)
	if out, err := m.up("quay", "-p", "ship it", "--no-adjutant", "-o", "codex", "--no-attach"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if plan := m.must("pause", "quay", "--dry-run"); !strings.Contains(plan, "interrupt") {
		t.Fatalf("%s", plan)
	}
	if st := m.must("status", "--route", "quay"); !strings.HasPrefix(st, "route quay: running") {
		t.Fatalf("a dry run changed the route:\n%s", st)
	}
	if out, err := m.verb("pause", "quay"); err == nil || !strings.Contains(out, "-y") {
		t.Fatalf("pause with no terminal: %v\n%s", err, out)
	}
	if out := m.must("pause", "quay", "-y"); !strings.Contains(out, "is paused") {
		t.Fatalf("%s", out)
	}
	if out := m.must("pause", "quay", "-y"); !strings.Contains(out, "already paused") {
		t.Fatalf("%s", out)
	}
	if out := m.must("resume", "quay", "-y", "-p", "carry on"); !strings.Contains(out, "is running") {
		t.Fatalf("%s", out)
	}
	if out := m.must("stop", "quay", "-y", "--grace", "1"); !strings.Contains(out, "is stopped") {
		t.Fatalf("%s", out)
	}
	if st := m.must("status", "--route", "quay", "--json"); !strings.Contains(st, `"control":"stopped"`) {
		t.Fatalf("%s", st)
	}
}
