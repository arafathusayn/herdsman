package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// A waiting exposure record (any pending state the classifier does
	// not call empty) fails closed until an owner of the input lock folds
	// it.
	if (&Channel{Root: r}).ExposurePending() {
		return false, FaultPending, nil
	}
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

// ProofV2Valid checks a daemon-owned inbox's version 2 setup proof
// (design 4.6, 7.3): setup.json of this inbox with version 2, a verified
// policy, at least one rule, and every rule and the binary an absolute
// regular file whose SHA-256 matches. The script and CRC binary forms of
// contract 1 are not version 2 and fail.
func ProofV2Valid(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, "state", "proof-stale")); err == nil {
		return false // the daemon saw this proof stale; only a new setup clears it
	}
	b, ok := ReadStrict(dir, filepath.Join(dir, "setup.json"))
	if !ok {
		return false
	}
	var doc struct {
		Version        int    `json:"version"`
		Inbox          string `json:"inbox"`
		PolicyVerified bool   `json:"policy_verified"`
		Rules          []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"rules"`
		Binary struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"binary"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Version != 2 || doc.Inbox != dir || !doc.PolicyVerified || len(doc.Rules) == 0 {
		return false
	}
	for _, f := range append(doc.Rules, doc.Binary) {
		if !filepath.IsAbs(f.Path) || len(f.SHA256) != 64 {
			return false
		}
		st, err := os.Lstat(f.Path)
		if err != nil || !st.Mode().IsRegular() {
			return false
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return false
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return false
		}
	}
	return true
}
