package inbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestProofV2NeedsAnExecutable: no executable, or a relative one, accepts
// no version 2 proof; on a daemon-owned inbox BoundaryOK checks the proof
// itself, so a root whose Open saw a valid proof is refused once the proof
// is gone.
func TestProofV2NeedsAnExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{"", "herdsman"} {
		if ProofV2Valid(dir, exe) {
			t.Errorf("executable %q accepts a proof", exe)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(`{"owner":"daemon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Root{Dir: dir, Executable: "/usr/bin/herdsman"} // ProofStale false, as Open left it
	if ok, fault, _ := r.BoundaryOK(context.Background(), nil); ok || fault != FaultStatic {
		t.Fatalf("no proof on the boundary check: %v %q", ok, fault)
	}
}
