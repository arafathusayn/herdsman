package inbox

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// TestProofV2RefusesSwappedFiles: the gate checks the version 2 proof with
// the input lock held, so a proof file swapped after its checks must be
// refused at once: a FIFO in place of a rule or the binary just before it
// is hashed, a link to an identical copy there, and a FIFO in place of
// setup.json once ProofV2Valid has read it (BoundaryOK keeps the bytes it
// checked).
func TestProofV2RefusesSwappedFiles(t *testing.T) {
	fifo := func(t *testing.T, p string) {
		t.Helper()
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		// Release a reader that blocked on it, so a failing run ends.
		t.Cleanup(func() {
			if f, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
	}
	link := func(t *testing.T, p string) {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cp := p + ".copy"
		if err := os.WriteFile(cp, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(cp, p); err != nil {
			t.Fatal(err)
		}
	}
	// within runs f and reports whether it returned within two seconds.
	within := func(f func()) bool {
		done := make(chan struct{})
		go func() { f(); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
	for _, c := range []struct {
		name string
		swap func(t *testing.T, x v2Inbox) string
		kind func(t *testing.T, p string)
	}{
		{"fifo rule", func(_ *testing.T, x v2Inbox) string { return x.rule }, fifo},
		{"fifo binary", func(_ *testing.T, x v2Inbox) string { return x.exe }, fifo},
		{"linked rule", func(_ *testing.T, x v2Inbox) string { return x.rule }, link},
		{"linked binary", func(_ *testing.T, x v2Inbox) string { return x.exe }, link},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newV2Inbox(t)
			target := c.swap(t, x)
			beforeHash = func(p string) {
				if p == target {
					c.kind(t, p)
				}
			}
			t.Cleanup(func() { beforeHash = nil })
			var ok bool
			if !within(func() { ok = ProofV2Valid(x.dir, x.exe) }) {
				t.Fatalf("ProofV2Valid blocked on a %s", c.name)
			}
			if ok {
				t.Fatalf("ProofV2Valid accepted a %s", c.name)
			}
		})
	}
	t.Run("fifo setup.json after the proof check", func(t *testing.T) {
		x := newV2Inbox(t)
		setup := filepath.Join(x.dir, "setup.json")
		swapped := false
		beforeOpen = func(p string) {
			if p == filepath.Join(x.dir, "watch-targets.json") && !swapped {
				swapped = true
				fifo(t, setup)
			}
		}
		t.Cleanup(func() { beforeOpen = nil })
		hc := herdr.New(filepath.Join(x.base, "no-herdr.sock"))
		var ok bool
		var fault string
		if !within(func() { ok, fault, _ = (&Root{Dir: x.dir, Executable: x.exe}).BoundaryOK(context.Background(), hc) }) {
			t.Fatal("BoundaryOK blocked on a FIFO setup.json")
		}
		if !swapped || ok || fault != FaultList {
			t.Fatalf("boundary after the swap: swapped %v, %v %q, want the agent list fault of the checked proof", swapped, ok, fault)
		}
	})
}
