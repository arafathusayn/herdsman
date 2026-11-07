package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// adoptCrashUnattended crashes an adoption at point and restarts the
// daemon: the start alone finishes it, with no second adopt call (the
// pending entry keeps the confirmed pair), and leaves no adoption state.
func adoptCrashUnattended(t *testing.T, point string) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	h.cfg.Crash = func(p string) {
		if p == point {
			panic(crashSignal{p})
		}
	}
	h.cfg.Log = func(string) {}
	h.start()
	_, _ = h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	h.stop()
	h.cfg.Crash = nil
	h.start()
	if _, e := h.d.route("old"); e != nil {
		t.Fatalf("after %s the start did not finish the adoption: %v", point, e)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == inbox.MutationLock || strings.HasPrefix(e.Name(), ".released.") {
			t.Fatalf("after %s: %s left", point, e.Name())
		}
	}
	if exists(filepath.Join(dir, "state", "adopting.json")) || len(h.d.readPending().Inboxes) != 0 {
		t.Fatalf("after %s: adoption state left", point)
	}
}

func TestAdoptCrashResumesUnattended(t *testing.T) {
	for _, p := range []string{"adopt.pending", "adopt.marker", "adopt.invalidated", "adopt.owner", "adopt.registered", "adopt.committed", "adopt.unlisted"} {
		t.Run(p, func(t *testing.T) { adoptCrashUnattended(t, p) })
	}
}

// TestAdoptStartDoesNotWaitOnDrain: a pending adoption whose inbox a
// paused writer still uses neither delays the start nor waits in it: the
// route is held adopting, health answers, and a later scheduler tick
// finishes the adoption once the writer is gone.
func TestAdoptStartDoesNotWaitOnDrain(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	var busy atomic.Bool
	busy.Store(true)
	h.cfg.Procs = func() ([]Proc, error) {
		if busy.Load() {
			return []Proc{{PID: 999999, Command: "herdsman message hold orch-1 x", Env: []string{"HERDSMAN_INBOX=" + dir}}}, nil
		}
		return nil, nil
	}
	h.cfg.DrainWait = time.Hour
	var slept atomic.Int64
	h.cfg.Sleep = func(d time.Duration) { slept.Add(int64(d)) }
	h.cfg.Log = func(string) {}
	h.start()
	// The human's adopt is refused busy after its drain; keep it short.
	h.d.cfg.DrainWait = time.Millisecond
	if _, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil); code(err) != "adopt_busy" {
		t.Fatalf("got %v", err)
	}
	h.stop()
	h.cfg.DrainWait = time.Hour
	slept.Store(0)
	h.start()
	if slept.Load() != 0 {
		t.Fatalf("the start slept %v in the drain", time.Duration(slept.Load()))
	}
	if _, e := h.d.route("old"); e == nil || e.Code != "route_unowned" || !strings.Contains(e.Message, "adopting") {
		t.Fatalf("not held adopting: %v", e)
	}
	var hl Health
	h.do("GET", "/v1/health", nil, &hl)
	h.d.retryHeld()
	if slept.Load() != 0 {
		t.Fatal("the scheduler slept in the drain")
	}
	busy.Store(false)
	h.d.retryHeld()
	if _, e := h.d.route("old"); e != nil {
		t.Fatalf("not adopted after the writer ended: %v", e)
	}
}

// TestAdoptCredentialFailureStaysPending: credentials that cannot be
// issued keep the adoption pending and the route unserved; once the
// cause is gone the adoption finishes with them.
func TestAdoptCredentialFailureStaysPending(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	creds := filepath.Join(dir, "state", "credentials")
	// After step b moved file mode's credentials aside, just before the
	// credentials are issued.
	h.cfg.Crash = func(p string) {
		if p == "adopt.registered" && !exists(creds) {
			if err := os.Mkdir(creds, 0o500); err != nil {
				t.Error(err)
			}
		}
	}
	h.start()
	t.Cleanup(func() { _ = os.Chmod(creds, 0o700) })
	_, err := h.client().Do(context.Background(), "POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, nil)
	if err == nil || !strings.Contains(err.Error(), "credentials at adoption") {
		t.Fatalf("got %v", err)
	}
	if !exists(filepath.Join(dir, "state", "adopting.json")) || len(h.d.readPending().Inboxes) != 1 {
		t.Fatal("the adoption did not stay pending")
	}
	if _, e := h.d.route("old"); e == nil {
		t.Fatal("served without credentials")
	}
	_ = os.Chmod(creds, 0o700)
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	if !exists(filepath.Join(dir, "state", "credentials", "adj-1")) || exists(filepath.Join(dir, "state", "adopting.json")) {
		t.Fatal("credentials or commit missing")
	}
}

// TestAdoptReleasesLockWithLeftover: an interrupted write inside the
// input lock does not keep it held or make it look foreign.
func TestAdoptReleasesLockWithLeftover(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	h.cfg.Crash = func(p string) {
		if p == "adopt.registered" {
			_ = os.WriteFile(filepath.Join(dir, inbox.MutationLock, ".daemon.123"), []byte("x"), 0o600)
		}
	}
	h.start()
	var res AdoptResult
	h.do("POST", "/v1/routes/old/adopt", AdoptRequest{Inbox: dir, ConfirmPairSHA256: sum}, &res)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == inbox.MutationLock || strings.HasPrefix(e.Name(), ".released.") {
			t.Fatalf("%s left", e.Name())
		}
	}
}

// TestReleaseInputLockLeavesOthers: an input lock of another owner, or
// of a daemon operation (another epoch), is never released.
func TestReleaseInputLockLeavesOthers(t *testing.T) {
	dir := t.TempDir()
	lk := filepath.Join(dir, inbox.MutationLock)
	_ = os.Mkdir(lk, 0o700)
	_ = os.WriteFile(filepath.Join(lk, "pid"), []byte("4242\n"), 0o600)
	if err := releaseInputLock(dir); err != nil || !exists(filepath.Join(lk, "pid")) {
		t.Fatalf("a foreign lock was touched: %v", err)
	}
	_ = os.WriteFile(filepath.Join(lk, "owner.json"), []byte(`{"owner":"daemon","epoch":"op"}`), 0o600)
	if err := releaseInputLock(dir); err != nil || !exists(lk) {
		t.Fatalf("an operation's lock was released: %v", err)
	}
	_ = os.WriteFile(filepath.Join(lk, "owner.json"), []byte(`{"owner":"daemon","epoch":"adopt-e1"}`), 0o600)
	if err := releaseInputLock(dir); err != nil || exists(lk) {
		t.Fatalf("own lock: %v", err)
	}
}

// TestReleaseInputLockErrors: a release that cannot rename the lock away
// (a read-only inbox) or cannot remove the renamed folder reports it.
func TestReleaseInputLockErrors(t *testing.T) {
	dir := t.TempDir()
	lk := filepath.Join(dir, inbox.MutationLock)
	_ = os.Mkdir(lk, 0o700)
	_ = os.WriteFile(filepath.Join(lk, "owner.json"), []byte(`{"owner":"daemon","epoch":"adopt-e1"}`), 0o600)
	_ = os.Chmod(dir, 0o500)
	err := releaseInputLock(dir)
	_ = os.Chmod(dir, 0o700)
	if err == nil || !strings.Contains(err.Error(), "releasing the input lock") || !exists(lk) {
		t.Fatalf("read-only inbox: %v", err)
	}
	stuck := filepath.Join(lk, "stuck")
	_ = os.Mkdir(stuck, 0o700)
	_ = os.WriteFile(filepath.Join(stuck, "f"), nil, 0o600)
	_ = os.Chmod(stuck, 0o500)
	t.Cleanup(func() {
		ents, _ := filepath.Glob(filepath.Join(dir, ".released.*", "stuck"))
		for _, e := range ents {
			_ = os.Chmod(e, 0o700)
		}
	})
	if err := releaseInputLock(dir); err == nil || !strings.Contains(err.Error(), "removing the released input lock") || exists(lk) {
		t.Fatalf("unremovable leftover: %v", err)
	}
}
