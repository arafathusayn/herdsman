package inbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// TestProofV2NeedsAnExecutable: a version 2 proof with an approved pair
// proves the executable it names and no other. ProofV2Valid accepts it
// for that executable and refuses another (an identical copy too), a
// relative path and none. On a daemon-owned inbox BoundaryOK checks the
// proof itself: a root of the named executable passes the proof and the
// boundary fields and fails only on the agent list (no Herdr here), and a
// root of another executable, whose Open saw no stale proof, is refused
// at the proof.
func TestProofV2NeedsAnExecutable(t *testing.T) {
	x := newV2Inbox(t)
	other := filepath.Join(x.base, "other")
	b, err := os.ReadFile(x.exe)
	if err != nil {
		t.Fatal(err)
	}
	x.write(t, other, b)
	if !ProofV2Valid(x.dir, x.exe) {
		t.Fatal("the proof does not prove the executable it names")
	}
	for _, exe := range []string{other, "", "herdsman"} {
		if ProofV2Valid(x.dir, exe) {
			t.Errorf("executable %q accepts a proof of %s", exe, x.exe)
		}
	}
	hc := herdr.New(filepath.Join(x.base, "no-herdr.sock"))
	if ok, fault, _ := (&Root{Dir: x.dir, Executable: x.exe}).BoundaryOK(context.Background(), hc); ok || fault != FaultList {
		t.Fatalf("the named executable's boundary check: %v %q, want the agent list fault", ok, fault)
	}
	if ok, fault, _ := (&Root{Dir: x.dir, Executable: other}).BoundaryOK(context.Background(), hc); ok || fault != FaultStatic {
		t.Fatalf("another executable's boundary check: %v %q, want the proof fault", ok, fault)
	}
}
