package inbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// TestProofV2BoundaryEdges: each part of the version 2 proof and of the
// boundary BoundaryOK reads from it refuses on its own, from the valid
// fixture (which fails only on the agent list).
func TestProofV2BoundaryEdges(t *testing.T) {
	setup := func(x v2Inbox) string { return filepath.Join(x.dir, "setup.json") }
	edit := func(t *testing.T, x v2Inbox, fn func(m map[string]any)) {
		t.Helper()
		b, err := os.ReadFile(setup(x))
		if err != nil {
			t.Fatal(err)
		}
		v, _ := Decode(b)
		m := Object(v)
		fn(m)
		x.writeJSON(t, setup(x), m)
	}
	for _, c := range []struct {
		name   string
		change func(t *testing.T, x v2Inbox, r *Root)
		fault  string
	}{
		{"valid", func(*testing.T, v2Inbox, *Root) {}, FaultList},
		{"stale at open", func(_ *testing.T, _ v2Inbox, r *Root) { r.ProofStale = true }, FaultStatic},
		{"stale latch", func(t *testing.T, x v2Inbox, _ *Root) {
			x.write(t, filepath.Join(x.dir, "state", "proof-stale"), []byte("changed\n"))
		}, FaultStatic},
		{"no setup.json", func(t *testing.T, x v2Inbox, _ *Root) {
			if err := os.Remove(setup(x)); err != nil {
				t.Fatal(err)
			}
		}, FaultStatic},
		{"short checksum", func(t *testing.T, x v2Inbox, _ *Root) {
			edit(t, x, func(m map[string]any) { Object(m["binary"])["sha256"] = "00" })
		}, FaultStatic},
		{"changed binary", func(t *testing.T, x v2Inbox, _ *Root) { x.write(t, x.exe, []byte("changed\n")) }, FaultStatic},
		{"target file without a proof target", func(t *testing.T, x v2Inbox, _ *Root) {
			x.writeJSON(t, filepath.Join(x.dir, "resume-targets.json"), []any{})
		}, FaultStatic},
		{"changed target file", func(t *testing.T, x v2Inbox, _ *Root) {
			x.writeJSON(t, filepath.Join(x.dir, "watch-targets.json"), []any{"pane"})
		}, FaultStatic},
		{"pending exposure", func(t *testing.T, x v2Inbox, _ *Root) {
			pending := filepath.Join(x.dir, "state", "exposure-pending")
			if err := os.MkdirAll(pending, 0o700); err != nil {
				t.Fatal(err)
			}
			x.write(t, filepath.Join(pending, "1.json"), []byte(`{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n"))
		}, FaultPending},
		{"registry awaiting approval", func(t *testing.T, x v2Inbox, _ *Root) {
			x.writeJSON(t, filepath.Join(x.dir, "peers"), map[string]any{})
		}, FaultStatic},
		{"boundary of another pair", func(t *testing.T, x v2Inbox, _ *Root) {
			edit(t, x, func(m map[string]any) { Object(m["boundary"])["pair"] = map[string]any{} })
		}, FaultStatic},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newV2Inbox(t)
			r := &Root{Dir: x.dir, Executable: x.exe}
			c.change(t, x, r)
			ok, fault, _ := r.BoundaryOK(context.Background(), herdr.New(filepath.Join(x.base, "no-herdr.sock")))
			if ok || fault != c.fault {
				t.Fatalf("%v %q, want %q", ok, fault, c.fault)
			}
		})
	}
}
