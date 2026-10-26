package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// credential is one party's credential record in state/credentials.json:
// its option and, for file and keychain, the SHA-256 of the secret.
type credential struct {
	Option string `json:"option"`
	SHA256 string `json:"sha256,omitempty"`
}

// registryBytes reads a registry file ("{}" when absent).
func (r *Route) registryBytes(name string) ([]byte, bool) {
	p := r.path(name)
	if !exists(p) {
		return []byte("{}"), true
	}
	b, ok := inbox.ReadStrict(r.inbox, p)
	if !ok {
		return nil, false
	}
	if _, ok := inbox.ValidRegistry(b); !ok {
		return nil, false
	}
	return b, true
}

// approvedPeers are the approved registry's peers, when the registry has
// no pending change.
func (r *Route) approvedPeers() ([]inbox.Peer, bool) {
	return r.root().Registry()
}

// deliveryAllowed is the static part of every party write: an approved
// registry with no pending change and a current setup proof (5.2).
func (r *Route) deliveryAllowed() *APIError {
	if _, ok := r.approvedPeers(); !ok {
		return Errf("registry_pending", "a pair change waits for the human's approval; delivery is held")
	}
	if !r.proofValid() {
		return Errf("proof_stale", "setup proof stale or missing; nothing moves until the human runs herdsman daemon setup %s", r.name)
	}
	return nil
}

// peerInfos are the pair event's peers of a registry.
func peerInfos(b []byte) []PeerInfo {
	peers, _ := inbox.ValidRegistry(b)
	out := []PeerInfo{}
	for _, p := range peers {
		out = append(out, PeerInfo{Name: p.Name, Role: p.Role, Pane: p.Pane, Session: clip(p.Identity.Session.Value, 8)})
	}
	return out
}

// rawRegistry is a registry's entries in file order.
func rawRegistry(b []byte) ordered {
	var o ordered
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return o
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return o
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return o
		}
		o = append(o, kvPair{t.(string), v})
	}
	return o
}

// enroll records a party's pending enrollment (7.4) with its live
// identity, as message-register.sh propose did.
func (r *Route) enroll(name, role string, id inbox.Identity) (string, *APIError) {
	if !inbox.NameOK(name) {
		return "", Errf("bad_request", "invalid name")
	}
	if role != "adjutant" && role != "orchestrator" {
		return "", Errf("bad_request", "invalid role")
	}
	pb, ok := r.registryBytes("peers")
	if !ok {
		return "", Errf("internal", "invalid pending registry; inspect without committing it")
	}
	if exists(r.path("enrolled", name+".retired")) {
		return "", Errf("name_retired", "retired name; use a fresh generation")
	}
	candidate := obj("role", role, "identity", identityObj(id))
	cb, _ := encode(candidate)
	if enrolled := r.path("enrolled", name+".json"); exists(enrolled) {
		b, _ := readSmall(enrolled, 4096)
		v1, _ := inbox.Decode(b)
		v2, _ := inbox.Decode(cb)
		if !inbox.Equal(v1, v2) {
			return "", Errf("name_retired", "name is immutable; use a fresh generation name")
		}
	}
	cur := rawRegistry(pb)
	same := false
	for _, e := range cur {
		raw := e.V.(json.RawMessage)
		if e.K == name {
			v1, _ := inbox.Decode(raw)
			v2, _ := inbox.Decode(cb)
			same = inbox.Equal(v1, v2)
			continue
		}
		v, _ := inbox.Decode(raw)
		p := inbox.Object(v)
		pid := inbox.Object(p["identity"])
		sess, _ := inbox.Decode(mustEncode(identityObj(id)))
		if p["role"] == role || pid["pane"] == id.Pane || inbox.Equal(pid["session"], inbox.Object(sess)["session"]) {
			return "", Errf("role_taken", "role or session already enrolled")
		}
	}
	if same {
		return sha256Hex(pb), nil
	}
	next := ordered{}
	replaced := false
	for _, e := range cur {
		if e.K == name {
			next = append(next, kvPair{name, candidate})
			replaced = true
			continue
		}
		next = append(next, e)
	}
	if !replaced {
		next = append(next, kvPair{name, candidate})
	}
	nb := prettyJSON(next)
	sum := sha256Hex(nb)
	pl, err := r.writePayload(filepath.Join("state", "registry", sum+".json"), nb)
	if err != nil {
		return "", Errf("internal", "registry payload: %v", err)
	}
	rec := &Record{Op: "pair", State: "pending", Name: name, Payload: pl,
		Event: &Event{Type: "pair", State: "pending", PendingChecksum: sum, Peers: peerInfos(nb)}}
	if err := r.commit(rec); err != nil {
		return "", Errf("internal", "journal: %v", err)
	}
	if err := r.redoPair(rec); err != nil {
		return "", r.unfinish(rec, err)
	}
	r.publish(rec)
	return sum, nil
}

func mustEncode(v any) []byte {
	b, _ := encode(v)
	return b
}

// ApproveRequest is POST /v1/routes/{route}/pair/approve.
type ApproveRequest struct {
	SHA256      string            `json:"sha256"`
	Interaction string            `json:"interaction,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// ApprovedCredential is one party's credential option in an approval's
// answer.
type ApprovedCredential struct {
	Name   string `json:"name"`
	Option string `json:"option"`
}

// ApproveResult is an approval's answer.
type ApproveResult struct {
	Approved    bool                 `json:"approved"`
	Credentials []ApprovedCredential `json:"credentials"`
	Revoked     []string             `json:"revoked"`
}

// credentialOptions are the options this machine offers (7.5).
func credentialOptions() []string {
	if runtime.GOOS == "darwin" {
		return []string{"identity", "file", "keychain"}
	}
	return []string{"identity", "file"}
}

// approve is the human's pair approval (7.5): the pending registry's
// SHA-256, exactly one adjutant and one orchestrator, one credential per
// party by the chosen option (file by default). Old credentials are
// revoked.
func (r *Route) approve(q ApproveRequest) (ApproveResult, *APIError) {
	pb, ok := r.registryBytes("peers")
	if !ok {
		return ApproveResult{}, Errf("internal", "invalid pending registry; inspect without committing it")
	}
	if q.SHA256 != sha256Hex(pb) {
		return ApproveResult{}, Errf("checksum_mismatch", "approve requires the inspected pending checksum (SHA-256)")
	}
	peers, _ := inbox.ValidRegistry(pb)
	roles := map[string]bool{}
	for _, p := range peers {
		roles[p.Role] = true
	}
	if len(peers) != 2 || !roles["adjutant"] || !roles["orchestrator"] {
		return ApproveResult{}, Errf("incomplete_pair", "approve exactly one complete pair: one adjutant and one orchestrator")
	}
	if len(q.Interaction) > 200 || !utf8.ValidString(q.Interaction) || strings.ContainsAny(q.Interaction, "\n\r") {
		return ApproveResult{}, Errf("bad_request", "invalid interaction reference")
	}
	names := map[string]bool{}
	for _, p := range peers {
		names[p.Name] = true
	}
	for n := range q.Credentials {
		if !names[n] {
			return ApproveResult{}, Errf("bad_request", "credential option for %q, who is not in the pending pair", n)
		}
	}
	creds := map[string]credential{}
	secrets := map[string]string{}
	res := ApproveResult{Approved: true, Credentials: []ApprovedCredential{}, Revoked: []string{}}
	for _, p := range peers {
		opt := q.Credentials[p.Name]
		if opt == "" {
			opt = "file"
		}
		if !slices.Contains(credentialOptions(), opt) {
			if opt == "keychain" {
				return ApproveResult{}, Errf("option_unavailable", "the keychain option exists only on macOS")
			}
			return ApproveResult{}, Errf("bad_request", "unknown credential option %q", opt)
		}
		c := credential{Option: opt}
		if opt != "identity" {
			secret := randomHex(32)
			secrets[p.Name] = secret
			c.SHA256 = sha256Hex([]byte(secret))
		}
		creds[p.Name] = c
		res.Credentials = append(res.Credentials, ApprovedCredential{Name: p.Name, Option: opt})
	}
	for n := range r.creds {
		if _, kept := creds[n]; !kept {
			res.Revoked = append(res.Revoked, n)
		}
	}
	sort.Strings(res.Revoked)
	// Step 2: the new generation's secrets, staged under names no client
	// reads; the active secrets are untouched until the commit (7.5).
	gen := randomHex(8)
	if err := r.stageSecrets(gen, creds, secrets); err != nil {
		r.dropStaged(gen, creds)
		return ApproveResult{}, Errf("internal", "credentials: %v", err)
	}
	pl, err := r.writePayload(filepath.Join("state", "registry", sha256Hex(pb)+".json"), pb)
	if err != nil {
		r.dropStaged(gen, creds)
		return ApproveResult{}, Errf("internal", "registry payload: %v", err)
	}
	rec := &Record{Op: "pair", State: "approved", Payload: pl, Interaction: q.Interaction, Credentials: creds, Generation: gen,
		Event: &Event{Type: "pair", State: "approved", Peers: peerInfos(pb)}}
	if err := r.commit(rec); err != nil {
		r.dropStaged(gen, creds)
		return ApproveResult{}, Errf("internal", "journal: %v", err)
	}
	if err := r.redoPair(rec); err != nil {
		return ApproveResult{}, r.unfinish(rec, err)
	}
	r.publish(rec)
	r.healthChanged()
	return res, nil
}

// retire retires an approved name (7.7): it leaves the pending registry,
// never returns, and its credential is revoked.
func (r *Route) retire(name, sum string) (string, *APIError) {
	pb, ok := r.registryBytes("peers")
	if !ok {
		return "", Errf("internal", "invalid pending registry; inspect without committing it")
	}
	if sum != sha256Hex(pb) {
		return "", Errf("checksum_mismatch", "retire requires the inspected pending checksum (SHA-256)")
	}
	if _, ok := r.root().Peer(name); !ok {
		return "", Errf("unknown_name", "unknown retirement name")
	}
	var next ordered
	for _, e := range rawRegistry(pb) {
		if e.K != name {
			next = append(next, e)
		}
	}
	nb := []byte("{}\n")
	if len(next) > 0 {
		nb = prettyJSON(next)
	}
	pl, err := r.writePayload(filepath.Join("state", "registry", sha256Hex(nb)+".json"), nb)
	if err != nil {
		return "", Errf("internal", "registry payload: %v", err)
	}
	rec := &Record{Op: "pair", State: "retired", Name: name, Payload: pl,
		Event: &Event{Type: "pair", State: "retired", PendingChecksum: sha256Hex(nb), Peers: peerInfos(nb)}}
	if err := r.commit(rec); err != nil {
		return "", Errf("internal", "journal: %v", err)
	}
	if err := r.redoPair(rec); err != nil {
		return "", r.unfinish(rec, err)
	}
	r.publish(rec)
	return sha256Hex(nb), nil
}

// revoke revokes one party's credential.
func (r *Route) revoke(name string) *APIError {
	if _, ok := r.creds[name]; !ok {
		return Errf("unknown_name", "%s has no credential", name)
	}
	rec := &Record{Op: "pair", State: "revoked", Name: name}
	if err := r.commit(rec); err != nil {
		return Errf("internal", "journal: %v", err)
	}
	if err := r.redoPair(rec); err != nil {
		return r.unfinish(rec, err)
	}
	r.publish(rec)
	return nil
}

// redoPair is a pair record's step 4, idempotent.
func (r *Route) redoPair(rec *Record) error {
	var pb []byte
	if rec.Payload != nil {
		b, err := r.readPayload(rec.Payload, inbox.MaxSize)
		if err != nil {
			return err
		}
		pb = b
	}
	switch rec.State {
	case "pending":
		for _, e := range rawRegistry(pb) {
			if e.K == rec.Name {
				if err := mkdirs(r.inbox, "enrolled"); err != nil {
					return err
				}
				if !exists(r.path("enrolled", rec.Name+".json")) {
					v, _ := inbox.Decode(e.V.(json.RawMessage))
					b, _ := encode(v)
					if err := writeDurable(r.path("enrolled", rec.Name+".json"), append(b, '\n'), 0o600); err != nil {
						return err
					}
				}
			}
		}
		if err := writeIfDifferent(r.path("peers"), pb); err != nil {
			return err
		}
		return r.boxInit(rec.Name)
	case "approved":
		if err := writeIfDifferent(r.path("peers"), pb); err != nil {
			return err
		}
		if err := writeIfDifferent(r.path("peers.approved"), pb); err != nil {
			return err
		}
		if err := r.activate(rec.Generation, rec.Credentials); err != nil {
			return err
		}
		return r.setCredentials(rec.Credentials)
	case "retired":
		if err := mkdirs(r.inbox, "enrolled"); err != nil {
			return err
		}
		if !exists(r.path("enrolled", rec.Name+".retired")) {
			if err := writeDurable(r.path("enrolled", rec.Name+".retired"), []byte("retired\n"), 0o600); err != nil {
				return err
			}
		}
		if err := writeIfDifferent(r.path("peers"), pb); err != nil {
			return err
		}
		fallthrough
	case "revoked":
		next := map[string]credential{}
		for n, c := range r.creds {
			if n != rec.Name {
				next[n] = c
			}
		}
		return r.setCredentials(next)
	}
	return nil
}

// writeIfDifferent writes data durably unless the file already holds it.
func writeIfDifferent(p string, data []byte) error {
	if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, data) {
		return nil
	}
	return writeDurable(p, data, 0o600)
}

// loadCredentials reads state/credentials.json at start.
func (r *Route) loadCredentials() {
	r.creds = map[string]credential{}
	if b, err := readSmall(r.path("state", "credentials.json"), 64<<10); err == nil {
		_ = json.Unmarshal(b, &r.creds)
	}
	r.d.indexCredentials(r.name, r.creds)
}

// HoldRequest is PUT /v1/routes/{route}/holds/{name}.
type HoldRequest struct {
	Reason string `json:"reason"`
}

// HoldResult is a hold's or unhold's answer: the owners of the hold.
type HoldResult struct {
	Holds []string `json:"holds"`
}

// holdOwners reads state/hold-<name> in the owner format.
func (r *Route) holdOwners(name string) (ordered, bool) {
	p := r.path("state", "hold-"+name)
	if !exists(p) {
		return ordered{}, true
	}
	b, ok := inbox.ReadStrict(r.inbox, p)
	if !ok {
		return nil, false
	}
	top := rawRegistry(b)
	if len(top) != 1 || top[0].K != "holds" {
		return nil, false
	}
	return rawRegistry(top[0].V.(json.RawMessage)), true
}

// hold sets or clears (reason nil) me's hold of a target, under the
// shared input lock (3.7), in the file-mode owner format.
func (r *Route) hold(me, name string, reason *string) (HoldResult, *APIError) {
	if !inbox.NameOK(name) {
		return HoldResult{}, Errf("bad_request", "invalid hold target")
	}
	if reason != nil && utf8.RuneCountInString(*reason) > 300 {
		return HoldResult{}, Errf("bad_request", "hold reason longer than 300 characters")
	}
	if _, ok := r.root().Peer(name); !ok {
		b, sok := inbox.ReadStrict(r.inbox, r.path("resume-targets.json"))
		v, _ := inbox.Decode(b)
		if m := inbox.Object(v); !sok || m == nil || m[name] == nil {
			return HoldResult{}, Errf("unknown_target", "unknown hold target: %s", name)
		}
	}
	owners, ok := r.holdOwners(name)
	if !ok {
		return HoldResult{}, Errf("internal", "hold record is not in the owner format; human inspection required")
	}
	idx := -1
	for i, o := range owners {
		if o.K == me {
			idx = i
		}
	}
	op := "hold"
	if reason == nil {
		op = "unhold"
		if idx < 0 {
			return HoldResult{}, Errf("no_hold", "no hold by %s for %s; another owner's hold stays", me, name)
		}
		owners = append(owners[:idx:idx], owners[idx+1:]...)
	} else {
		entry := obj("reason", *reason, "time", r.now().Unix())
		if idx >= 0 {
			owners[idx].V = entry
		} else {
			owners = append(owners, kvPair{me, entry})
		}
	}
	rec := &Record{Op: op, Name: name, From: me}
	if len(owners) > 0 {
		pl, err := r.writePayload(filepath.Join("state", "holds", randomHex(8)+".json"), prettyJSON(obj("holds", owners)))
		if err != nil {
			return HoldResult{}, Errf("internal", "hold payload: %v", err)
		}
		rec.Payload = pl
	}
	if err := r.commit(rec); err != nil {
		return HoldResult{}, Errf("internal", "journal: %v", err)
	}
	if err := r.redoHold(rec); err != nil {
		return HoldResult{}, r.unfinish(rec, err)
	}
	r.publish(rec)
	res := HoldResult{Holds: []string{}}
	for _, o := range owners {
		res.Holds = append(res.Holds, o.K)
	}
	sort.Strings(res.Holds)
	return res, nil
}

// redoHold writes (or removes) the hold file from the record.
func (r *Route) redoHold(rec *Record) error {
	p := r.path("state", "hold-"+rec.Name)
	if rec.Payload == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := r.readPayload(rec.Payload, inbox.MaxSize)
	if err != nil {
		return err
	}
	return writeIfDifferent(p, b)
}

// inputLock takes the shared input lock herdr-mutation.lock (3.7):
// exclusive across processes and across this daemon's own requests. A
// lock folder that exists is never adopted, whoever holds it and however
// old it is: the caller waits up to LockWait seconds, then is busy, and a
// stale lock is the human's to inspect (mutation-lock-stale). It runs
// outside the route actor; the lock order is input lock, then actor.
func (r *Route) inputLock() (func(), *APIError) {
	dir := r.path(inbox.MutationLock)
	token := randomHex(16)
	for try := 0; ; try++ {
		err := mkdirDurable(dir)
		if err == nil {
			b, _ := encode(lockOwner{Owner: "daemon", Epoch: token, PID: os.Getpid(), Start: r.now().Unix()})
			werr := writeDurable(filepath.Join(dir, "pid"), []byte("daemon "+strconv.Itoa(os.Getpid())+"\n"), 0o600)
			if werr == nil {
				werr = writeDurable(filepath.Join(dir, "owner.json"), append(b, '\n'), 0o600)
			}
			if werr != nil {
				dropInputLock(dir, token)
				return nil, Errf("internal", "input lock: %v", werr)
			}
			return func() { dropInputLock(dir, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, Errf("internal", "input lock: %v", err)
		}
		if try+1 >= r.d.cfg.LockWait {
			return nil, Errf("busy", "input lock held (%s); retry with backoff, never auto-remove", lockHolder(dir))
		}
		r.d.sleep(time.Second)
	}
}

// dropInputLock removes the input lock only while it is still this
// request's (its owner record carries the request's token).
func dropInputLock(dir, token string) {
	if o, ok := daemonLockOwner(dir); !ok || o.Epoch != token {
		if exists(filepath.Join(dir, "owner.json")) {
			return
		}
	}
	_ = os.Remove(filepath.Join(dir, "owner.json"))
	_ = os.Remove(filepath.Join(dir, "pid"))
	_ = os.Remove(dir)
	_ = syncDir(filepath.Dir(dir))
}

// authority is RELAY under a current protected setup that matches the
// live inventory, HUMAN otherwise (8), with the reasons.
//
// The caller holds the shared input lock: an unknown agent the check
// observes is recorded (boundary-tainted) and a waiting exposure is folded
// under it before the answer, so a later check cannot regain RELAY when
// the agent leaves (8; the exposure rule of e93bcc1).
func (r *Route) authority(ctx context.Context, c *herdr.Client) (string, []string) {
	var reasons []string
	if !r.proofValid() {
		reasons = append(reasons, "setup-proof-stale")
	}
	if _, ok := r.approvedPeers(); !ok {
		reasons = append(reasons, "registry-not-approved")
	}
	if len(reasons) == 0 {
		doc, _ := r.readSetup()
		var b map[string]any
		v, _ := inbox.Decode(doc.Boundary)
		b = inbox.Object(v)
		if b != nil && b["pair"] == nil {
			// The version 2 proof's pair is the approved registry (7.3).
			pb, _ := os.ReadFile(r.path("peers.approved"))
			b["pair"], _ = inbox.Decode(pb)
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ok, fault, unknown := inbox.BoundaryV2(cctx, c, r.inbox, b)
		cancel()
		if !ok {
			reasons = append(reasons, "boundary-"+fault)
			if fault == inbox.FaultInventory || fault == inbox.FaultPending {
				if err := r.recordExposure(fault, unknown); err != nil {
					r.d.logf("route %s: exposure record: %v", r.name, err)
					reasons = append(reasons, "exposure-unrecorded")
				}
			}
		}
	}
	if len(reasons) == 0 {
		return "RELAY", []string{}
	}
	return "HUMAN", reasons
}

// recordExposure records an observed inventory exposure, or folds the
// waiting ones, with the input lock held by the caller.
func (r *Route) recordExposure(fault string, unknown []inbox.Unknown) error {
	root, err := inbox.Open(inbox.Options{Dir: r.inbox, ReadOnly: true, Now: r.d.now})
	if err != nil {
		return err
	}
	return (&inbox.Channel{Root: root}).RecordExposure(fault, unknown, true)
}

// healthFlags are the route's health flags (5.3 names).
func (r *Route) healthFlags() []string {
	flags := []string{}
	if !r.proofValid() {
		flags = append(flags, "setup-proof-stale")
	}
	return flags
}

// healthChanged records a health event when the flag set changed (D1).
func (r *Route) healthChanged() {
	flags := r.healthFlags()
	if slices.Equal(flags, r.flags) {
		return
	}
	var changed []string
	for _, f := range flags {
		if !slices.Contains(r.flags, f) {
			changed = append(changed, f)
		}
	}
	for _, f := range r.flags {
		if !slices.Contains(flags, f) {
			changed = append(changed, f)
		}
	}
	sort.Strings(changed)
	status := "ok"
	if len(flags) > 0 {
		status = "held"
	}
	rec := &Record{Op: "health", Event: &Event{Type: "health", Status: status, Flags: flags, Changed: changed}}
	if err := r.commit(rec); err != nil {
		r.d.logf("route %s: health record: %v", r.name, err)
		return
	}
	r.flags = flags
	r.publish(rec)
}
