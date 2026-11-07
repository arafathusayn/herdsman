package daemon

import (
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// TestProofV2NamesTheRunningBinary: a version 2 proof is valid only for
// the executable it names. After an upgrade to another path that leaves
// the old binary in place, the old proof does not prove the new binary.
func TestProofV2NamesTheRunningBinary(t *testing.T) {
	x, _, _ := proofRoute(t)
	proved, err := filepath.EvalSymlinks(x.d.cfg.Executable)
	if err != nil {
		t.Fatal(err)
	}
	other := writeFile(t, t.TempDir(), "herdsman", "another build\n")
	for _, c := range []struct {
		exe  string
		want bool
	}{{proved, true}, {other, false}} {
		ch, err := inbox.OpenChannel(inbox.Options{Dir: x.dir, ReadOnly: true}, c.exe)
		if err != nil {
			t.Fatal(err)
		}
		if got := ch.ProofValid(); got != c.want {
			t.Errorf("channel of %s: proof valid %v, want %v", c.exe, got, c.want)
		}
		r, err := inbox.Open(inbox.Options{Dir: x.dir, ReadOnly: true, Executable: c.exe})
		if err != nil {
			t.Fatal(err)
		}
		if r.ProofStale == c.want {
			t.Errorf("root of %s: proof stale %v, want %v", c.exe, r.ProofStale, !c.want)
		}
	}
}
