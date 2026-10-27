package gate

import (
	"encoding/json"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// askedByAdjutant is herdsman approval-gate's asker check (PR 28): the approved
// pair must be valid and name an adjutant, and the trusted interaction's
// asked_by must be that adjutant. An asker that the pair names as the
// orchestrator gets its own line; any other asker, or none, is refused.
func (g *Gate) askedByAdjutant(im map[string]any) *exit {
	refuse := func(msg string) *exit { e := g.refuse(msg); return &e }
	b, ok := inbox.ReadStrict(g.root.Dir, g.root.Path("peers.approved"))
	var peers []inbox.Peer
	if ok {
		peers, ok = inbox.ValidRegistry(b)
	}
	if !ok {
		return refuse("no approved pair to name the adjutant")
	}
	roles := map[string]string{}
	adjutant := ""
	for _, p := range peers {
		roles[p.Name] = p.Role
		if p.Role == "adjutant" {
			adjutant = p.Name
		}
	}
	if adjutant == "" {
		return refuse("no approved adjutant")
	}
	if s, isString := im["asked_by"].(string); isString && s == adjutant {
		return nil
	}
	if roles[askerKey(im["asked_by"])] == "orchestrator" {
		return refuse("the orchestrator's own ask is no go in a route with an adjutant; ask again through the adjutant")
	}
	return refuse("trusted interaction was not asked by the approved adjutant")
}

// askerKey is jq's tostring of asked_by: a string as it is, anything else
// (a missing value is null) as its JSON text.
func askerKey(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
