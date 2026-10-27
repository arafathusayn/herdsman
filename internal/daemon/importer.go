package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// This file holds the core (S13): the approved pair, the messages of
// ready/, cur/ and quarantine/ with their terminals, the acks byte for
// byte, an active reader claim, the id high-water mark and one import_log
// row per source file read. Every file and folder is read through
// inbox.ReadSource and inbox.SourceDir (this user's, not writable by
// others, no link, no FIFO open, bounded). A missing optional file is
// skipped; a present one that fails a check refuses the import.

// importer is one import run.
type importer struct {
	tx     store.Conn
	rt     *inbox.Root
	now    int64
	lastID int64
	seen   map[int64]string // message id to the file that brought it
	logged map[string]bool  // import_log paths written
}

// importCore imports the core of the file-mode inbox dir into tx.
func importCore(tx store.Conn, dir string, now int64) *APIError {
	im := &importer{tx: tx, rt: &inbox.Root{Dir: dir}, now: now, seen: map[int64]string{}, logged: map[string]bool{}}
	if err := inbox.SourceDir(dir, dir); err != nil {
		return importErr("unsafe source: %v", err)
	}
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

// log records one source file with its SHA-256 and disposition, once.
func (im *importer) log(p string, b []byte, disposition string) *APIError {
	if im.logged[im.rel(p)] {
		return nil
	}
	im.logged[im.rel(p)] = true
	sum := sha256.Sum256(b)
	if _, err := im.tx.Exec(`INSERT INTO import_log(path, sha256, disposition, time) VALUES (?, ?, ?, ?)`,
		im.rel(p), hex.EncodeToString(sum[:]), disposition, im.now); err != nil {
		return importErr("import_log %s: %v", im.rel(p), err)
	}
	return nil
}

// read reads an optional source file: (nil, false) when it is missing.
func (im *importer) read(p string) ([]byte, bool, *APIError) {
	b, err := inbox.ReadSource(im.rt.Dir, p, inbox.MaxSize)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, importErr("unsafe source: %v", err)
	}
	return b, true, nil
}

// raw reads a source file that must be present.
func (im *importer) raw(p string) ([]byte, *APIError) {
	b, ok, e := im.read(p)
	if e == nil && !ok {
		e = importErr("%s is missing", im.rel(p))
	}
	return b, e
}

// strict reads a source file that must be present as a strict JSON object.
func (im *importer) strict(p string) ([]byte, map[string]any, *APIError) {
	b, e := im.raw(p)
	if e != nil {
		return nil, nil, e
	}
	if inbox.StrictJSON(b) != nil {
		return nil, nil, importErr("%s is not strict JSON", im.rel(p))
	}
	v, _ := inbox.Decode(b)
	o := inbox.Object(v)
	if o == nil {
		return nil, nil, importErr("%s is not a JSON object", im.rel(p))
	}
	return b, o, nil
}

// text reads an optional one-line source file, trimmed.
func (im *importer) text(p string) (string, []byte, bool, *APIError) {
	b, ok, e := im.read(p)
	return strings.TrimSpace(string(b)), b, ok, e
}

// names lists a source folder, sorted; a missing folder is empty.
func (im *importer) names(dir string) ([]string, *APIError) {
	if err := inbox.SourceDir(im.rt.Dir, dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, importErr("unsafe source: %v", err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, importErr("%s: %v", im.rel(dir), err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// parties imports the approved pair: one adjutant and one orchestrator,
// peers equal to peers.approved, else checksum_mismatch.
func (im *importer) parties() ([]inbox.Peer, *APIError) {
	files := map[string][]byte{}
	for _, f := range []string{"peers", "peers.approved"} {
		b, e := im.raw(im.rt.Path(f))
		if e != nil {
			return nil, e
		}
		files[f] = b
	}
	peers, ok := im.rt.Registry()
	roles := map[string]int{}
	for _, p := range peers {
		roles[p.Role]++
	}
	if !ok || len(peers) != 2 || roles["adjutant"] != 1 || roles["orchestrator"] != 1 {
		return nil, Errf("checksum_mismatch", "import: the registry is not an approved pair (one adjutant, one orchestrator) with no pending change")
	}
	for _, f := range []string{"peers", "peers.approved"} {
		if e := im.log(im.rt.Path(f), files[f], "parties"); e != nil {
			return nil, e
		}
	}
	for _, p := range peers {
		session, _ := json.Marshal(p.Identity.Session)
		if _, err := im.tx.Exec(`INSERT INTO parties(name, role, pane, session, generation, state, enrolled_at, approved_at)
			VALUES (?, ?, ?, ?, 1, 'approved', ?, ?)`, p.Name, p.Role, p.Identity.Pane, string(session), im.now, im.now); err != nil {
			return nil, importErr("party %s: %v", p.Name, err)
		}
		f := im.rt.Path("enrolled", p.Name)
		b, ok, e := im.read(f)
		if e != nil {
			return nil, e
		}
		if ok {
			if e := im.log(f, b, "parties"); e != nil {
				return nil, e
			}
		}
	}
	return peers, nil
}

// claimTokenOK accepts the two token forms of the route schema (D1): 32
// lowercase hex characters (the daemon's), or the file client's
// .claim.<digits> folder name, kept unchanged.
func claimTokenOK(s string) bool {
	if len(s) == 32 {
		return strings.Trim(s, "0123456789abcdef") == ""
	}
	digits, ok := strings.CutPrefix(s, ".claim.")
	return ok && len(s) <= 40 && digits != "" && strings.Trim(digits, "0123456789") == ""
}

// claim imports a mailbox's active reader claim; it returns the token
// ("" for none).
func (im *importer) claim(box string) (string, *APIError) {
	dir := im.rt.Path(box, "claim")
	if err := inbox.SourceDir(im.rt.Dir, dir); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", importErr("unsafe source: %v", err)
	}
	token, tb, tok, e := im.text(filepath.Join(dir, "token"))
	if e != nil {
		return "", e
	}
	owner, ob, ook, e := im.text(filepath.Join(dir, "owner"))
	if e != nil {
		return "", e
	}
	if !tok || !ook || owner == "" {
		return "", importErr("%s is not a reader claim (a token and an owner)", im.rel(dir))
	}
	if !claimTokenOK(token) {
		return "", importErr("%s: token %q is not a claim token (32 hex characters, or .claim.<digits>)", im.rel(dir), token)
	}
	if _, err := im.tx.Exec(`INSERT INTO claims(token, box, owner, read_key, state, created_at) VALUES (?, ?, ?, ?, 'active', ?)`,
		token, box, owner, "import-"+token, im.now); err != nil {
		return "", importErr("claim of %s: %v", box, err)
	}
	if e := im.log(filepath.Join(dir, "token"), tb, "claims"); e != nil {
		return "", e
	}
	return token, im.log(filepath.Join(dir, "owner"), ob, "claims")
}

// box imports one mailbox's ready/, cur/ and quarantine/ messages.
func (im *importer) box(name, token string) *APIError {
	for _, q := range []string{"ready", "cur", "quarantine"} {
		dir := im.rt.Path(name, q)
		names, e := im.names(dir)
		if e != nil {
			return e
		}
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
// ready/ and cur/ entries must be contract 1 envelopes (inbox.Envelope's
// rules); a quarantined entry that is none stays legacy evidence.
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
	raw, e := im.raw(p)
	if e != nil {
		return e
	}
	alloc := im.rt.Path("ids", idText, "envelope")
	ab, aok, e := im.read(alloc)
	if e != nil {
		return e
	}
	env, valid := inbox.EnvelopeBytes(raw, idText)
	if valid && env["to"] != box {
		valid = false
	}
	if !valid || (aok && !bytes.Equal(ab, raw)) {
		if queue != "quarantine" {
			if !valid {
				return importErr("%s is not a contract 1 envelope for %s", im.rel(p), box)
			}
			return importErr("%s differs from its queue file %s", im.rel(alloc), im.rel(p))
		}
		if !valid {
			return im.evidence(idText, "legacy evidence: quarantined, not an envelope", p, alloc, im.rt.Path("ids", idText, "terminal.json"))
		}
	}
	body, _ := inbox.String(env, "body")
	from, _ := inbox.String(env, "from")
	kind, _ := inbox.String(env, "kind")
	t, _ := inbox.Int(env, "time")
	var reply any
	if r, _ := inbox.String(env, "reply_to"); r != "" {
		reply, _ = messageID(r)
	}
	header := make(map[string]any, len(env))
	for k, v := range env {
		if k != "body" {
			header[k] = v
		}
	}
	hb, _ := json.Marshal(header)
	sum := sha256.Sum256([]byte(body))
	state := map[string]string{"ready": "ready", "cur": "cur", "quarantine": "quarantined"}[queue]
	ackPath := filepath.Join(im.rt.Path(box), "ack", idText+".md.json")
	ack, acked, e := im.read(ackPath)
	if e != nil {
		return e
	}
	acked = acked && queue == "cur"
	var claimTok any
	claimRec := im.rt.Path("ids", idText, "claim")
	held, cb, cok, e := im.text(claimRec)
	if e != nil {
		return e
	}
	switch {
	case acked:
		state = "handled"
		if e := im.checkAck(idText, ack, raw, held, cok); e != nil {
			return e
		}
	case cok && queue == "cur" && token != "" && held == token:
		claimTok = token
		if e := im.log(claimRec, cb, "claims binding"); e != nil {
			return e
		}
	case cok:
		if e := im.log(claimRec, cb, "legacy evidence: the claim of an earlier reader"); e != nil {
			return e
		}
	}
	if _, err := im.tx.Exec(`INSERT INTO messages(id, sender, recipient, kind, reply_to, envelope, body, body_sha256, state, claim_token, created_seq, created_at, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'imported')`,
		id, from, box, kind, reply, string(hb), []byte(body), hex.EncodeToString(sum[:]), state, claimTok, t); err != nil {
		return importErr("message %s: %v", idText, err)
	}
	if e := im.log(p, raw, "messages "+state); e != nil {
		return e
	}
	if aok {
		disposition := "messages " + state
		if !bytes.Equal(ab, raw) {
			disposition = "legacy evidence: allocation differs from the quarantined file"
		}
		if e := im.log(alloc, ab, disposition); e != nil {
			return e
		}
	}
	if acked {
		return im.ack(idText, id, ackPath, ack, cb)
	}
	if queue == "quarantine" {
		return im.terminal(idText, id)
	}
	return nil
}

// evidence logs the files of a message that is not imported.
func (im *importer) evidence(idText, disposition string, paths ...string) *APIError {
	for _, p := range paths {
		b, ok, e := im.read(p)
		if e != nil {
			return e
		}
		if ok {
			if e := im.log(p, b, disposition); e != nil {
				return e
			}
		}
	}
	return nil
}

// checkAck validates a present ack record as ack_valid does, with every
// typed field checked: its id, a claim equal to ids/<id>/claim, an
// integral time, a processing record and the checksum of the cur file.
func (im *importer) checkAck(idText string, b, cur []byte, held string, heldOK bool) *APIError {
	bad := func(why string) *APIError {
		return importErr("the ack of %s is present but invalid (%s); remove or repair it before adopting", idText, why)
	}
	if inbox.StrictJSON(b) != nil {
		return bad("not strict JSON")
	}
	v, _ := inbox.Decode(b)
	a := inbox.Object(v)
	claim, _ := inbox.String(a, "claim")
	t, tok := inbox.Int(a, "time")
	sum, _ := inbox.String(a, "envelope_checksum")
	switch {
	case a == nil || a["id"] != idText:
		return bad("id")
	case claim == "" || !heldOK || claim != held:
		return bad("claim, against ids/" + idText + "/claim")
	case !tok || t < 0:
		return bad("time")
	case !inbox.NonEmpty(a, "processing_record"):
		return bad("processing record")
	case sum != inbox.Digest(cur):
		return bad("envelope checksum")
	}
	return nil
}

// ack imports a checked ack/<id>.md.json byte for byte, with the handled
// terminal, ids/<id>/ack.json and the claim record it names.
func (im *importer) ack(idText string, id int64, p string, b, claimRec []byte) *APIError {
	v, _ := inbox.Decode(b)
	a := inbox.Object(v)
	claim, _ := inbox.String(a, "claim")
	t, _ := inbox.Int(a, "time")
	sum, _ := inbox.String(a, "envelope_checksum")
	if _, err := im.tx.Exec(`INSERT INTO acks(id, claim, time, record, envelope_checksum) VALUES (?, ?, ?, ?, ?)`, id, claim, t, b, sum); err != nil {
		return importErr("ack %s: %v", idText, err)
	}
	if e := im.log(p, b, "acks"); e != nil {
		return e
	}
	if e := im.log(im.rt.Path("ids", idText, "claim"), claimRec, "acks"); e != nil {
		return e
	}
	term := im.rt.Path("ids", idText, "terminal.json")
	if tb, ok, e := im.read(term); e != nil {
		return e
	} else if ok {
		if inbox.StrictJSON(tb) != nil {
			return importErr("%s is not strict JSON", im.rel(term))
		}
		tv, _ := inbox.Decode(tb)
		if o := inbox.Object(tv); o == nil || o["id"] != idText || o["state"] != "handled" {
			return importErr("%s does not record %s as handled", im.rel(term), idText)
		}
		if e := im.log(term, tb, "acks"); e != nil {
			return e
		}
	}
	return im.evidence(idText, "acks", im.rt.Path("ids", idText, "ack.json"))
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
	p := im.rt.Path("state", "sequence")
	text, b, ok, e := im.text(p)
	if e != nil {
		return e
	}
	if ok {
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil || v < 0 || v > 999999999999 {
			return importErr("state/sequence is not a message number")
		}
		n = v
		if e := im.log(p, b, "meta last_message_id"); e != nil {
			return e
		}
	}
	ids, e := im.names(im.rt.Path("ids"))
	if e != nil {
		return e
	}
	for _, name := range ids {
		if v, ok := messageID(name); ok && v > n {
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
