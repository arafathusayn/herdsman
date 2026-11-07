package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The route's registry on the store (design sections 1.3 and 3, item S6):
// parties, approvals and credentials. Each operation is one transaction
// in the operation map's order; the launch match (S32, S33) comes later.

// OpError is an operation's refusal with its API code.
type OpError struct{ Code, Msg string }

func (e *OpError) Error() string { return e.Code + ": " + e.Msg }

func opErr(code, format string, a ...any) error {
	return &OpError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Party is one parties row; Session is the strict JSON identity.
type Party struct {
	Name, Role, Pane, Session string
	Generation                int64
	State                     string
}

// InitRoute gives a new route database its meta rows: the first epoch and
// sequence (InitEvents), registry_rev 0 and last_message_id 0. A route
// that has them keeps them.
func InitRoute(c Conn, epoch string) error {
	if err := InitEvents(c, epoch); err != nil {
		return err
	}
	for k, v := range map[string]string{"registry_rev": "0", "last_message_id": "0"} {
		if _, err := c.Exec("INSERT OR IGNORE INTO meta(k, v) VALUES (?, ?)", k, v); err != nil {
			return err
		}
	}
	return nil
}

func parties(c Conn, where string, args ...any) ([]Party, error) {
	var out []Party
	err := c.Query("SELECT name, role, pane, session, generation, state FROM parties WHERE "+where+" ORDER BY name", args, func(r Row) error {
		out = append(out, Party{Name: r.Text(0), Role: r.Text(1), Pane: r.Text(2), Session: r.Text(3), Generation: r.Int64(4), State: r.Text(5)})
		return nil
	})
	return out, err
}

// PendingSHA256 is the checksum of the pending pair the human inspects
// and approves: the pending rows, by name, as canonical JSON.
func PendingSHA256(c Conn) (string, error) {
	ps, err := parties(c, "state = 'pending'")
	if err != nil {
		return "", err
	}
	type entry struct {
		Name    string          `json:"name"`
		Role    string          `json:"role"`
		Pane    string          `json:"pane"`
		Session json.RawMessage `json:"session"`
	}
	es := []entry{}
	for _, p := range ps {
		es = append(es, entry{p.Name, p.Role, p.Pane, json.RawMessage(p.Session)})
	}
	b, err := json.Marshal(es)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Enroll records a pending enrollment (7.4): a retired name never
// returns, an enrolled name keeps its identity, and a role, pane or
// session already pending is role_taken. Enrolling again with the same
// identity is a no-op.
func Enroll(c Conn, p Party, now int64) error {
	if p.Role != "adjutant" && p.Role != "orchestrator" {
		return opErr("bad_request", "invalid role")
	}
	return c.Tx(func(tx Conn) error {
		same, err := parties(tx, "name = ?", p.Name)
		if err != nil {
			return err
		}
		if len(same) == 1 {
			switch old := same[0]; {
			case old.State == "retired":
				return opErr("name_retired", "retired name; use a fresh generation")
			case old.Role != p.Role || old.Pane != p.Pane || old.Session != p.Session:
				return opErr("name_retired", "name is immutable; use a fresh generation name")
			}
		}
		clash, err := parties(tx, "state = 'pending' AND name <> ? AND (role = ? OR pane = ? OR session = ?)", p.Name, p.Role, p.Pane, p.Session)
		if err != nil {
			return err
		}
		if len(clash) > 0 {
			return opErr("role_taken", "role or session already enrolled")
		}
		if len(same) == 1 {
			// The same party enrolling again with its own identity, pending
			// or approved, is a no-op.
			return nil
		}
		if _, err := tx.Exec("INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at) VALUES (?, ?, ?, ?, 1, 'pending', ?)",
			p.Name, p.Role, p.Pane, p.Session, now); err != nil {
			return err
		}
		// Every enroll voids the open launches; until the launch match
		// (S32) no web group is exempt (section 3 enroll row).
		if err := voidOpenLaunches(tx, "registry changed"); err != nil {
			return err
		}
		if err := bumpRegistry(tx); err != nil {
			return err
		}
		return pairEvent(tx, now, "pending")
	})
}

func bumpRegistry(tx Conn) error {
	rev, err := metaInt(tx, "registry_rev")
	if err != nil {
		return err
	}
	return metaSet(tx, "registry_rev", strconv.FormatInt(rev+1, 10))
}

// PeerInfo is one party of a pair event (5.3): name, role, pane and the
// first 8 characters of its session id.
type PeerInfo struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Pane    string `json:"pane"`
	Session string `json:"session"`
}

// pairData is a pair event's 5.3 type fields.
type pairData struct {
	State           string     `json:"state"`
	PendingChecksum string     `json:"pending_checksum,omitempty"`
	Peers           []PeerInfo `json:"peers"`
}

// pairEvent appends the pair event of a registry change: every party not
// retired (after an approval, the approved pair) and, for an enroll or a
// retire, the new pending checksum.
func pairEvent(tx Conn, now int64, state string) error {
	d := pairData{State: state, Peers: []PeerInfo{}}
	if state != "approved" {
		sum, err := PendingSHA256(tx)
		if err != nil {
			return err
		}
		d.PendingChecksum = sum
	}
	ps, err := parties(tx, "state <> 'retired'")
	if err != nil {
		return err
	}
	for _, p := range ps {
		var s struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal([]byte(p.Session), &s)
		d.Peers = append(d.Peers, PeerInfo{Name: p.Name, Role: p.Role, Pane: p.Pane, Session: clip(s.Value, 8)})
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = AppendEvent(tx, Event{Time: now, Type: "pair", Data: string(b)})
	return err
}

// clip is s cut to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Credential is a party's new credential: its option, the secret's
// SHA-256 (for identity, Approve stores the SHA-256 of the approved
// session) and, for option file, the secret.
type Credential struct {
	Option string
	SHA256 string
	Secret []byte
}

// ApproveRequest is the human's (or an ask's) approval of the pending
// pair (7.5).
type ApproveRequest struct {
	SHA256      string // the inspected pending checksum
	RequestHash string // SHA-256 of the exact request: a retry answers the stored result (F5)
	Interaction string
	AskedBy     string
	ApprovedBy  string // terminal, web, adjutant-ask, first-pairing
	Credentials map[string]Credential
}

// ApprovedCredential and ApproveResult are an approval's answer.
type ApprovedCredential struct {
	Name   string `json:"name"`
	Option string `json:"option"`
}

type ApproveResult struct {
	Approved    bool                 `json:"approved"`
	Credentials []ApprovedCredential `json:"credentials"`
	Revoked     []string             `json:"revoked"`
}

// Approve approves the pending pair in one transaction (section 3): the
// F5 retry check first (the same request answers its stored result and
// issues nothing), then the asker rule, then the approvals row, the
// superseded parties retired with their credentials and open launches
// (every approved party is of one of the pair's roles, so this revokes
// every old credential), the pair approved, the new credentials written,
// the open launches voided, registry_rev raised and the pair event.
func Approve(c Conn, q ApproveRequest, now int64) (ApproveResult, error) {
	var res ApproveResult
	if len(q.Interaction) > 200 || !utf8.ValidString(q.Interaction) || strings.ContainsAny(q.Interaction, "\n\r") {
		return res, opErr("bad_request", "invalid interaction reference")
	}
	if q.ApprovedBy == "import" {
		return res, opErr("bad_request", "approved_by import is the importer's alone (D1)")
	}
	err := c.Tx(func(tx Conn) error {
		stored := ""
		if err := tx.Query("SELECT result FROM approvals WHERE request_sha256 = ?", []any{q.RequestHash}, func(r Row) error { stored = r.Text(0); return nil }); err != nil {
			return err
		}
		if stored != "" {
			return json.Unmarshal([]byte(stored), &res)
		}
		if err := askerRule(tx, q); err != nil {
			return err
		}
		sum, err := PendingSHA256(tx)
		if err != nil {
			return err
		}
		if q.SHA256 != sum {
			return opErr("checksum_mismatch", "approve requires the inspected pending checksum (SHA-256)")
		}
		pending, err := parties(tx, "state = 'pending'")
		if err != nil {
			return err
		}
		if len(pending) != 2 || pending[0].Role == pending[1].Role {
			return opErr("incomplete_pair", "approve exactly one complete pair: one adjutant and one orchestrator")
		}
		names := map[string]bool{pending[0].Name: true, pending[1].Name: true}
		for n := range q.Credentials {
			if !names[n] {
				return opErr("bad_request", "credential for %q, who is not in the pending pair", n)
			}
		}
		res = ApproveResult{Approved: true, Credentials: []ApprovedCredential{}, Revoked: []string{}}
		for _, p := range pending {
			cr, ok := q.Credentials[p.Name]
			if !ok {
				return opErr("bad_request", "no credential for %q", p.Name)
			}
			res.Credentials = append(res.Credentials, ApprovedCredential{p.Name, cr.Option})
		}
		err = tx.Query("SELECT DISTINCT name FROM credentials WHERE state = 'active' AND name NOT IN (?, ?) ORDER BY name",
			[]any{pending[0].Name, pending[1].Name}, func(r Row) error { res.Revoked = append(res.Revoked, r.Text(0)); return nil })
		if err != nil {
			return err
		}
		sort.Strings(res.Revoked)
		resJSON, _ := json.Marshal(res)
		seq, err := metaInt(tx, "next_seq")
		if err != nil {
			return err
		}
		var asked any
		if q.AskedBy != "" {
			asked = q.AskedBy
		}
		if _, err := tx.Exec("INSERT INTO approvals(seq, registry_sha256, request_sha256, interaction, asked_by, approved_by, result, time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			seq, sum, q.RequestHash, q.Interaction, asked, q.ApprovedBy, string(resJSON), now); err != nil {
			return err
		}
		for _, p := range pending {
			old, err := parties(tx, "state = 'approved' AND role = ?", p.Role)
			if err != nil {
				return err
			}
			for _, o := range old {
				if err := retireRow(tx, o.Name, now, "superseded"); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec("UPDATE parties SET state = 'approved', approved_at = ? WHERE state = 'pending' AND name IN (?, ?)", now, pending[0].Name, pending[1].Name); err != nil {
			return err
		}
		for _, p := range pending {
			cr := q.Credentials[p.Name]
			var secret any
			if cr.Option == "file" {
				secret = cr.Secret
			}
			if cr.Option == "identity" {
				// No secret: the row binds the approved live identity.
				sum := sha256.Sum256([]byte(p.Session))
				cr.SHA256 = hex.EncodeToString(sum[:])
			}
			gen := int64(0)
			if err := tx.Query("SELECT coalesce(max(generation), 0) + 1 FROM credentials WHERE name = ?", []any{p.Name}, func(r Row) error { gen = r.Int64(0); return nil }); err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO credentials(name, generation, option, sha256, secret, state, approval_seq) VALUES (?, ?, ?, ?, ?, 'active', ?)",
				p.Name, gen, cr.Option, cr.SHA256, secret, seq); err != nil {
				return err
			}
		}
		if err := voidOpenLaunches(tx, "registry changed"); err != nil {
			return err
		}
		if err := bumpRegistry(tx); err != nil {
			return err
		}
		return pairEvent(tx, now, "approved")
	})
	return res, err
}

// askerRule is the gate's rule for an approval that answers an ask (PR
// 28): once the route has an approved adjutant only its ask is a go, and
// the orchestrator's own ask is refused. The human's own approval (no
// interaction, or a paired browser "web:<device>") is not an ask.
func askerRule(tx Conn, q ApproveRequest) error {
	if q.Interaction == "" || strings.HasPrefix(q.Interaction, "web:") {
		return nil
	}
	if len(q.AskedBy) > 200 || !utf8.ValidString(q.AskedBy) || strings.IndexFunc(q.AskedBy, unicode.IsControl) >= 0 {
		return opErr("bad_request", "invalid asked_by")
	}
	approved, err := parties(tx, "state = 'approved'")
	if err != nil {
		return err
	}
	adjutant, roles := "", map[string]string{}
	for _, p := range approved {
		roles[p.Name] = p.Role
		if p.Role == "adjutant" {
			adjutant = p.Name
		}
	}
	switch {
	case adjutant == "" || q.AskedBy == adjutant:
		return nil
	case roles[q.AskedBy] == "orchestrator":
		return opErr("bad_request", "the orchestrator's own ask is no go in a route with an adjutant; ask again through the adjutant")
	}
	return opErr("bad_request", "trusted interaction was not asked by the approved adjutant")
}

// retireRow retires one party, revokes its credentials and voids its open
// launches with reason.
func retireRow(tx Conn, name string, now int64, reason string) error {
	if _, err := tx.Exec("UPDATE parties SET state = 'retired', retired_at = ? WHERE name = ?", now, name); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE credentials SET state = 'revoked' WHERE name = ?", name); err != nil {
		return err
	}
	_, err := tx.Exec("UPDATE launches SET void_reason = ? WHERE name = ? AND used_by_approval IS NULL AND void_reason IS NULL", reason, name)
	return err
}

func voidOpenLaunches(tx Conn, reason string) error {
	_, err := tx.Exec("UPDATE launches SET void_reason = ? WHERE used_by_approval IS NULL AND void_reason IS NULL", reason)
	return err
}

// Retire retires a party (7.7): the name never returns, its credentials
// are revoked, its open launches and every other open launch are voided.
func Retire(c Conn, name string, now int64) error {
	return c.Tx(func(tx Conn) error {
		ps, err := parties(tx, "name = ? AND state <> 'retired'", name)
		if err != nil {
			return err
		}
		if len(ps) == 0 {
			return opErr("unknown_name", "unknown retirement name")
		}
		if err := retireRow(tx, name, now, "retired name"); err != nil {
			return err
		}
		if err := voidOpenLaunches(tx, "registry changed"); err != nil {
			return err
		}
		if err := bumpRegistry(tx); err != nil {
			return err
		}
		return pairEvent(tx, now, "retired")
	})
}

// Revoke revokes a party's active credential.
func Revoke(c Conn, name string) error {
	return c.Tx(func(tx Conn) error {
		n, err := tx.Exec("UPDATE credentials SET state = 'revoked' WHERE name = ? AND state = 'active'", name)
		if err != nil {
			return err
		}
		if n == 0 {
			return opErr("unknown_name", "%s has no credential", name)
		}
		return nil
	})
}

// ErrNoCredential is a name with no active credential.
var ErrNoCredential = errors.New("no active credential")

// ActiveCredential returns a party's active credential, looked up in this
// route's database only (P1-3); the secret is set for option file.
func ActiveCredential(c Conn, name string) (Credential, error) {
	var cr Credential
	found := false
	err := c.Query("SELECT option, sha256, secret FROM credentials WHERE name = ? AND state = 'active'", []any{name}, func(r Row) error {
		cr, found = Credential{Option: r.Text(0), SHA256: r.Text(1), Secret: r.Bytes(2)}, true
		return nil
	})
	if err == nil && !found {
		err = ErrNoCredential
	}
	return cr, err
}
