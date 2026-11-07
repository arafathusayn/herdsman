package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestConfirmedPairFIFODoesNotBlock: peers.approved replaced by a FIFO
// after the registry check must refuse the pair at once, not block the
// adoption that holds regMu and the inbox locks.
func TestConfirmedPairFIFODoesNotBlock(t *testing.T) {
	h := newHarness(t)
	_, _, dir := h.route("r")
	h.stop()
	p := filepath.Join(dir, "peers.approved")
	ab, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if e := confirmedPair(dir, sha256Hex(ab)); e != nil {
		t.Fatalf("the plain pair is refused: %v", e)
	}
	pairChecked = func(dir string) {
		_ = os.Remove(p)
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { pairChecked = nil })
	done := make(chan *APIError, 1)
	go func() { done <- confirmedPair(dir, sha256Hex(ab)) }()
	select {
	case e := <-done:
		if e == nil || e.Code != "checksum_mismatch" {
			t.Fatalf("a FIFO pair is accepted: %v", e)
		}
	case <-time.After(3 * time.Second):
		// Free the blocked open so the test binary can exit.
		if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		<-done
		t.Fatal("confirmedPair blocked on a FIFO peers.approved")
	}
}
