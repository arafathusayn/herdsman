package inbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	return BoundaryV2Inventory(ctx, TUIInventory{Client: c}, dir, boundary)
}

// Inventory finds the live agents a boundary does not know. The Herdr TUI's
// is its agent list; a herdr-core route's is the shared-core inventory of the
// runtime spec's section 6, read under the inventory rule in force.
type Inventory interface {
	Unknown(ctx context.Context, boundary map[string]any) ([]Unknown, error)
}

// TUIInventory is the Herdr TUI's agent list: every live agent of the server
// must be known to the setup.
type TUIInventory struct{ Client *herdr.Client }

// Unknown implements Inventory. An empty list is a failure: a server always
// holds the pair.
func (t TUIInventory) Unknown(ctx context.Context, boundary map[string]any) ([]Unknown, error) {
	agents, err := t.Client.AgentList(ctx)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, errors.New("the server lists no agent")
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
	return unknown, nil
}

// BoundaryV2Inventory is BoundaryV2 over any inventory.
func BoundaryV2Inventory(ctx context.Context, inv Inventory, dir string, boundary map[string]any) (bool, string, []Unknown) {
	r := &Root{Dir: dir}
	// A waiting exposure record (any pending state the classifier does
	// not call empty) fails closed until an owner of the input lock folds
	// it.
	if (&Channel{Root: r}).ExposurePending() {
		return false, FaultPending, nil
	}
	// The pair is the approved registry as Registry read it (bounded, no
	// link, no FIFO), not a second read of the path.
	pb, _, ok := r.registry()
	if !ok || Exists(r.Path("state", "boundary-tainted")) {
		return false, FaultStatic, nil
	}
	pair, _ := Decode(pb)
	if !boundaryFields(boundary, pair) {
		return false, FaultStatic, nil
	}
	unknown, err := inv.Unknown(ctx, boundary)
	if err != nil {
		return false, FaultList, nil
	}
	if len(unknown) > 0 {
		return false, FaultInventory, unknown
	}
	return true, "", nil
}

// ProofV2Valid checks a daemon-owned inbox's version 2 setup proof
// (design 4.6, 7.3) for the executable exe (absolute, links resolved; ""
// accepts no proof): setup.json of this inbox with version 2, a verified
// policy, at least one rule, the binary named exe, and every rule and the
// binary an absolute regular file whose SHA-256 matches. A proof of
// another binary does not prove exe, even when that binary is still in
// place after an upgrade. The script and CRC binary forms of contract 1
// are not version 2 and fail.
func ProofV2Valid(dir, exe string) bool {
	_, ok := proofV2(dir, exe)
	return ok
}

// proofV2 is ProofV2Valid that also returns the setup.json bytes it
// checked, so a caller reads the boundary from the proof it verified and
// not from a second read of the path.
func proofV2(dir, exe string) ([]byte, bool) {
	if exe == "" || !filepath.IsAbs(exe) {
		return nil, false
	}
	if _, err := os.Lstat(filepath.Join(dir, "state", "proof-stale")); err == nil {
		return nil, false // the daemon saw this proof stale; only a new setup clears it
	}
	b, ok := ReadStrict(dir, filepath.Join(dir, "setup.json"))
	if !ok {
		return nil, false
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
		WatchTargets  json.RawMessage `json:"watch_targets"`
		ResumeTargets json.RawMessage `json:"resume_targets"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Version != 2 || doc.Inbox != dir || !doc.PolicyVerified || len(doc.Rules) == 0 || doc.Binary.Path != exe {
		return nil, false
	}
	for _, f := range append(doc.Rules, doc.Binary) {
		if !filepath.IsAbs(f.Path) || len(f.SHA256) != 64 {
			return nil, false
		}
		if beforeHash != nil {
			beforeHash(f.Path)
		}
		if sum, err := hashRegular(f.Path); err != nil || sum != f.SHA256 {
			return nil, false
		}
	}
	// The target files are part of the proof (7.3): each equals the
	// proof's targets, or is absent when the proof has none.
	for name, raw := range map[string]json.RawMessage{"watch-targets.json": doc.WatchTargets, "resume-targets.json": doc.ResumeTargets} {
		p := filepath.Join(dir, name)
		if len(raw) == 0 || string(raw) == "null" {
			if _, err := os.Lstat(p); err == nil {
				return nil, false
			}
			continue
		}
		t, ok := ReadStrict(dir, p)
		var x, y bytes.Buffer
		if !ok || json.Compact(&x, t) != nil || json.Compact(&y, raw) != nil || !bytes.Equal(x.Bytes(), y.Bytes()) {
			return nil, false
		}
	}
	return b, true
}

// hashRegular is the SHA-256 of a regular file, streamed: it is opened once
// without following a link and without blocking on a FIFO, and its type is
// checked on the open descriptor, so a file swapped after an earlier check
// is refused at once. The read has no size bound: the binary is larger
// than MaxSize.
func hashRegular(p string) (string, error) {
	f, err := openRegular(p, false)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// beforeHash runs in ProofV2Valid just before a proof file is hashed;
// tests swap the file there.
var beforeHash func(p string)
