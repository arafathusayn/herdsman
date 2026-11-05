package inbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/inbox/inboxtest"
)

// Review inbox-fifo-2 P1: the boundary checks reread peers.approved after
// Registry validated it. A refused reread (the file replaced by a link)
// read as nil, and a proof whose boundary.pair is null compared equal to
// it, so the boundary held with no approved registry read. A refused
// reread must fail the check closed, in BoundaryOK and Channel.Boundary.
func TestBoundaryRefusedRegistryRereadFailsClosed(t *testing.T) {
	srv := herdrtest.Start(t)
	fx := herdrtest.Serve(srv)
	hc := herdr.New(srv.Socket)
	ctx := context.Background()
	c := inboxtest.New(t)
	c.Enroll("sender-pane", "sender-session", "receiver-pane", "receiver-session")
	agents := []map[string]any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}
	for _, a := range agents {
		fx.SetAgent(a["pane_id"].(string), a)
	}
	c.RelayBoundary(agents...)
	approved := filepath.Join(c.Dir, "peers.approved")
	real := filepath.Join(c.Base, "approved-copy")
	checks := map[string]func() (bool, string){
		"BoundaryOK": func() (bool, string) {
			r, err := inbox.Open(c.Options(true))
			if err != nil {
				t.Fatal(err)
			}
			ok, fault, _ := r.BoundaryOK(ctx, hc)
			return ok, fault
		},
		"Channel.Boundary": func() (bool, string) {
			ch, err := inbox.OpenChannel(c.Options(true), "")
			if err != nil {
				t.Fatal(err)
			}
			ok, fault, _ := ch.Boundary(ctx, hc)
			return ok, fault
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			c.RelayBoundary(agents...)
			if ok, fault := check(); !ok {
				t.Fatalf("the positive control: the boundary of the approved pair fails: %q", fault)
			}
			// The pair's panes listed as courier panes, so with a null pair
			// the live inventory still matches.
			c.Edit(c.Proof, func(m map[string]any) {
				b := m["boundary"].(map[string]any)
				var couriers []any
				for _, p := range b["pair"].(map[string]any) {
					couriers = append(couriers, p.(map[string]any)["identity"])
				}
				b["courier_panes"] = couriers
				b["pair"] = nil
			})
			if ok, fault := check(); ok || fault != inbox.FaultStatic {
				t.Fatalf("a null pair with the registry in place: %v %q", ok, fault)
			}
			// The second strict read of peers.approved (after Registry's)
			// finds a link to the same bytes.
			b, err := os.ReadFile(approved)
			if err != nil {
				t.Fatal(err)
			}
			reads := 0
			inbox.SetBeforeOpen(func(p string) {
				if p != approved {
					return
				}
				reads++
				if reads == 2 {
					c.Write(real, string(b))
					if err := os.Remove(approved); err != nil {
						t.Error(err)
					}
					if err := os.Symlink(real, approved); err != nil {
						t.Error(err)
					}
				}
			})
			ok, fault := check()
			inbox.SetBeforeOpen(nil)
			if err := os.Remove(approved); err != nil {
				t.Fatal(err)
			}
			c.Write(approved, string(b))
			if reads < 2 {
				t.Fatalf("peers.approved was read %d times; the reread was not reached", reads)
			}
			if ok || fault != inbox.FaultStatic {
				t.Errorf("a refused reread of peers.approved with a null pair: boundary %v, fault %q, want FaultStatic", ok, fault)
			}
		})
	}
}
