package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResumeRefusalNamesPending: a re-run of a genuine pending adoption
// with another route or another confirm is refused with the pending
// route and its confirmed pair, and never with advice to remove the
// marker, which would throw the daemon's own resume away.
func TestResumeRefusalNamesPending(t *testing.T) {
	h := newHarness(t)
	dir, sum, busy := busyAdoption(t, h)
	busy.Store(false)
	other := strings.Repeat("0", 64)
	for _, c := range []struct{ name, route, confirm string }{
		{"other route", "neu", sum},
		{"other confirm", "old", other},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := adoptErr(h, c.route, dir, c.confirm)
			if err == nil {
				t.Fatal("accepted")
			}
			msg := err.Error()
			if strings.Contains(msg, "remove it") || !strings.Contains(msg, "route old") || !strings.Contains(msg, sum) {
				t.Errorf("refusal does not name the pending adoption: %v", err)
			}
			stillPending(t, h, dir)
		})
	}
	if err := adoptErr(h, "old", dir, sum); err != nil {
		t.Fatalf("re-run with the pending route and pair: %v", err)
	}
}

// TestPlantedCredentialsMovedAside: file mode's credential records go to
// state/legacy with its approval record, so the adoption always issues
// its own generation, whatever an inbox writer planted before it.
func TestPlantedCredentialsMovedAside(t *testing.T) {
	h := newHarness(t)
	dir, sum := fileInbox(t, h)
	planted := []byte(`{"adj-1":{"option":"file","sha256":"` + sha256Hex([]byte("S")) + `"}}` + "\n")
	if err := mkdirs(dir, "state/credentials"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "credentials.json"), planted, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "credentials", "adj-1"), []byte("S"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptErr(h, "old", dir, sum); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "state", "legacy", "credentials-v1.json")); err != nil || !bytes.Equal(b, planted) {
		t.Errorf("credentials.json not moved aside: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "state", "legacy", "credentials-v1", "adj-1")); err != nil || string(b) != "S" {
		t.Errorf("state/credentials/ not moved aside: %q %v", b, err)
	}
	for _, n := range []string{"adj-1", "orch-1"} {
		b, err := os.ReadFile(filepath.Join(dir, "state", "credentials", n))
		if err != nil || string(b) == "S" {
			t.Errorf("%s has no daemon-issued credential: %q %v", n, b, err)
		}
	}
}

// TestSwapBeforeCredentials: a pair swap after the pair check and before
// the credentials are issued answers checksum_mismatch naming the
// recorded confirm, and the adoption stays pending until the pair is back.
func TestSwapBeforeCredentials(t *testing.T) {
	h := newHarness(t)
	h.stop()
	dir, sum := fileInbox(t, h)
	var orig []byte
	h.cfg.Crash = func(p string) {
		if p == "adopt.registered" && orig == nil {
			orig = swapPair(t, dir, sum, "peers", "peers.approved")
		}
	}
	h.start()
	err := adoptErr(h, "old", dir, sum)
	if code(err) != "checksum_mismatch" || !strings.Contains(err.Error(), sum) || !strings.Contains(err.Error(), "stays pending") {
		t.Fatalf("want checksum_mismatch naming %s: %v", sum, err)
	}
	stillPending(t, h, dir)
	for _, f := range []string{"peers", "peers.approved"} {
		if err := os.WriteFile(filepath.Join(dir, f), orig, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := adoptErr(h, "old", dir, sum); err != nil {
		t.Fatalf("resume with the confirmed pair back: %v", err)
	}
}
