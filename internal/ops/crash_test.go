package ops_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/ops/crashharness"
)

func TestMain(m *testing.M) {
	if os.Getenv(crashharness.EnvDir) != "" {
		os.Exit(crashharness.Child())
	}
	os.Exit(m.Run())
}

func runChild(t *testing.T, dir, phase, point, trace string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), crashharness.EnvDir+"="+dir, crashharness.EnvPhase+"="+phase, crashharness.EnvPoint+"="+point, crashharness.EnvTrace+"="+trace)
	out, err := cmd.CombinedOutput()
	if err != nil && !killed(err) {
		t.Fatalf("child %s at %q: %v\n%s", phase, point, err, out)
	}
	return err
}

func killed(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	st, ok := ee.Sys().(syscall.WaitStatus)
	return ok && st.Signaled() && st.Signal() == syscall.SIGKILL
}

func newHarnessDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ops")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := crashharness.Prepare(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// boundaries runs the script without a fault and returns every boundary it reached.
func boundaries(t *testing.T) []string {
	t.Helper()
	dir := newHarnessDir(t)
	trace := filepath.Join(dir, "trace")
	if err := runChild(t, dir, "run", "", trace); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func assertOneEffect(t *testing.T, dir string) {
	t.Helper()
	for _, k := range crashharness.Keys {
		b, err := os.ReadFile(filepath.Join(crashharness.Effects(dir), k+".log"))
		if err != nil {
			t.Fatalf("effect %s: %v", k, err)
		}
		if n := strings.Count(string(b), "\n"); n != 1 {
			t.Fatalf("effect %s ran %d times:\n%s", k, n, b)
		}
	}
	ents, err := os.ReadDir(crashharness.Sink(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != len(crashharness.Keys) {
		t.Fatalf("%d events delivered, want %d", len(ents), len(crashharness.Keys))
	}
	db, err := store.Open(crashharness.DB(dir), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := ops.NewEngine(db, ops.Options{})
	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != ops.OpDone {
		t.Fatalf("operations after recovery: %+v", list)
	}
	if held, err := e.HeldLocks(); err != nil || len(held) != 0 {
		t.Fatalf("locks still held: %v (%v)", held, err)
	}
	if n, err := e.Undelivered(); err != nil || n != 0 {
		t.Fatalf("undelivered events: %d (%v)", n, err)
	}
}

func TestKillAtEveryBoundaryLeavesOneEffect(t *testing.T) {
	points := boundaries(t)
	if len(points) < 3*3+2+3 {
		t.Fatalf("too few boundaries: %v", points)
	}
	for _, p := range points {
		t.Run(strings.ReplaceAll(p, ":", "-"), func(t *testing.T) {
			dir := newHarnessDir(t)
			if err := runChild(t, dir, "run", p, ""); !killed(err) {
				t.Fatalf("the child was not killed at %s: %v", p, err)
			}
			// A second crash at the same boundary during recovery, when it is reached again.
			_ = runChild(t, dir, "recover", p, "")
			if err := runChild(t, dir, "recover", "", ""); err != nil {
				t.Fatalf("recover: %v", err)
			}
			assertOneEffect(t, dir)
			// Recovery of a finished journal changes nothing.
			if err := runChild(t, dir, "recover", "", ""); err != nil {
				t.Fatalf("second recover: %v", err)
			}
			assertOneEffect(t, dir)
		})
	}
}
