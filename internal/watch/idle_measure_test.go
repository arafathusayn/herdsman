package watch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// A24 measurement, run by hand: the built binary's two watchers stay
// connected to a fake Herdr socket and idle for HERDSMAN_IDLE_SECONDS
// (default 300), next to the load publisher, and each one's peak resident
// memory and CPU time are logged. It runs only when HERDSMAN_IDLE_BIN names
// the binary.
func TestConnectedIdleMeasurement(t *testing.T) {
	bin := os.Getenv("HERDSMAN_IDLE_BIN")
	if bin == "" {
		t.Skip("HERDSMAN_IDLE_BIN is not set")
	}
	secs := 300
	if v, err := strconv.Atoi(os.Getenv("HERDSMAN_IDLE_SECONDS")); err == nil && v > 0 {
		secs = v
	}
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	for _, p := range []string{"w2:pA", "w2:pR"} {
		fx.SetAgent(p, herdrtest.Agent(p, "s"))
		fx.SetScreen(p, "working on it")
	}
	dir := t.TempDir()
	type proc struct {
		name string
		cmd  *exec.Cmd
		out  bytes.Buffer
	}
	start := func(name string, env []string, args ...string) *proc {
		p := &proc{name: name, cmd: exec.Command(bin, args...)}
		p.cmd.Env = append(append(os.Environ(), "HERDR_SOCKET_PATH="+srv.Socket, "HERDSMAN_REPORTS="+dir,
			"HERDSMAN_STATE="+filepath.Join(dir, "state-"+name), "HERDSMAN_IMPLEMENTERS=im-a"), env...)
		p.cmd.Stdout, p.cmd.Stderr = &p.out, &p.out
		if err := p.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return p
	}
	procs := []*proc{
		start("implementers", nil, "watch", "implementers"),
		start("reviewer", nil, "watch", "reviewer"),
		start("publisher", []string{"HERDSMAN_PUBLISH_SECONDS=" + strconv.Itoa(secs+30), "HERDSMAN_READING=" + filepath.Join(dir, "load")},
			"run-check", "--publish", "1"),
	}
	time.Sleep(time.Duration(secs/2) * time.Second)
	if n := srv.Subscribers(); n != 2 {
		t.Errorf("%d subscriptions halfway, want 2 (both watchers connected)", n)
	}
	time.Sleep(time.Duration(secs-secs/2) * time.Second)
	for _, p := range procs {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	for _, p := range procs {
		_ = p.cmd.Wait()
		ru, _ := p.cmd.ProcessState.SysUsage().(*syscall.Rusage)
		if ru == nil {
			t.Fatalf("%s: no resource usage", p.name)
		}
		cpu := time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
		// ru_maxrss is in bytes on darwin.
		t.Logf("%s: %ds connected and idle, max RSS %d bytes (%.1f MB), CPU %s (user %s, sys %s)",
			p.name, secs, ru.Maxrss, float64(ru.Maxrss)/1e6, cpu.Round(time.Millisecond),
			time.Duration(ru.Utime.Nano()).Round(time.Millisecond), time.Duration(ru.Stime.Nano()).Round(time.Millisecond))
		if ru.Maxrss > 30e6 || cpu > 1500*time.Millisecond {
			t.Errorf("%s over the A24 budget (30 MB, 1.5 s CPU)", p.name)
		}
	}
}
