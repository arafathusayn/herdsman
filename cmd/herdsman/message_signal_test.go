package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// A running herdsman message courier that gets INT, TERM or HUP frees its
// courier lock and exits 128+signal, as the script's traps do.
func TestMessageSignalReleasesLocks(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			srv := herdrtest.Start(t)
			fx := herdrtest.Serve(srv)
			c := inboxtest.New(t)
			c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
			for _, a := range []map[string]any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")} {
				fx.SetAgent(a["pane_id"].(string), a)
			}
			cmd := exec.Command(os.Args[0], "message", "courier")
			cmd.Env = append(append(os.Environ(), c.Env()...), "HERDSMAN_TEST_MAIN=1", "HERDR_SOCKET_PATH="+srv.Socket,
				"HERDR_PANE_ID=sender-pane", "HERDSMAN_NAME=route-adjutant", "RUN_FOR=0", "SWEEP=60")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(c.Dir, "courier.lock")
			deadline := time.Now().Add(10 * time.Second)
			for !inbox.Exists(lock) || srv.Subscribers() == 0 {
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatal("the courier never took its lock and subscribed")
				}
				time.Sleep(20 * time.Millisecond)
			}
			_ = cmd.Process.Signal(sig)
			err := cmd.Wait()
			if code := cmd.ProcessState.ExitCode(); code != 128+int(sig) {
				t.Errorf("exit %d (%v), want %d", code, err, 128+int(sig))
			}
			if inbox.Exists(lock) {
				t.Error("the courier lock is left behind")
			}
		})
	}
}
