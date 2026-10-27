package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The importer of `route adopt` (sqlite-storage.md section 5, step 3):
// it reads a file-mode inbox, which nothing writes while adoption holds
// its locks, into a staging route database in the caller's transaction.
// This file holds the core (S13): the approved parties, the messages of
// ready/, cur/ and quarantine/ with their terminals, the acks byte for
// byte, an active reader claim, the id high-water mark and one import_log
// row per source file read. Every read is strict and guarded as the
// courier's: bounded regular files under the inbox, no link on the way.

// importer is one import run.
type importer struct {
	tx     store.Conn
	rt     *inbox.Root
	now    int64
	lastID int64
	seen   map[int64]string // message id to the file that brought it
}

// importCore imports the core of the file-mode inbox dir into tx.
func importCore(tx store.Conn, dir string, now int64) *APIError {
	im := &importer{tx: tx, rt: &inbox.Root{Dir: dir}, now: now, seen: map[int64]string{}}
	peers, e := im.parties()
	if e != nil {
		return e
	}
	for _, p := range peers {
		token, e := im.claim(p.Name)
		if e != nil {
			return e
		}
		if e := im.box(p.Name, token); e != nil {
			return e
		}
	}
	// Notices name messages of any mailbox: after every message is in.
	for _, p := range peers {
		if e := im.notices(p.Name); e != nil {
			return e
		}
	}
	if e := im.highWater(); e != nil {
		return e
	}
	return im.state()
}

func importErr(format string, a ...any) *APIError {
	return Errf("internal", "import: "+format, a...)
}

// rel is a source path relative to the inbox, as import_log keys it.
func (im *importer) rel(p string) string {
	r, err := filepath.Rel(im.rt.Dir, p)
	if err != nil {
		return p
	}
	return r
}

// log records one source file with its SHA-256 and disposition.
func (im *importer) log(p string, b []byte, disposition string) *APIError {
	sum := sha256.Sum256(b)
	if _, err := im.tx.Exec(`INSERT INTO import_log(path, sha256, disposition, time) VALUES (?, ?, ?, ?)`,
		im.rel(p), hex.EncodeToString(sum[:]), disposition, im.now); err != nil {
		return importErr("import_log %s: %v", im.rel(p), err)
	}
	return nil
}

// strict reads a bounded regular file under the inbox as strict JSON.
func (im *importer) strict(p string) ([]byte, map[string]any, *APIError) {
	b, ok := inbox.ReadStrict(im.rt.Dir, p)
	if !ok {
		return nil, nil, importErr("%s is not a strict JSON file under the inbox (link, size or syntax)", im.rel(p))
	}
	v, _ := inbox.Decode(b)
	o := inbox.Object(v)
	if o == nil {
		return nil, nil, importErr("%s is not a JSON object", im.rel(p))
	}
	return b, o, nil
}

// parties imports the approved pair: peers equal to peers.approved and
// valid, else checksum_mismatch (no pending change is imported).
func (im *importer) parties() ([]inbox.Peer, *APIError) {
	peers, ok := im.rt.Registry()
	if !ok || len(peers) == 0 {
		return nil, Errf("checksum_mismatch", "import: the registry is not an approved pair with no pending change")
	}
	for _, f := range []string{"peers", "peers.approved"} {
		b, _ := inbox.ReadStrict(im.rt.Dir, im.rt.Path(f))
		if e := im.log(im.rt.Path(f), b, "parties"); e != nil {
			return nil, e
		}
	}
	for _, p := range peers {
		session, _ := json.Marshal(p.Identity.Session)
		if _, err := im.tx.Exec(`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at)
			VALUES (?, ?, ?, ?, 1, 'approved', ?, ?)`, p.Name, p.Role, p.Identity.Pane, string(session), im.now, im.now); err != nil {
			return nil, importErr("party %s: %v", p.Name, err)
		}
		if f := im.rt.Path("enrolled", p.Name); inbox.Regular(im.rt.Dir, f) {
			b, _ := os.ReadFile(f)
			if e := im.log(f, b, "parties"); e != nil {
				return nil, e
			}
		}
	}
	return peers, nil
}

// claim imports a mailbox's active reader claim; it returns the token
// ("" for none).
func (im *importer) claim(box string) (string, *APIError) {
	dir := im.rt.Path(box, "claim")
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return "", nil
	}
	if !im.rt.ClaimValid(im.rt.Path(box)) {
		return "", importErr("%s is not a valid reader claim (a link, or no token and owner)", im.rel(dir))
	}
	token := inbox.CatText(filepath.Join(dir, "token"))
	owner := inbox.CatText(filepath.Join(dir, "owner"))
	if len(token) != 32 {
		// The route schema keeps 32-character tokens; a file-mode token
		// is a temporary folder name. Until the schema takes it (handoff),
		// the claim is refused, never renamed: its holder still uses it.
		return "", Errf("adopt_locked", "mailbox %s holds a file-mode reader claim (token %s); ack its messages and release it with herdsman message read %s %s --release, then adopt again", box, token, box, token)
	}
	if _, err := im.tx.Exec(`INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES (?, ?, ?, ?, 'active', ?)`,
		token, box, owner, "import-"+token, im.now); err != nil {
		return "", importErr("claim of %s: %v", box, err)
	}
	for _, f := range []string{"token", "owner"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if e := im.log(filepath.Join(dir, f), b, "claims"); e != nil {
			return "", e
		}
	}
	return token, nil
}

// box imports one mailbox's ready/, cur/ and quarantine/ messages.
func (im *importer) box(name, token string) *APIError {
	box := im.rt.Path(name)
	for _, q := range []string{"ready", "cur", "quarantine"} {
		dir := filepath.Join(box, q)
		if !inbox.NoLinks(box, dir) {
			return importErr("%s is a link", im.rel(dir))
		}
		ents, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return importErr("%s: %v", im.rel(dir), err)
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			if e := im.message(name, q, filepath.Join(dir, n), token); e != nil {
				return e
			}
		}
	}
	return nil
}

// messageID reads "m" and twelve digits.
func messageID(s string) (int64, bool) {
	if !inbox.IDOK(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s[1:], 10, 64)
	return n, err == nil
}

// message imports one queue file with its envelope, terminal and ack.
func (im *importer) message(box, queue, p, token string) *APIError {
	idText, ok := strings.CutSuffix(filepath.Base(p), ".md")
	id, idOK := messageID(idText)
	if !ok || !idOK {
		return importErr("%s is not a message file (m and twelve digits, .md)", im.rel(p))
	}
	if other, dup := im.seen[id]; dup {
		return importErr("%s is in two queues: %s and %s; reconcile before adopting", idText, other, im.rel(p))
	}
	im.seen[id] = im.rel(p)
	raw, env, e := im.strict(p)
	if e != nil {
		return e
	}
	body, bodyOK := inbox.String(env, "body")
	from, _ := inbox.String(env, "from")
	to, _ := inbox.String(env, "to")
	kind, _ := inbox.String(env, "kind")
	t, timeOK := inbox.Int(env, "time")
	if env["id"] != idText || to != box || !bodyOK || from == "" || kind == "" || !timeOK {
		return importErr("%s: the envelope does not match its file (id, recipient, body, sender, kind, time)", im.rel(p))
	}
	var reply any
	if r, _ := inbox.String(env, "reply_to"); r != "" {
		n, ok := messageID(r)
		if !ok {
			return importErr("%s: reply_to %q is not a message id", im.rel(p), r)
		}
		reply = n
	}
	header := make(map[string]any, len(env))
	for k, v := range env {
		if k != "body" {
			header[k] = v
		}
	}
	hb, _ := json.Marshal(header)
	sum := sha256.Sum256([]byte(body))
	state, claimTok := map[string]string{"ready": "ready", "cur": "cur", "quarantine": "quarantined"}[queue], any(nil)
	acked := queue == "cur" && im.rt.AckValid(im.rt.Path(box), idText)
	switch {
	case acked:
		state = "handled"
	case queue == "cur" && token != "" && inbox.CatText(im.rt.Path("ids", idText, "claim")) == token:
		claimTok = token
	}
	if _, err := im.tx.Exec(`INSERT INTO messages(id, sender, recipient, kind, reply_to, envelope, body, body_sha256, state, claim_token, created_seq, created_at, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'imported')`,
		id, from, to, kind, reply, string(hb), []byte(body), hex.EncodeToString(sum[:]), state, claimTok, t); err != nil {
		return importErr("message %s: %v", idText, err)
	}
	if e := im.log(p, raw, "messages "+state); e != nil {
		return e
	}
	if env := im.rt.Path("ids", idText, "envelope"); inbox.Regular(im.rt.Dir, env) {
		b, _ := os.ReadFile(env)
		if e := im.log(env, b, "messages "+state); e != nil {
			return e
		}
	}
	if acked {
		return im.ack(box, idText, id)
	}
	if queue == "quarantine" {
		return im.terminal(idText, id)
	}
	return nil
}

// ack imports ack/<id>.md.json byte for byte, with the handled terminal
// and the claim record it names.
func (im *importer) ack(box, idText string, id int64) *APIError {
	p := filepath.Join(im.rt.Path(box), "ack", idText+".md.json")
	b, a, e := im.strict(p)
	if e != nil {
		return e
	}
	claim, _ := inbox.String(a, "claim")
	t, _ := inbox.Int(a, "time")
	sum, _ := inbox.String(a, "envelope_checksum")
	if _, err := im.tx.Exec(`INSERT INTO acks(id, claim, time, record, envelope_checksum) VALUES (?, ?, ?, ?, ?)`, id, claim, t, b, sum); err != nil {
		return importErr("ack %s: %v", idText, err)
	}
	if e := im.log(p, b, "acks"); e != nil {
		return e
	}
	for _, f := range []string{"terminal.json", "claim"} {
		if q := im.rt.Path("ids", idText, f); inbox.Regular(im.rt.Dir, q) {
			qb, _ := os.ReadFile(q)
			if e := im.log(q, qb, "acks"); e != nil {
				return e
			}
		}
	}
	return nil
}

// terminal imports a quarantined message's terminal record.
func (im *importer) terminal(idText string, id int64) *APIError {
	p := im.rt.Path("ids", idText, "terminal.json")
	b, t, e := im.strict(p)
	if e != nil {
		return e
	}
	if t["id"] != idText || t["state"] != "quarantined" {
		return importErr("%s does not record %s as quarantined", im.rel(p), idText)
	}
	reason, _ := inbox.String(t, "reason")
	if reason == "" {
		reason = "quarantined by the file-mode courier"
	}
	if _, err := im.tx.Exec(`INSERT INTO terminals(id, state, reason) VALUES (?, 'quarantined', ?)`, id, reason); err != nil {
		return importErr("terminal %s: %v", idText, err)
	}
	return im.log(p, b, "terminals")
}

// highWater sets meta.last_message_id to the largest of state/sequence,
// every allocated ids/<id> folder and every imported message, so no id
// is allocated twice after the import (2ea167c's rule).
func (im *importer) highWater() *APIError {
	var n int64
	if p := im.rt.Path("state", "sequence"); inbox.Regular(im.rt.Dir, p) {
		b, _ := os.ReadFile(p)
		v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil || v < 0 || v > 999999999999 {
			return importErr("state/sequence is not a message number")
		}
		n = v
		if e := im.log(p, b, "meta last_message_id"); e != nil {
			return e
		}
	}
	ents, _ := os.ReadDir(im.rt.Path("ids"))
	for _, e := range ents {
		if v, ok := messageID(e.Name()); ok && v > n {
			n = v
		}
	}
	for v := range im.seen {
		n = max(n, v)
	}
	im.lastID = n
	if _, err := im.tx.Exec(`INSERT INTO meta(k, v) VALUES ('last_message_id', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, fmt.Sprintf("m%012d", n)); err != nil {
		return importErr("meta last_message_id: %v", err)
	}
	return nil
}
