package inbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// DaemonOwned reports whether an inbox is owned by the herdsman daemon:
// state/owner.json names the daemon (design contract 2). A file-mode
// writer refuses such an inbox.
func DaemonOwned(dir string) bool {
	b, ok := ReadStrict(dir, filepath.Join(dir, "state", "owner.json"))
	if !ok {
		return false
	}
	v, _ := Decode(b)
	return Object(v)["owner"] == "daemon"
}

// DaemonOwnedLine is the refusal of a file-mode writer on a daemon-owned
// inbox.
const DaemonOwnedLine = "inbox owned by the herdsman daemon; use herdsman message … with HERDSMAN_ROUTE"

// BoundaryV2 is the boundary check of a daemon-owned inbox over the
// version 2 setup proof's boundary object (its fields are the version 1
// boundary's, unchanged): the approved registry, no exposure record, every
// setup field, and every live agent of the server known to the setup. The
// caller has checked the proof's checksums. It returns the fault and, for
// FaultInventory, the unknown agents.
func BoundaryV2(ctx context.Context, c *herdr.Client, dir string, boundary map[string]any) (bool, string, []Unknown) {
	r := &Root{Dir: dir}
	if _, ok := r.Registry(); !ok || Exists(r.Path("state", "boundary-tainted")) {
		return false, FaultStatic, nil
	}
	pb, _ := os.ReadFile(r.Path("peers.approved"))
	pair, _ := Decode(pb)
	if !boundaryFields(boundary, pair) {
		return false, FaultStatic, nil
	}
	agents, err := c.AgentList(ctx)
	if err != nil || len(agents) == 0 {
		return false, FaultList, nil
	}
	var unknown []Unknown
	for _, a := range agents {
		var sess any
		if len(a.AgentSession) > 0 {
			sess, _ = Decode(a.AgentSession)
		}
		var harness any
		if a.Agent.Agent != nil {
			harness = *a.Agent.Agent
		}
		if knownAgent(boundary, map[string]any{"pane": a.PaneID, "session": sess}, harness) {
			continue
		}
		raw := a.AgentSession
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		unknown = append(unknown, Unknown{Pane: a.PaneID, Agent: harness, Session: raw})
	}
	if len(unknown) > 0 {
		return false, FaultInventory, unknown
	}
	return true, "", nil
}
