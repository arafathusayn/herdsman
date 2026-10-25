package inbox_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

func exposureChannel(t *testing.T) (*inboxtest.Case, *inbox.Channel) {
	c := inboxtest.New(t)
	c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	c.Prove()
	ch, err := inbox.OpenChannel(c.Options(false), "")
	if err != nil {
		t.Fatal(err)
	}
	return c, ch
}

// Boundary as the input lock's owner publishes a waiting exposure itself
// and fails this check closed; as a non-owner it reports FaultPending.
func TestBoundaryFoldsAsLockOwner(t *testing.T) {
	c, ch := exposureChannel(t)
	pending := filepath.Join(c.Dir, "state", "exposure-pending")
	os.MkdirAll(pending, 0o700)
	c.Write(filepath.Join(pending, "1.json"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":null}],"time":1}`+"\n")
	if ok, fault, _ := ch.Boundary(context.Background(), herdr.New("")); ok || fault != inbox.FaultPending {
		t.Fatalf("non-owner: %v %q", ok, fault)
	}
	ch.MutationHeld = true
	if ok, fault, _ := ch.Boundary(context.Background(), herdr.New("")); ok || fault != inbox.FaultStatic {
		t.Fatalf("owner: %v %q", ok, fault)
	}
	b, _ := os.ReadFile(filepath.Join(c.Dir, "state", "boundary-tainted"))
	if !strings.Contains(string(b), "rogue-pane") || ch.ExposurePending() {
		t.Errorf("owner fold: %q pending=%v", b, ch.ExposurePending())
	}
}

// A pending path that is a link is refused, never followed or published.
func TestFoldRefusesAnExposureLink(t *testing.T) {
	c, ch := exposureChannel(t)
	outside := filepath.Join(c.Base, "outside")
	os.MkdirAll(outside, 0o700)
	c.Write(filepath.Join(outside, "x.json"), `{"agents":[],"time":1}`+"\n")
	if err := os.Symlink(outside, filepath.Join(c.Dir, "state", "exposure-pending")); err != nil {
		t.Fatal(err)
	}
	if !ch.ExposurePending() {
		t.Fatal("a link is pending exposure")
	}
	if err := ch.FoldExposure(); err == nil {
		t.Fatal("a link was folded")
	}
	if inbox.Exists(filepath.Join(c.Dir, "state", "boundary-tainted")) || !inbox.Exists(filepath.Join(outside, "x.json")) {
		t.Error("the link was followed")
	}
}
