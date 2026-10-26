package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

var keyRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// root is a read-only view of the inbox for the file-format checks of
// internal/inbox (AckValid, ClaimValid, Unacked), which use only the
// folder.
func (r *Route) root() *inbox.Root { return &inbox.Root{Dir: r.inbox} }

// boxInit creates a mailbox with its folders, none of them a link.
func (r *Route) boxInit(name string) error {
	if !inbox.NameOK(name) {
		return fmt.Errorf("invalid mailbox name %q", name)
	}
	for _, d := range []string{"tmp", "new", "ready", "cur", "quarantine", "ack", "notices"} {
		if err := mkdirs(r.inbox, name+"/"+d); err != nil {
			return err
		}
	}
	return nil
}

// SendRequest is POST /v1/routes/{route}/messages.
type SendRequest struct {
	To      string `json:"to"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
	Key     string `json:"key"`
}

// SendResult is a send's answer.
type SendResult struct {
	ID        string `json:"id"`
	Seq       int64  `json:"seq"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// validate checks a send request's fields (step 1, the parts that need no
// file).
func (q *SendRequest) validate() *APIError {
	switch {
	case !inbox.NameOK(q.To):
		return Errf("bad_request", "invalid recipient name")
	case !slices.Contains(inbox.Kinds, q.Kind):
		return Errf("bad_request", "invalid kind")
	case q.ReplyTo != "" && !inbox.IDOK(q.ReplyTo):
		return Errf("bad_request", "invalid reply id")
	case !keyRe.MatchString(q.Key):
		return Errf("bad_request", "invalid send key: 32 lower-case hex digits")
	case !utf8.ValidString(q.Body) || utf8.RuneCountInString(q.Body) > inbox.MaxBody:
		return Errf("bad_request", "body must be valid UTF-8 of at most %d characters", inbox.MaxBody)
	}
	return nil
}

// send is one send (4.2 Send) for the authenticated party from.
func (r *Route) send(from string, q SendRequest) (SendResult, *APIError) {
	if e := q.validate(); e != nil {
		return SendResult{}, e
	}
	bodySum := sha256Hex([]byte(q.Body))
	if k, ok := r.sendKeys[q.Key]; ok {
		if k.From == from && k.To == q.To && k.Kind == q.Kind && k.ReplyTo == q.ReplyTo && k.BodySHA256 == bodySum {
			return SendResult{ID: k.ID, Seq: k.Seq, Duplicate: true}, nil
		}
		return SendResult{}, Errf("key_conflict", "send key reused with other content")
	}
	if e := r.deliveryAllowed(); e != nil {
		return SendResult{}, e
	}
	peers, _ := r.approvedPeers()
	var me, to *inbox.Peer
	for i := range peers {
		if peers[i].Name == from {
			me = &peers[i]
		}
		if peers[i].Name == q.To {
			to = &peers[i]
		}
	}
	if me == nil {
		return SendResult{}, Errf("unauthorized", "%s is not an approved party", from)
	}
	if to == nil {
		return SendResult{}, Errf("bad_request", "unknown peer")
	}
	if q.ReplyTo != "" && !regular(r.path("ids", q.ReplyTo, "envelope")) {
		return SendResult{}, Errf("bad_request", "unknown reply id")
	}
	if r.keysInWindow() >= keyWindowMax {
		return SendResult{}, Errf("key_window_full", "%d send keys are inside the retry window; retry with the same key shortly", keyWindowMax)
	}
	if e := r.caps(q); e != nil {
		return SendResult{}, e
	}
	if r.lastID >= 999999999999 {
		return SendResult{}, Errf("internal", "message sequence exhausted")
	}
	if err := r.boxInit(q.To); err != nil {
		return SendResult{}, Errf("internal", "mailbox: %v", err)
	}
	// Step 2: allocate the id and write the envelope, not yet visible.
	n := r.lastID + 1
	id := fmt.Sprintf("m%012d", n)
	// An allocated id is never reused: an existing ids/<id> fails closed
	// before any file changes.
	if exists(r.path("ids", id)) {
		return SendResult{}, Errf("internal", "message id %s is already allocated; the route's sequence is behind its files, inspect state/sequence", id)
	}
	if err := writeDurable(r.path("state", "sequence"), []byte(strconv.FormatInt(n, 10)+"\n"), 0o600); err != nil {
		return SendResult{}, Errf("internal", "sequence: %v", err)
	}
	r.lastID = n
	if err := mkdirDurable(r.path("ids", id)); err != nil {
		return SendResult{}, Errf("internal", "allocation: %v", err)
	}
	env := prettyJSON(obj("version", 1, "id", id, "from", from, "to", q.To,
		"from_identity", identityObj(me.Identity), "to_identity", identityObj(to.Identity),
		"time", r.now().Unix(), "kind", q.Kind, "reply_to", q.ReplyTo, "body", q.Body))
	if len(env) > inbox.MaxSize {
		return SendResult{}, Errf("bad_request", "encoded envelope too large; not queued")
	}
	if err := writeDurable(r.path("ids", id, "envelope"), env, 0o600); err != nil {
		return SendResult{}, Errf("internal", "envelope: %v", err)
	}
	r.crash("send.envelope")
	// Step 3: the commit point.
	reply := q.ReplyTo
	rec := &Record{Op: "send", ID: id, Key: q.Key, From: from, To: q.To, Kind: q.Kind, ReplyTo: q.ReplyTo, BodySHA256: bodySum,
		Event: &Event{Type: "message", ID: id, From: from, To: q.To, Kind: q.Kind, ReplyTo: &reply}}
	if err := r.commit(rec); err != nil {
		return SendResult{}, Errf("internal", "journal: %v", err)
	}
	r.rememberSend(rec)
	r.crash("send.record")
	// Step 4: the visible file.
	if err := r.redoSend(rec); err != nil {
		return SendResult{}, r.unfinish(rec, err)
	}
	r.crash("send.queue")
	r.publish(rec)
	return SendResult{ID: id, Seq: rec.Seq}, nil
}

// rememberSend indexes a send record's key.
func (r *Route) rememberSend(rec *Record) {
	r.sendKeys[rec.Key] = &sendKey{Key: rec.Key, ID: rec.ID, From: rec.From, To: rec.To, Kind: rec.Kind, ReplyTo: rec.ReplyTo,
		BodySHA256: rec.BodySHA256, Time: rec.Time, Seq: rec.Seq}
	// At most 512 keys in memory and in a checkpoint: a key past its retry
	// window is kept while there is room, then dropped oldest first (5.4).
	// A key inside the window is never dropped; admission already refused
	// a 513th one.
	for len(r.sendKeys) > keyWindowMax {
		now := r.now().Unix()
		var oldest *sendKey
		for _, k := range r.sendKeys {
			if now-k.Time >= int64(keyWindow.Seconds()) && (oldest == nil || k.Seq < oldest.Seq) {
				oldest = k
			}
		}
		if oldest == nil {
			return
		}
		delete(r.sendKeys, oldest.Key)
	}
}

// keysInWindow counts send keys inside the retry window.
func (r *Route) keysInWindow() int {
	now := r.now().Unix()
	n := 0
	for _, k := range r.sendKeys {
		if now-k.Time < int64(keyWindow.Seconds()) {
			n++
		}
	}
	return n
}

// caps are the script's backlog and side-agent budget (5.2).
func (r *Route) caps(q SendRequest) *APIError {
	box := r.path(q.To)
	var source any
	if q.Kind == "side_agent_data" {
		v, err := inbox.Decode([]byte(q.Body))
		src := inbox.Object(inbox.Object(v)["source"])
		if err != nil || src == nil || len(src) != 2 || src["pane"] == nil || src["session"] == nil {
			return Errf("bad_request", "side-agent source identity required")
		}
		source = src
	}
	rt := r.root()
	backlog, service, sourceCount := 0, 0, 0
	for _, d := range []string{"new", "ready", "cur", "quarantine"} {
		for _, f := range inbox.MdFiles(filepath.Join(box, d)) {
			id := strings.TrimSuffix(filepath.Base(f), ".md")
			if r.acks.valid(rt, box, id) {
				continue
			}
			var m map[string]any
			if inbox.Bounded(r.inbox, f) {
				if b, err := os.ReadFile(f); err == nil {
					v, _ := inbox.Decode(b)
					m = inbox.Object(v)
				}
			}
			if m != nil && m["kind"] == "side_agent_data" {
				service++
				if source != nil {
					bs, _ := m["body"].(string)
					bv, _ := inbox.Decode([]byte(bs))
					if inbox.Equal(inbox.Object(bv)["source"], source) {
						sourceCount++
					}
				}
			} else {
				backlog++
			}
		}
	}
	if q.Kind == "side_agent_data" {
		if service >= sideBudget || sourceCount >= sidePerSource {
			return Errf("budget_full", "side-agent budget full; human inspection required")
		}
	} else if backlog >= backlogMax {
		return Errf("backlog_full", "pending backlog full; inspect status")
	}
	return nil
}

// redoSend is a send's step 4, idempotent: the queue file from the
// envelope when the message is in no queue and has no terminal record.
func (r *Route) redoSend(rec *Record) error {
	box := r.path(rec.To)
	if exists(filepath.Join(box, "ready", rec.ID+".md")) || exists(filepath.Join(box, "cur", rec.ID+".md")) ||
		exists(filepath.Join(box, "quarantine", rec.ID+".md")) || exists(r.path("ids", rec.ID, "terminal.json")) {
		return nil
	}
	env, err := readSmall(r.path("ids", rec.ID, "envelope"), inbox.MaxSize)
	if err != nil {
		return err
	}
	if err := r.boxInit(rec.To); err != nil {
		return err
	}
	return writeDurable(filepath.Join(box, "ready", rec.ID+".md"), env, 0o600)
}

func identityObj(id inbox.Identity) ordered {
	s := id.Session
	return obj("pane", id.Pane, "session", obj("agent", s.Agent, "kind", s.Kind, "source", s.Source, "value", s.Value))
}

// ClaimRequest is POST /v1/routes/{route}/claims: a first read carries a
// read_key; an extension carries the token and an extend_key (4.2).
type ClaimRequest struct {
	ReadKey   string `json:"read_key,omitempty"`
	Token     string `json:"token,omitempty"`
	ExtendKey string `json:"extend_key,omitempty"`
}

// ManifestEntry is one manifest line of a read, as message read prints it.
type ManifestEntry struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Kind    string `json:"kind"`
	Time    int64  `json:"time"`
	ReplyTo string `json:"reply_to"`
	Path    string `json:"path"`
}

// ClaimSummary is a read's summary line.
type ClaimSummary struct {
	PendingNew int `json:"pending_new"`
	Quarantine int `json:"quarantine"`
}

// ClaimResult is a read's answer: the token, the manifest (paths, never
// bodies) and the summary.
type ClaimResult struct {
	Claim    string          `json:"claim"`
	Manifest []ManifestEntry `json:"manifest"`
	Summary  ClaimSummary    `json:"summary"`
}

// claimPayload is a claim payload file (5.7).
type claimPayload struct {
	Token string   `json:"token"`
	N     int      `json:"n"`
	IDs   []string `json:"ids"`
}

// claim is a read for the party me: a first read with a read_key, or an
// extension of the active claim with its token and an extend_key.
func (r *Route) claim(me string, q ClaimRequest) (ClaimResult, *APIError) {
	if e := r.deliveryAllowed(); e != nil {
		return ClaimResult{}, e
	}
	if err := r.boxInit(me); err != nil {
		return ClaimResult{}, Errf("internal", "mailbox: %v", err)
	}
	active := r.activeToken(me)
	if q.Token == "" {
		if !keyRe.MatchString(q.ReadKey) || q.ExtendKey != "" {
			return ClaimResult{}, Errf("bad_request", "a first read carries read_key (32 lower-case hex digits) only")
		}
		if k, ok := r.readKeys[q.ReadKey]; ok && k.Box == me {
			return r.manifest(me, k.Token)
		}
		if active != "" {
			return ClaimResult{}, Errf("claim_held", "reader claim held; recover with the recorded token, do not start another reader")
		}
		return r.firstClaim(me, q.ReadKey)
	}
	if !keyRe.MatchString(q.ExtendKey) || q.ReadKey != "" || !keyRe.MatchString(q.Token) {
		return ClaimResult{}, Errf("bad_request", "an extension carries token and extend_key (32 lower-case hex digits)")
	}
	if k, ok := r.extendKeys[q.ExtendKey]; ok && k.Box == me && k.Token == q.Token {
		return r.manifest(me, k.Token)
	}
	if active == "" || active != q.Token {
		return ClaimResult{}, Errf("no_claim", "no such active claim")
	}
	if r.claimExtensions(q.Token) >= extendKeyMax {
		return ClaimResult{}, Errf("key_window_full", "%d extensions of this claim are inside the retry window", extendKeyMax)
	}
	return r.extendClaim(me, q.Token, q.ExtendKey)
}

// claimExtensions counts a claim's extend keys inside the window.
func (r *Route) claimExtensions(token string) int {
	now := r.now().Unix()
	n := 0
	for _, k := range r.extendKeys {
		if k.Token == token && now-k.Time < int64(keyWindow.Seconds()) {
			n++
		}
	}
	return n
}

// readyIDs lists a mailbox's ready messages, sorted, at most claimMaxIDs.
func (r *Route) readyIDs(box string) []string {
	var ids []string
	for _, f := range inbox.MdFiles(r.path(box, "ready")) {
		ids = append(ids, strings.TrimSuffix(filepath.Base(f), ".md"))
		if len(ids) == claimMaxIDs {
			break
		}
	}
	return ids
}

func (r *Route) firstClaim(me, readKey string) (ClaimResult, *APIError) {
	token := randomHex(16)
	ids := r.readyIDs(me)
	data, _ := encode(claimPayload{Token: token, N: 1, IDs: nonNil(ids)})
	pl, err := r.writePayload(filepath.Join("state", "claims", token+"-1.json"), data)
	if err != nil {
		return ClaimResult{}, Errf("internal", "claim payload: %v", err)
	}
	rec := &Record{Op: "claim", Box: me, Token: token, ReadKey: readKey, N: 1, Payload: pl}
	if err := r.commit(rec); err != nil {
		return ClaimResult{}, Errf("internal", "journal: %v", err)
	}
	r.readKeys[readKey] = &claimKey{Key: readKey, Token: token, Box: me, N: 1, Time: rec.Time}
	r.crash("claim.record")
	if err := r.redoClaim(rec); err != nil {
		return ClaimResult{}, r.unfinish(rec, err)
	}
	r.publish(rec)
	return r.manifest(me, token)
}

func (r *Route) extendClaim(me, token, extendKey string) (ClaimResult, *APIError) {
	n := 1
	for _, k := range r.extendKeys {
		if k.Token == token && k.N > n-1 {
			n = k.N + 1
		}
	}
	if n < 2 {
		n = 2
	}
	for exists(r.path("state", "claims", token+"-"+strconv.Itoa(n)+".json")) {
		n++
	}
	ids := r.readyIDs(me)
	rec := &Record{Op: "claim_extend", Box: me, Token: token, ExtendKey: extendKey, N: n}
	if len(ids) == 0 {
		rec.Empty = true
	} else {
		data, _ := encode(claimPayload{Token: token, N: n, IDs: ids})
		pl, err := r.writePayload(filepath.Join("state", "claims", token+"-"+strconv.Itoa(n)+".json"), data)
		if err != nil {
			return ClaimResult{}, Errf("internal", "claim payload: %v", err)
		}
		rec.Payload = pl
	}
	if err := r.commit(rec); err != nil {
		return ClaimResult{}, Errf("internal", "journal: %v", err)
	}
	r.extendKeys[extendKey] = &claimKey{Key: extendKey, Token: token, Box: me, N: n, Empty: rec.Empty, Time: rec.Time}
	r.crash("extend.record")
	if err := r.redoClaim(rec); err != nil {
		return ClaimResult{}, r.unfinish(rec, err)
	}
	r.publish(rec)
	return r.manifest(me, token)
}

// redoClaim is a claim's or an extension's step 4, idempotent: publish
// the claim folder (first claim only), then for each id write its claim
// record and move it from ready to cur.
func (r *Route) redoClaim(rec *Record) error {
	box := r.path(rec.Box)
	if err := r.boxInit(rec.Box); err != nil {
		return err
	}
	if rec.Op == "claim" {
		active := r.activeToken(rec.Box)
		if active == "" && !exists(filepath.Join(box, "claim")) {
			stage := filepath.Join(box, "tmp", ".claim."+rec.Token)
			if err := mkdirs(box, "tmp/.claim."+rec.Token); err != nil {
				return err
			}
			if err := writeDurable(filepath.Join(stage, "token"), []byte(rec.Token+"\n"), 0o600); err != nil {
				return err
			}
			if err := writeDurable(filepath.Join(stage, "owner"), []byte(rec.Box+":daemon\n"), 0o600); err != nil {
				return err
			}
			r.crash("claim.staged")
			if err := renameDurable(stage, filepath.Join(box, "claim")); err != nil {
				return err
			}
			r.crash("claim.published")
		} else if active != rec.Token {
			// A later claim replaced this one; its ids were bound then.
			return nil
		}
	}
	if rec.Payload == nil {
		return nil
	}
	b, err := r.readPayload(rec.Payload, 64<<10)
	if err != nil {
		return err
	}
	var p claimPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	for _, id := range p.IDs {
		if !inbox.IDOK(id) {
			continue
		}
		if !exists(r.path("ids", id, "claim")) || strings.TrimSpace(readText(r.path("ids", id, "claim"))) != rec.Token {
			if exists(filepath.Join(box, "ready", id+".md")) {
				if err := mkdirs(r.inbox, "ids/"+id); err != nil {
					return err
				}
				if err := writeDurable(r.path("ids", id, "claim"), []byte(rec.Token+"\n"), 0o600); err != nil {
					return err
				}
				r.crash("claim.bound")
			}
		}
		src := filepath.Join(box, "ready", id+".md")
		if exists(src) {
			dst := filepath.Join(box, "cur", id+".md")
			if exists(dst) {
				return fmt.Errorf("%w: %s is in both ready and cur", errHeld, id)
			}
			if err := renameDurable(src, dst); err != nil {
				return err
			}
			r.crash("claim.moved")
		}
	}
	return nil
}

func readText(p string) string {
	b, _ := readSmall(p, 4096)
	return string(b)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// manifest is the pure function of the files (4.2): every cur entry bound
// to the token and not acked, sorted by id.
func (r *Route) manifest(me, token string) (ClaimResult, *APIError) {
	box := r.path(me)
	rt := r.root()
	res := ClaimResult{Claim: token, Manifest: []ManifestEntry{}}
	for _, f := range inbox.MdFiles(filepath.Join(box, "cur")) {
		id := strings.TrimSuffix(filepath.Base(f), ".md")
		if r.acks.valid(rt, box, id) || strings.TrimSpace(readText(r.path("ids", id, "claim"))) != token {
			continue
		}
		m, ok := inbox.Envelope(r.inbox, f)
		if !ok {
			return ClaimResult{}, Errf("internal", "invalid cur envelope %s; held for inspection", id)
		}
		t, _ := inbox.Int(m, "time")
		e := ManifestEntry{ID: id, Path: f, Time: t}
		e.From, _ = inbox.String(m, "from")
		e.To, _ = inbox.String(m, "to")
		e.Kind, _ = inbox.String(m, "kind")
		e.ReplyTo, _ = inbox.String(m, "reply_to")
		res.Manifest = append(res.Manifest, e)
	}
	res.Summary = ClaimSummary{PendingNew: inbox.CountState(filepath.Join(box, "new")), Quarantine: inbox.CountState(filepath.Join(box, "quarantine"))}
	return res, nil
}

// AckRequest is POST /v1/routes/{route}/claims/{token}/acks.
type AckRequest struct {
	ID     string `json:"id"`
	Record string `json:"record"`
}

// AckResult is an ack's answer.
type AckResult struct {
	ID      string `json:"id"`
	Seq     int64  `json:"seq,omitempty"`
	Already bool   `json:"already,omitempty"`
}

// ack acknowledges one claimed message after the party's durable intake
// (4.2 Ack).
func (r *Route) ack(me, token string, q AckRequest) (AckResult, *APIError) {
	if !inbox.IDOK(q.ID) {
		return AckResult{}, Errf("bad_request", "invalid message id")
	}
	if q.Record == "" || len(q.Record) > inbox.MaxSize {
		return AckResult{}, Errf("bad_request", "nonempty processing record of at most %d bytes required", inbox.MaxSize)
	}
	if e := r.deliveryAllowed(); e != nil {
		return AckResult{}, e
	}
	box := r.path(me)
	active := r.activeToken(me)
	if active == "" || active != token {
		return AckResult{}, Errf("no_claim", "wrong active reader claim")
	}
	cur := filepath.Join(box, "cur", q.ID+".md")
	if _, ok := inbox.Envelope(r.inbox, cur); !ok || strings.TrimSpace(readText(r.path("ids", q.ID, "claim"))) != token {
		return AckResult{}, Errf("no_claim", "no claimed envelope %s", q.ID)
	}
	sum, _ := inbox.DigestFile(cur)
	ackFile := filepath.Join(box, "ack", q.ID+".md.json")
	if exists(ackFile) {
		b, err := readSmall(ackFile, inbox.MaxSize)
		v, _ := inbox.Decode(b)
		m := inbox.Object(v)
		if err == nil && m != nil && m["claim"] == token && m["processing_record"] == q.Record && m["envelope_checksum"] == sum {
			return AckResult{ID: q.ID, Already: true}, nil
		}
		return AckResult{}, Errf("terminal_conflict", "an existing ack record differs; held, never overwritten")
	}
	terminal := r.path("ids", q.ID, "terminal.json")
	want := prettyJSON(obj("id", q.ID, "state", "handled", "envelope_checksum", sum))
	if exists(terminal) {
		b, _ := readSmall(terminal, inbox.MaxSize)
		v1, _ := inbox.Decode(b)
		v2, _ := inbox.Decode(want)
		if !inbox.Equal(v1, v2) {
			return AckResult{}, Errf("terminal_conflict", "conflicting or invalid terminal disposition held for inspection; not overwritten")
		}
	}
	data := prettyJSON(obj("id", q.ID, "claim", token, "time", r.now().Unix(), "processing_record", q.Record, "envelope_checksum", sum))
	if len(data) > inbox.MaxSize {
		return AckResult{}, Errf("ack_too_large", "processing record too large once encoded (%d bytes over %d); message remains unhandled", len(data), inbox.MaxSize)
	}
	env, _ := inbox.Envelope(r.inbox, cur)
	sender, _ := inbox.String(env, "from")
	pl, err := r.writePayload(filepath.Join("ids", q.ID, "ack.json"), data)
	if err != nil {
		return AckResult{}, Errf("internal", "ack payload: %v", err)
	}
	rec := &Record{Op: "ack", ID: q.ID, Box: me, Token: token, Payload: pl,
		Event: &Event{Type: "receipt", ID: q.ID, By: me, To: sender, State: "handled"}}
	if err := r.commit(rec); err != nil {
		return AckResult{}, Errf("internal", "journal: %v", err)
	}
	r.crash("ack.record")
	if err := r.redoAck(rec); err != nil {
		return AckResult{}, r.unfinish(rec, err)
	}
	r.publish(rec)
	return AckResult{ID: q.ID, Seq: rec.Seq}, nil
}

// redoAck is an ack's step 4, idempotent: the ack file from the payload's
// exact bytes (hash checked), then the terminal record.
func (r *Route) redoAck(rec *Record) error {
	box := r.path(rec.Box)
	ackFile := filepath.Join(box, "ack", rec.ID+".md.json")
	if !exists(ackFile) {
		b, err := r.readPayload(rec.Payload, inbox.MaxSize)
		if err != nil {
			return err
		}
		if err := r.boxInit(rec.Box); err != nil {
			return err
		}
		if err := writeDurable(ackFile, b, 0o600); err != nil {
			return err
		}
		r.crash("ack.file")
	}
	terminal := r.path("ids", rec.ID, "terminal.json")
	if !exists(terminal) {
		sum, err := inbox.DigestFile(filepath.Join(box, "cur", rec.ID+".md"))
		if err != nil {
			return err
		}
		if err := writeDurable(terminal, prettyJSON(obj("id", rec.ID, "state", "handled", "envelope_checksum", sum)), 0o600); err != nil {
			return err
		}
		r.crash("ack.terminal")
	}
	return nil
}

// release releases a fully acknowledged claim (4.2 Release).
func (r *Route) release(me, token string) *APIError {
	active := r.activeToken(me)
	if active == "" || active != token {
		return Errf("no_claim", "no such active claim")
	}
	if len(r.unacked(r.path(me))) > 0 {
		return Errf("unacked_work", "acknowledge all delivered work before releasing claim; read again first")
	}
	rec := &Record{Op: "release", Box: me, Token: token}
	if err := r.commit(rec); err != nil {
		return Errf("internal", "journal: %v", err)
	}
	r.dropClaimKeys(token)
	r.crash("release.record")
	if err := r.redoRelease(rec); err != nil {
		return r.unfinish(rec, err)
	}
	r.publish(rec)
	return nil
}

// redoRelease is a release's step 4, idempotent.
func (r *Route) redoRelease(rec *Record) error {
	box := r.path(rec.Box)
	gone := filepath.Join(box, "tmp", ".released."+rec.Token)
	// A replayed claim record may have bound the claim again after an
	// earlier, interrupted release: the old renamed folder goes first.
	if err := dropClaimDir(gone); err != nil {
		return err
	}
	if r.activeToken(rec.Box) == rec.Token {
		if err := renameDurable(filepath.Join(box, "claim"), gone); err != nil {
			return err
		}
		r.crash("release.renamed")
	}
	return dropClaimDir(gone)
}

// dropClaimDir removes a released claim folder (its token and owner
// files, then the folder); a missing folder is done.
func dropClaimDir(p string) error {
	if !exists(p) {
		return nil
	}
	_ = os.Remove(filepath.Join(p, "token"))
	_ = os.Remove(filepath.Join(p, "owner"))
	return os.Remove(p)
}

// replay redoes step 4 of every record after the last checkpoint (4.4
// step 4), in order, idempotently.
func (r *Route) replay(recs []*Record) error {
	for i, rec := range recs {
		// A protected write is applied only as the input lock's owner;
		// while another process holds it, this record and every later
		// one stay unfinished, in order, for the first turn that can
		// take the lock (3.7).
		if protectedRecord(rec) {
			unlock, e := r.tryInputLock()
			if e != nil {
				r.d.logf("route %s: %s %d waits for the input lock (%s)", r.name, rec.Op, rec.Seq, e.Message)
				for _, later := range recs[i:] {
					r.restoreKeys(later)
				}
				r.unfinished = append(r.unfinished, recs[i:]...)
				return nil
			}
			err := r.redo(rec)
			unlock()
			if err != nil {
				return fmt.Errorf("route %s: replay of %s %d: %w", r.name, rec.Op, rec.Seq, err)
			}
			continue
		}
		if err := r.redo(rec); err != nil {
			return fmt.Errorf("route %s: replay of %s %d: %w", r.name, rec.Op, rec.Seq, err)
		}
	}
	return nil
}

// redo is one committed record's step 4, idempotent, with the key indexes
// it restores.
func (r *Route) redo(rec *Record) error {
	if err := r.redoFiles(rec); err != nil {
		return err
	}
	return r.settle(rec)
}

// settle fsyncs every folder a record's step 4 changes. A retry or a
// replay may find an effect already visible (a rename done, a file
// written) whose folder fsync failed or never ran; the folders are synced
// again whatever the effect's state, so no checkpoint passes an unsynced
// move (4.2).
func (r *Route) settle(rec *Record) error {
	var dirs []string
	box := func(b string, qs ...string) {
		if !inbox.NameOK(b) {
			return
		}
		dirs = append(dirs, r.path(b))
		for _, q := range qs {
			dirs = append(dirs, r.path(b, q))
		}
	}
	id := func(i string) {
		if inbox.IDOK(i) {
			dirs = append(dirs, r.path("ids"), r.path("ids", i))
		}
	}
	switch rec.Op {
	case "send":
		box(rec.To, "ready")
	case "claim", "claim_extend":
		box(rec.Box, "ready", "cur", "tmp")
		if rec.Payload != nil {
			if b, err := r.readPayload(rec.Payload, 64<<10); err == nil {
				var p claimPayload
				if json.Unmarshal(b, &p) == nil {
					for _, i := range p.IDs {
						id(i)
					}
				}
			}
		}
	case "ack":
		box(rec.Box, "ack", "cur")
		id(rec.ID)
	case "release":
		box(rec.Box, "tmp")
	case "pair":
		dirs = append(dirs, r.inbox, r.path("enrolled"), r.path("state"), r.path("state", "credentials"))
	case "hold", "unhold":
		dirs = append(dirs, r.path("state"))
	case "notice":
		id(rec.ID)
	case "intake":
		box(rec.To, "new", "ready", "quarantine")
		box(rec.From, "notices")
		id(rec.ID)
	}
	for _, d := range dirs {
		if !exists(d) {
			continue
		}
		if err := syncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// redoFiles applies one record's step 4 files.
func (r *Route) redoFiles(rec *Record) error {
	switch rec.Op {
	case "send":
		r.rememberSend(rec)
		return r.redoSend(rec)
	case "claim":
		r.readKeys[rec.ReadKey] = &claimKey{Key: rec.ReadKey, Token: rec.Token, Box: rec.Box, N: rec.N, Time: rec.Time}
		return r.redoClaim(rec)
	case "claim_extend":
		r.extendKeys[rec.ExtendKey] = &claimKey{Key: rec.ExtendKey, Token: rec.Token, Box: rec.Box, N: rec.N, Empty: rec.Empty, Time: rec.Time}
		return r.redoClaim(rec)
	case "ack":
		return r.redoAck(rec)
	case "release":
		r.dropClaimKeys(rec.Token)
		return r.redoRelease(rec)
	case "pair":
		return r.redoPair(rec)
	case "hold", "unhold":
		return r.redoHold(rec)
	case "notice":
		return r.redoNotice(rec)
	case "intake":
		return r.redoIntake(rec)
	}
	return nil
}

// restoreKeys restores the key indexes a deferred record carries, so a
// retry is answered and no orphan is abandoned before its files exist.
func (r *Route) restoreKeys(rec *Record) {
	switch rec.Op {
	case "send":
		r.rememberSend(rec)
	case "claim":
		r.readKeys[rec.ReadKey] = &claimKey{Key: rec.ReadKey, Token: rec.Token, Box: rec.Box, N: rec.N, Time: rec.Time}
	case "claim_extend":
		r.extendKeys[rec.ExtendKey] = &claimKey{Key: rec.ExtendKey, Token: rec.Token, Box: rec.Box, N: rec.N, Empty: rec.Empty, Time: rec.Time}
	}
}

// dropClaimKeys forgets a released claim's read and extend keys, so the
// key state stays bounded by the open claims (4.2).
func (r *Route) dropClaimKeys(token string) {
	for k, v := range r.readKeys {
		if v.Token == token {
			delete(r.readKeys, k)
		}
	}
	for k, v := range r.extendKeys {
		if v.Token == token {
			delete(r.extendKeys, k)
		}
	}
}

// unfinish keeps a committed record whose step 4 failed: the route admits
// no other operation, publishes nothing past it and writes no checkpoint
// until its files are finished (do retries them first).
func (r *Route) unfinish(rec *Record, err error) *APIError {
	r.unfinished = append(r.unfinished, rec)
	r.d.logf("route %s: %s %d committed; its files are not finished: %v", r.name, rec.Op, rec.Seq, err)
	return Errf("busy", "the %s is committed (record %d) but its files are not finished; retry with the same key", rec.Op, rec.Seq)
}

// finishUnfinished applies the step 4 of every unfinished record in
// order and publishes each; the first failure stops the route's work.
func (r *Route) finishUnfinished() *APIError {
	for len(r.unfinished) > 0 {
		rec := r.unfinished[0]
		// A protected write is retried only as the input lock's owner,
		// as its handler wrote it (3.7).
		var unlock func()
		if protectedRecord(rec) {
			u, e := r.tryInputLock()
			if e != nil {
				return Errf("busy", "route %s: committed record %d waits for the input lock (%s); retry", r.name, rec.Seq, e.Message)
			}
			unlock = u
		}
		err := r.redo(rec)
		if unlock != nil {
			unlock()
		}
		if err != nil {
			r.d.logf("route %s: %s %d still unfinished: %v", r.name, rec.Op, rec.Seq, err)
			return Errf("busy", "route %s cannot finish committed record %d (%v); retry", r.name, rec.Seq, err)
		}
		r.unfinished = r.unfinished[1:]
		r.publish(rec)
	}
	return nil
}

// protectedRecord reports a record whose files the shared input lock
// guards: holds, and side-agent data queued under the boundary check.
func protectedRecord(rec *Record) bool {
	return rec.Op == "hold" || rec.Op == "unhold" || rec.Op == "send" && rec.Kind == "side_agent_data"
}

// recoverOrphans handles state no record names (4.4 step 5): an envelope
// allocated after the last committed send is abandoned with a notice; a
// staged claim with no record goes to recovery; other partial writes are
// reported and kept.
func (r *Route) recoverOrphans() {
	recorded := map[string]bool{}
	var maxRecorded int64
	for _, k := range r.sendKeys {
		recorded[k.ID] = true
		if n, _ := strconv.ParseInt(strings.TrimPrefix(k.ID, "m"), 10, 64); n > maxRecorded {
			maxRecorded = n
		}
	}
	if ents, err := os.ReadDir(r.path("ids")); err == nil {
		var orphans []string
		for _, e := range ents {
			id := e.Name()
			if !inbox.IDOK(id) || recorded[id] || !regular(r.path("ids", id, "envelope")) || exists(r.path("ids", id, "terminal.json")) {
				continue
			}
			n, _ := strconv.ParseInt(id[1:], 10, 64)
			if n <= maxRecorded || r.inAnyQueue(id) {
				continue
			}
			orphans = append(orphans, id)
		}
		sort.Strings(orphans)
		for _, id := range orphans {
			if err := r.abandon(id); err != nil {
				r.d.logf("route %s: abandon %s: %v", r.name, id, err)
			}
		}
	}
	for _, box := range r.boxes() {
		ents, err := os.ReadDir(r.path(box, "tmp"))
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if tok, ok := strings.CutPrefix(n, ".claim."); ok && e.IsDir() {
				known := false
				for _, k := range r.readKeys {
					known = known || k.Token == tok
				}
				if !known {
					_ = mkdirs(r.inbox, box+"/recovery")
					_ = os.Rename(r.path(box, "tmp", n), r.path(box, "recovery", n))
					r.d.logf("route %s: uncommitted claim %s moved to %s/recovery", r.name, n, box)
				}
				continue
			}
			r.d.logf("route %s: partial write kept in %s/tmp: %s", r.name, box, n)
		}
	}
}

// inAnyQueue reports whether a message id is in some mailbox's queue.
func (r *Route) inAnyQueue(id string) bool {
	for _, b := range r.boxes() {
		for _, d := range []string{"new", "ready", "cur", "quarantine"} {
			if exists(r.path(b, d, id+".md")) {
				return true
			}
		}
	}
	return false
}

// abandon disposes of an uncommitted send: the terminal "abandoned" and a
// notice to its sender. It is never delivered.
func (r *Route) abandon(id string) error {
	sender := ""
	if b, err := readSmall(r.path("ids", id, "envelope"), inbox.MaxSize); err == nil {
		v, _ := inbox.Decode(b)
		sender, _ = inbox.String(inbox.Object(v), "from")
	}
	rec := &Record{Op: "notice", ID: id, To: sender, State: "abandoned"}
	if inbox.NameOK(sender) {
		rec.Event = &Event{Type: "notice", ID: id, To: sender, State: "abandoned"}
	}
	if err := r.commit(rec); err != nil {
		return err
	}
	if err := r.redoNotice(rec); err != nil {
		return r.unfinish(rec, err)
	}
	r.publish(rec)
	return nil
}

// redoNotice writes an abandoned terminal record, idempotently.
func (r *Route) redoNotice(rec *Record) error {
	if rec.State != "abandoned" || exists(r.path("ids", rec.ID, "terminal.json")) {
		return nil
	}
	return writeDurable(r.path("ids", rec.ID, "terminal.json"), prettyJSON(obj("id", rec.ID, "state", "abandoned")), 0o600)
}
