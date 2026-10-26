package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// TestMain runs the command itself when a test starts this binary as a
// herdsman process (HERDSMAN_TEST_MAIN=1).
func TestMain(m *testing.M) {
	if os.Getenv("HERDSMAN_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// R2 finding 6: a gate interrupted while it holds the action lock and the
// input lock (here, during its authority check) frees both and exits
// 128+signal, as the script's traps do.
func TestGateSignalReleasesItsLocks(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			srv := herdrtest.Start(t)
			fx := herdrtest.Serve(srv)
			c := inboxtest.New(t)
			c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
			agents := []map[string]any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}
			for _, a := range agents {
				fx.SetAgent(a["pane_id"].(string), a)
			}
			c.RelayBoundary(agents...)
			apr := filepath.Join(c.Dir, "approvals")
			draft := filepath.Join(c.Base, "draft.txt")
			c.Write(draft, "reviewed text\n")
			sum, _ := inbox.DigestFile(draft)
			goFile := filepath.Join(c.Base, "go.json")
			c.WriteJSON(goFile, map[string]any{"version": 1, "type": "user_go", "question_id": "choice", "request_id": "s1", "action": "post",
				"target": "pr-comment", "revision": "head1", "draft_hash": sum, "expires_at": time.Now().Unix() + 600, "text": "post this draft",
				"human_interaction": "ask-s1"})
			c.WriteJSON(filepath.Join(apr, "interactions", "ask-s1.json"), map[string]any{"source": "ask-tool", "question_id": "choice",
				"request_id": "s1", "revision": "head1", "approved": true, "action": "post", "target": "pr-comment", "draft_hash": sum})
			// The authority check lists the agents: hold it there.
			called, release := make(chan struct{}, 1), make(chan struct{})
			t.Cleanup(func() { close(release) })
			srv.Handle("agent.list", func(map[string]any) (any, string) {
				select {
				case called <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
				return map[string]any{"agents": []any{}}, ""
			})
			cmd := exec.Command(os.Args[0], "approval-gate", "consume", goFile, "post", "pr-comment", "head1", draft)
			cmd.Env = append(append(os.Environ(), c.Env()...), "HERDSMAN_TEST_MAIN=1", "HERDR_SOCKET_PATH="+srv.Socket)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-called:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("the gate never reached its authority check")
			}
			for _, l := range []string{filepath.Join(apr, inbox.ActionLock), filepath.Join(c.Dir, inbox.MutationLock)} {
				if !inbox.Exists(l) {
					t.Fatalf("the gate does not hold %s", l)
				}
			}
			_ = cmd.Process.Signal(sig)
			err := cmd.Wait()
			if code := cmd.ProcessState.ExitCode(); code != 128+int(sig) {
				t.Errorf("exit %d (%v), want %s", code, err, strconv.Itoa(128+int(sig)))
			}
			for _, l := range []string{filepath.Join(apr, inbox.ActionLock), filepath.Join(c.Dir, inbox.MutationLock)} {
				if inbox.Exists(l) {
					t.Errorf("%s is left behind", l)
				}
			}
			if inbox.Exists(filepath.Join(apr, "consumed", "s1.json")) {
				t.Error("an interrupted gate recorded a consumption")
			}
		})
	}
}

// PR #25 (P1): the gate's lock wait ends at once when its context ends
// (a stopped gate), and otherwise waits the time given.
func TestGateLockWaitEndsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sleep := ctxSleep(ctx)
	start := time.Now()
	sleep(5 * time.Millisecond)
	if time.Since(start) < 5*time.Millisecond {
		t.Error("a running gate's wait ended early")
	}
	cancel()
	start = time.Now()
	sleep(time.Hour)
	if time.Since(start) > time.Second {
		t.Error("a stopped gate's wait did not end at once")
	}
}
