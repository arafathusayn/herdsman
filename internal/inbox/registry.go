package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// Peer is one entry of the registry: a name, its role and its pane.
type Peer struct {
	Name     string
	Role     string
	Pane     string
	Identity Identity
}

// ValidRegistry checks registry bytes as valid_registry does: strict JSON,
// an object of at most two entries, each a usable name with exactly a role
// (adjutant or orchestrator) and an identity, and no role, pane or session
// used twice. One bad entry voids it. Peers come sorted by name.
func ValidRegistry(b []byte) ([]Peer, bool) {
	var reg map[string]json.RawMessage
	if StrictJSON(b) != nil || json.Unmarshal(b, &reg) != nil || reg == nil || len(reg) > 2 {
		return nil, false
	}
	out := []Peer{}
	roles, panes, sessions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for name, raw := range reg {
		var p struct {
			Role     string          `json:"role"`
			Identity json.RawMessage `json:"identity"`
		}
		if !NameOK(name) || !KeysExactly(raw, "identity", "role") ||
			json.Unmarshal(raw, &p) != nil || (p.Role != "adjutant" && p.Role != "orchestrator") || !IdentityOK(p.Identity) {
			return nil, false
		}
		id, _ := ParseIdentity(p.Identity)
		s := id.Session
		key := fmt.Sprintf("%q", []string{s.Agent, s.Kind, s.Source, s.Value})
		if roles[p.Role] || panes[id.Pane] || sessions[key] {
			return nil, false
		}
		roles[p.Role], panes[id.Pane], sessions[key] = true, true, true
		out = append(out, Peer{Name: name, Role: p.Role, Pane: id.Pane, Identity: id})
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	return out, true
}

// readRegistry reads one registry file the way strict_json accepts it.
func (r *Root) readRegistry(name string) ([]byte, []Peer, bool) {
	b, ok := ReadStrict(r.Dir, r.Path(name))
	if !ok {
		return nil, nil, false
	}
	peers, ok := ValidRegistry(b)
	return b, peers, ok
}

// Registry is the scripts' registry: peers and peers.approved both valid
// and byte for byte the same (no change awaits the human's approval). It
// returns the approved peers.
func (r *Root) Registry() ([]Peer, bool) {
	pb, _, ok := r.readRegistry("peers")
	if !ok {
		return nil, false
	}
	ab, peers, ok := r.readRegistry("peers.approved")
	if !ok || !bytes.Equal(pb, ab) {
		return nil, false
	}
	return peers, true
}

// Peer is the scripts' peer: the approved entry of a name.
func (r *Root) Peer(name string) (Peer, bool) {
	_, peers, ok := r.readRegistry("peers.approved")
	if !ok {
		return Peer{}, false
	}
	for _, p := range peers {
		if p.Name == name {
			return p, true
		}
	}
	return Peer{}, false
}

// Session is an agent session as herdr reports it.
type Session struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// Identity is a pane and the agent session in it.
type Identity struct {
	Pane    string  `json:"pane"`
	Session Session `json:"session"`
}

// ParseIdentity reads an identity object.
func ParseIdentity(raw json.RawMessage) (Identity, error) {
	var id Identity
	err := json.Unmarshal(raw, &id)
	return id, err
}

// Same is the scripts' same_identity: the same pane and the same session
// source, agent, kind and value.
func (id Identity) Same(o Identity) bool { return id == o }

// Live identity results, as live_identity's exit codes.
const (
	LiveOK      = 0 // a usable observation
	LiveGone    = 3 // the pane is conclusively gone
	LiveUnknown = 4 // unavailable or indeterminate
)

// LiveIdentity is the scripts' live_identity: herdr's record of the agent
// in pane, checked strictly (ready, not launching, a known status, typed
// fields, an id session). It returns LiveGone when herdr says the pane or
// agent does not exist, LiveUnknown for any other failure or a record that
// does not pass. The note is the scripts' stderr line for an unavailable
// call ("" otherwise).
func LiveIdentity(ctx context.Context, c *herdr.Client, pane string) (Identity, int, string) {
	a, err := c.AgentGet(ctx, pane)
	if err != nil {
		if he, ok := errors.AsType[*herdr.Error](err); ok && (he.Code == "pane_not_found" || he.Code == "agent_not_found") {
			return Identity{}, LiveGone, ""
		}
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return Identity{}, LiveUnknown, "agent get unavailable: " + msg
	}
	v, err := Decode(a.Raw)
	m := Object(v)
	if err != nil || m == nil {
		return Identity{}, LiveUnknown, ""
	}
	sess := Object(m["agent_session"])
	status, _ := String(m, "agent_status")
	pid, _ := String(m, "pane_id")
	_, termOK := String(m, "terminal_id")
	_, wsOK := String(m, "workspace_id")
	_, tabOK := String(m, "tab_id")
	_, focusOK := m["focused"].(bool)
	rev, revOK := Int(m, "revision")
	value, _ := String(sess, "value")
	ready, _ := m["interactive_ready"].(bool)
	// (.launch_pending // false)==false: missing, null and false pass.
	lp := m["launch_pending"]
	lok := lp == nil || lp == false
	if pid != pane || !NonEmpty(m, "agent") || !ready || !lok ||
		!slices.Contains([]string{"idle", "working", "blocked", "done"}, status) ||
		!termOK || !wsOK || !tabOK || !focusOK || !revOK || rev < 0 ||
		sess == nil || sess["kind"] != "id" || !SessionValueOK(value) || !NonEmpty(sess, "source") || !NonEmpty(sess, "agent") {
		return Identity{}, LiveUnknown, ""
	}
	return Identity{Pane: pid, Session: Session{Agent: sess["agent"].(string), Kind: "id", Source: sess["source"].(string), Value: value}}, LiveOK, ""
}

// PreflightValid is the scripts' preflight_valid, the setup proof: the
// proof file is <inbox>/setup.json, strict JSON, names this inbox with a
// verified policy, and lists rules and scripts, each an absolute path and
// a checksum; every listed file has its checksum, and every script of the
// installed folder (and the adapters beside it) is listed.
func (r *Root) PreflightValid() bool {
	if r.Preflight == "" || r.Preflight != r.Path("setup.json") {
		return false
	}
	b, ok := ReadStrict(r.Dir, r.Preflight)
	if !ok {
		return false
	}
	v, err := Decode(b)
	proof := Object(v)
	if err != nil || proof == nil || proof["inbox"] != r.Dir || proof["policy_verified"] != true {
		return false
	}
	rules, rok := proof["rules"].([]any)
	scripts, sok := proof["scripts"].([]any)
	if !rok || !sok || len(rules) == 0 || len(scripts) == 0 {
		return false
	}
	type entry struct{ path, sum string }
	var all, listed []entry
	for i, e := range append(append([]any{}, rules...), scripts...) {
		m := Object(e)
		if len(m) != 2 {
			return false
		}
		p, pok := String(m, "path")
		c, cok := String(m, "checksum")
		if !pok || !cok || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\t\r\n\\") || !checksumShape(c) {
			return false
		}
		all = append(all, entry{p, c})
		if i >= len(rules) {
			listed = append(listed, entry{p, c})
		}
	}
	paths := map[string]bool{}
	for _, e := range listed {
		if paths[e.path] {
			return false
		}
		paths[e.path] = true
	}
	installed := r.Installed
	if installed == "" {
		for _, e := range listed {
			if filepath.Base(e.path) == "message-lib.sh" {
				installed = filepath.Dir(e.path)
				break
			}
		}
	}
	if installed == "" {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(installed, "*.sh"))
	for _, a := range []string{"approval-gate", "transcript-audit", "checkpointer-guard", "checkpointer"} {
		if p := filepath.Join(filepath.Dir(installed), a+".sh"); Exists(p) {
			files = append(files, p)
		}
	}
	for _, f := range files {
		if !paths[f] {
			return false
		}
	}
	for _, e := range all {
		if !Regular(r.Dir, e.path) {
			return false
		}
		if sum, err := DigestFile(e.path); err != nil || sum != e.sum {
			return false
		}
	}
	return true
}

func checksumShape(c string) bool {
	crc, size, ok := strings.Cut(c, "-")
	return ok && digits(crc) && digits(size)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Boundary faults, as the scripts' BOUNDARY_FAULT.
const (
	FaultStatic    = "static"    // the proof, the registry, an exposure record or the setup fields
	FaultList      = "list"      // no usable agent list
	FaultInventory = "inventory" // a live agent outside the protected setup
)

// Unknown is a live agent outside the protected setup.
type Unknown struct {
	Pane    string          `json:"pane"`
	Agent   any             `json:"agent"`
	Session json.RawMessage `json:"session"`
}

// BoundaryOK is the scripts' boundary_ok: the protected human setup holds
// (a fresh proof, the approved registry, no exposure record, every setup
// field of the proof's boundary) and every live agent of the server is the
// approved pair, a courier pane or a sandboxed lower-trust launch. On a
// failure it returns the fault and, for FaultInventory, the unknown agents.
func (r *Root) BoundaryOK(ctx context.Context, c *herdr.Client) (bool, string, []Unknown) {
	if r.ProofStale || !r.PreflightValid() {
		return false, FaultStatic, nil
	}
	if _, ok := r.Registry(); !ok {
		return false, FaultStatic, nil
	}
	if Exists(r.Path("state", "boundary-tainted")) {
		return false, FaultStatic, nil
	}
	pb, _ := os.ReadFile(r.Path("peers.approved"))
	pair, _ := Decode(pb)
	pb2, _ := os.ReadFile(r.Preflight)
	pv, _ := Decode(pb2)
	b := Object(Object(pv)["boundary"])
	if !boundaryFields(b, pair) {
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
		id := map[string]any{"pane": a.PaneID, "session": sess}
		var harness any
		if a.Agent.Agent != nil {
			harness = *a.Agent.Agent
		}
		if knownAgent(b, id, harness) {
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

func boundaryFields(b map[string]any, pair any) bool {
	if b == nil {
		return false
	}
	if v, ok := Int(b, "version"); !ok || v != 1 {
		return false
	}
	for _, k := range []string{"inventory_current", "private_home", "human_checked", "protected_domain", "launch_admission"} {
		if b[k] != true {
			return false
		}
	}
	if !Equal(b["pair"], pair) {
		return false
	}
	if _, ok := b["courier_panes"].([]any); !ok {
		return false
	}
	lower, ok := b["lower_trust"].([]any)
	if !ok {
		return false
	}
	for _, l := range lower {
		m := Object(l)
		for _, k := range []string{"sandbox", "inbox_denied", "socket_denied", "code_denied", "proof_denied"} {
			if m[k] != true {
				return false
			}
		}
		if !NonEmpty(m, "launch_hash") || Object(m["identity"]) == nil {
			return false
		}
	}
	return true
}

func knownAgent(b map[string]any, id map[string]any, harness any) bool {
	for _, p := range Object(b["pair"]) {
		if Equal(Object(p)["identity"], id) {
			return true
		}
	}
	for _, p := range b["courier_panes"].([]any) {
		if Equal(p, id) {
			return true
		}
	}
	for _, l := range b["lower_trust"].([]any) {
		m := Object(l)
		if Equal(m["identity"], id) && Equal(m["harness"], harness) {
			return true
		}
	}
	return false
}

// The authority lines, as message-authority.sh prints them.
const (
	RelayLine = "RELAY current protected setup; bind each human go before use."
	HumanLine = "HUMAN relayed gos authorize nothing outward or irreversible. Human executes the action; automatic resume and card relay are held. Tell the user."
)

// Authority is message-authority.sh's decision: RelayLine and exit 0 when
// the boundary holds, HumanLine and exit 3 when it does not.
func (r *Root) Authority(ctx context.Context, c *herdr.Client) (string, int) {
	if ok, _, _ := r.BoundaryOK(ctx, c); ok {
		return RelayLine, 0
	}
	return HumanLine, 3
}
