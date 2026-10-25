package gate

import (
	"context"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"os"
	"path/filepath"
	"testing"
)

// e93bcc1 XB1d (B3-001): an exposure that waited for the input lock is
// published by the gate as the lock's next owner, and the gate refuses
// under the HUMAN boundary; nothing is consumed and nothing stays pending.
func TestGatePublishesAWaitingExposure(t *testing.T) {
	r := newRig(t)
	r.gojson("xb1", 600, nil)
	pending := filepath.Join(r.c.Dir, "state", "exposure-pending")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	r.c.Write(filepath.Join(pending, "1-1-1.json"), `{"agents":[{"pane":"rogue-pane","agent":"claude","session":{"agent":"claude","kind":"id","source":"hook","value":"rogue-session"}}],"time":1}`+"\n")
	out := r.consume("xb1")
	match(t, "XB1d the gate refuses under the HUMAN boundary", `^HUMAN .*rc=3$`, out)
	b, _ := os.ReadFile(filepath.Join(r.c.Dir, "state", "boundary-tainted"))
	match(t, "XB1e the record names the agent", `rogue-pane`, string(b))
	if ents, _ := os.ReadDir(pending); len(ents) != 0 {
		t.Errorf("XB1e %d entries left pending", len(ents))
	}
	if exists(r.consumed("xb1")) {
		t.Error("XB1f a go was consumed")
	}
}

// A waiting exposure alone fails the gate's authority closed.
func TestGateAuthorityFailsClosedOnPendingExposure(t *testing.T) {
	r := newRig(t)
	pending := filepath.Join(r.c.Dir, "state", "exposure-pending")
	os.MkdirAll(pending, 0o700)
	r.c.Write(filepath.Join(pending, "x.json"), "{}\n")
	g := r.gateValue()
	if line, code := g.authority(); code != Human {
		t.Errorf("pending exposure: %d %q", code, line)
	}
}

func (r *rig) gateValue() *Gate {
	o := r.c.Options(false)
	o.Now = r.clock
	return &Gate{Options: o, Client: r.hc, ctx: context.Background()}
}

// Landing review P0 (e93bcc1 B3-001): an unknown agent seen only by the
// gate's first authority check is recorded from that same observation, so
// a retry after HUMAN/exit 3 stays held instead of consuming on a later
// clean inventory.
func TestGateRecordsTheObservationItRefusedOn(t *testing.T) {
	r := newRig(t)
	r.gojson("xb4", 600, nil)
	lists := 0
	r.srv.Handle("agent.list", func(map[string]any) (any, string) {
		lists++
		agents := []any{herdrtest.Agent("sender-pane", "sender-session"), herdrtest.Agent("receiver-pane", "receiver-session")}
		if lists == 1 {
			agents = append(agents, herdrtest.Agent("brief-pane", "brief-session"))
		}
		return map[string]any{"type": "agent_list", "agents": agents}, ""
	})
	match(t, "the first consume is refused", `^HUMAN .*rc=3$`, r.consume("xb4"))
	b, _ := os.ReadFile(filepath.Join(r.c.Dir, "state", "boundary-tainted"))
	match(t, "the agent it saw is recorded", `brief-pane`, string(b))
	match(t, "a retry stays held", `^HUMAN .*rc=3$`, r.consume("xb4"))
	if exists(r.consumed("xb4")) {
		t.Error("a retry consumed without the human setup")
	}
}
