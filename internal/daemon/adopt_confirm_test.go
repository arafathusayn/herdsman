package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Every run of an adoption, a resume included, checks the pair the human
// confirmed: the registry is an approved pair with no pending change and
// sha256(peers.approved) is the daemon's record of the confirmed pair.
// Otherwise the adoption stays pending with checksum_mismatch, and no
// credential is issued for a pair the human never confirmed.

// swapPair renames adj-1 to adj-new in the inbox's registry files, as an
// inbox writer can while an adoption is pending.
func swapPair(t *testing.T, dir, sum string, files ...string) []byte {
	t.Helper()
	pb, err := os.ReadFile(filepath.Join(dir, "peers"))
	if err != nil {
		t.Fatal(err)
	}
	swapped := bytes.ReplaceAll(pb, []byte("adj-1"), []byte("adj-new"))
	if bytes.Equal(pb, swapped) || sha256Hex(swapped) == sum {
		t.Fatal("bad swap fixture")
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), swapped, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return pb
}

// stillPending checks a resume refused for an unconfirmed pair: the route
// is not served, the new party has no credential, and the marker and the
// daemon's record of the adoption stay.
func stillPending(t *testing.T, h *harness, dir string) {
	t.Helper()
	if _, e := h.d.route("old"); e == nil {
		t.Error("the resume adopted a pair the human never confirmed")
	}
	for _, n := range []string{"adj-new", "adj-1", "orch-1"} {
		if exists(filepath.Join(dir, "state", "credentials", n)) {
			t.Errorf("the resume issued a credential for %s", n)
		}
	}
	if !exists(filepath.Join(dir, "state", "adopting.json")) {
		t.Error("the marker went")
	}
	if pl := h.d.readPending(); len(pl.Inboxes) != 1 || pl.Inboxes[0].Inbox != dir {
		t.Errorf("the pending adoption went: %+v", pl.Inboxes)
	}
}

// busyAdoption starts an adoption of old that stops at the drain, with a
// file-mode process in the inbox while busy is set.
func busyAdoption(t *testing.T, h *harness) (dir, sum string, busy *atomic.Bool) {
	t.Helper()
	h.stop()
	dir, sum = fileInbox(t, h)
	busy = &atomic.Bool{}
	busy.Store(true)
	h.cfg.Procs = func() ([]Proc, error) {
		if busy.Load() {
			return []Proc{{PID: 999999, Command: "herdsman message hold orch-1 x", Env: []string{"HERDSMAN_INBOX=" + dir}}}, nil
		}
		return nil, nil
	}
	h.cfg.Log = func(string) {}
	h.start()
	h.d.cfg.DrainWait = time.Millisecond
	if err := adoptErr(h, "old", dir, sum); code(err) != "adopt_busy" {
		t.Fatalf("want adopt_busy: %v", err)
	}
	return dir, sum, busy
}

// TestResumeChecksPairAfterBusy: the pair is swapped while the adoption
// waits on a busy inbox; the human's re-run with the same confirm is
// refused until the confirmed pair is back, then the adoption completes.
func TestResumeChecksPairAfterBusy(t *testing.T) {
	for _, files := range [][]string{{"peers"}, {"peers", "peers.approved"}} {
		t.Run(files[len(files)-1], func(t *testing.T) {
			h := newHarness(t)
			dir, sum, busy := busyAdoption(t, h)
			orig := swapPair(t, dir, sum, files...)
			busy.Store(false)
			if err := adoptErr(h, "old", dir, sum); code(err) != "checksum_mismatch" {
				t.Errorf("resume after the swap: %v", err)
			}
			stillPending(t, h, dir)
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(dir, f), orig, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := adoptErr(h, "old", dir, sum); err != nil {
				t.Fatalf("resume with the confirmed pair back: %v", err)
			}
			for _, n := range []string{"adj-1", "orch-1"} {
				if !exists(filepath.Join(dir, "state", "credentials", n)) {
					t.Errorf("no credential for %s", n)
				}
			}
		})
	}
}

// TestResumeChecksPairAtStart: the reviewers' changed pair (only peers,
// or both registries) after a busy adoption, and a swap after a crash;
// the start's unattended resume refuses both.
func TestResumeChecksPairAtStart(t *testing.T) {
	for _, files := range [][]string{{"peers"}, {"peers", "peers.approved"}} {
		t.Run("busy "+files[len(files)-1], func(t *testing.T) {
			h := newHarness(t)
			dir, sum, busy := busyAdoption(t, h)
			h.stop()
			swapPair(t, dir, sum, files...)
			busy.Store(false)
			h.start()
			stillPending(t, h, dir)
		})
		t.Run("crash "+files[len(files)-1], func(t *testing.T) {
			h := newHarness(t)
			h.stop()
			dir, sum := fileInbox(t, h)
			h.cfg.Crash = func(p string) {
				if p == "adopt.marker" {
					panic(crashSignal{p})
				}
			}
			h.cfg.Log = func(string) {}
			h.start()
			_ = adoptErr(h, "old", dir, sum)
			h.stop()
			swapPair(t, dir, sum, files...)
			h.cfg.Crash = nil
			h.start()
			stillPending(t, h, dir)
		})
	}
}
