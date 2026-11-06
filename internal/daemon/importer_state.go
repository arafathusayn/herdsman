package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The importer's route state (S14): holds, resume intents, alerts, the card journal, the
// watch and resume targets, and the contract 2 files kept as evidence.
// Each source file read gets its import_log row, as in the core.

// state imports the route state after the core.
func (im *importer) state() *APIError {
	for _, f := range []func() *APIError{im.deliveryHold, im.holds, im.resumeIntents, im.alerts, im.cards, im.targets, im.contract2} {
		if e := f(); e != nil {
			return e
		}
	}
	return nil
}

// mtime is a source file's modification time, the time of a record that
// keeps none of its own.
func mtime(p string) int64 {
	st, err := os.Lstat(p)
	if err != nil {
		return 0
	}
	return st.ModTime().Unix()
}

// holds imports state/hold-<target>: one row per owner, in the owner
// format of `message hold` ({"holds": {owner: {reason, time}}}).
func (im *importer) holds() *APIError {
	dir := im.rt.Path("state")
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	for _, n := range names {
		target, ok := strings.CutPrefix(n, "hold-")
		if !ok {
			continue
		}
		p := filepath.Join(dir, n)
		b, top, e := im.strict(p)
		if e != nil {
			return e
		}
		holds, ok := top["holds"].(map[string]any)
		if target == "" || len(top) != 1 || !ok {
			return importErr("%s is not in the owner format", im.rel(p))
		}
		for _, owner := range sortedKeys(holds) {
			h := inbox.Object(holds[owner])
			reason, rok := inbox.String(h, "reason")
			t, tok := inbox.Int(h, "time")
			if len(h) != 2 || !rok || !tok {
				return importErr("%s: the hold of %s is not {reason, time}", im.rel(p), owner)
			}
			if _, err := im.tx.Exec(`INSERT INTO holds(target, owner, reason, time) VALUES (?, ?, ?, ?)`, target, owner, reason, t); err != nil {
				return importErr("hold %s by %s: %v", target, owner, err)
			}
		}
		if e := im.log(p, b, "holds"); e != nil {
			return e
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// alertFile is one file of alerts/: its key and its suffix.
func alertFile(n string) (key, suffix string) {
	if k, _, ok := strings.Cut(n, ".queued-"); ok {
		return k, ".queued"
	}
	i := strings.LastIndex(n, ".")
	if i <= 0 {
		return "", ""
	}
	return n[:i], n[i:]
}

// alerts imports alerts/: per key, a payload ({title, body}) from
// .pending, else the first .queued-* incident, else .resolved; .sent
// means shown. The retry bookkeeping (.attempt, .failures, .lock) is the
// file channel's and is not imported; the daemon retries afresh.
func (im *importer) alerts() *APIError {
	dir := im.rt.Path("alerts")
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	type alert struct {
		payload []byte
		title   string
		rank    int // 3 pending, 2 queued, 1 resolved, 0 no payload
		sent    bool
		time    int64
	}
	byKey := map[string]*alert{}
	var keys []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		key, suffix := alertFile(n)
		if strings.HasPrefix(n, ".") || key == "" {
			if e := im.logAny(p, "partial-write"); e != nil {
				return e
			}
			continue
		}
		a := byKey[key]
		if a == nil {
			a = &alert{}
			byKey[key] = a
			keys = append(keys, key)
		}
		switch suffix {
		case ".pending", ".queued", ".resolved":
			b, o, e := im.strict(p)
			if e != nil {
				return e
			}
			title, tok := inbox.String(o, "title")
			_, bok := inbox.String(o, "body")
			if len(o) != 2 || !tok || !bok {
				return importErr("%s is not an alert payload {title, body}", im.rel(p))
			}
			// .pending wins over a queued incident, both over .resolved.
			if rank := map[string]int{".pending": 3, ".queued": 2, ".resolved": 1}[suffix]; rank > a.rank {
				a.payload, a.title, a.rank, a.time = b, title, rank, mtime(p)
			}
			if e := im.log(p, b, "alerts"); e != nil {
				return e
			}
		case ".sent":
			a.sent = true
			if e := im.logAny(p, "alerts shown"); e != nil {
				return e
			}
		default:
			if e := im.logAny(p, "not imported: alert retry state of the file channel"); e != nil {
				return e
			}
		}
	}
	for _, key := range keys {
		a := byKey[key]
		state := "pending"
		switch {
		case a.sent:
			state = "shown"
		case a.rank == 1:
			state = "resolved"
		case a.rank == 0:
			continue // retry bookkeeping only: nothing to show
		}
		if a.payload == nil {
			a.payload, a.title = []byte("{}"), key
		}
		if _, err := im.tx.Exec(`INSERT INTO alerts(key, title, payload, state, time) VALUES (?, ?, ?, ?, ?)`,
			key, a.title, string(a.payload), state, a.time); err != nil {
			return importErr("alert %s: %v", key, err)
		}
	}
	return nil
}

// logAny logs a file of any kind: a regular file by its bytes, a folder
// and each file under it (the SHA-256 of nothing for the folder itself).
func (im *importer) logAny(p, disposition string) *APIError {
	st, err := os.Lstat(p)
	if err != nil {
		return importErr("%s: %v", im.rel(p), err)
	}
	// raw refuses a link or any other file that is not regular.
	if st.IsDir() {
		if e := im.log(p, nil, disposition); e != nil {
			return e
		}
		names, e := im.names(p)
		if e != nil {
			return e
		}
		for _, n := range names {
			if e := im.logAny(filepath.Join(p, n), disposition); e != nil {
				return e
			}
		}
		return nil
	}
	b, e := im.raw(p)
	if e != nil {
		return e
	}
	return im.log(p, b, disposition)
}

// cards imports card-events/<id>.json (the card watcher's journal) with
// the relay recorded in state/card-<id>.queued.
func (im *importer) cards() *APIError {
	dir := im.rt.Path("card-events")
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		episode, ok := strings.CutSuffix(n, ".json")
		if !ok || strings.HasPrefix(n, ".") {
			if e := im.logAny(p, "partial-write"); e != nil {
				return e
			}
			continue
		}
		b, o, e := im.strict(p)
		if e != nil {
			return e
		}
		text, tok := inbox.String(o, "text")
		src := inbox.Object(o["source"])
		if o["id"] != episode || o["type"] != "side_agent_data" || !tok || src == nil {
			return importErr("%s is not a card record (id, type, source, text)", im.rel(p))
		}
		sb, _ := json.Marshal(src)
		var relayed any
		q := im.rt.Path("state", "card-"+episode+".queued")
		qb, qok, e := im.read(q)
		if e != nil {
			return e
		}
		if qok {
			if f := strings.Fields(string(qb)); len(f) > 0 {
				if id, ok := messageID(f[0]); ok {
					relayed = id
				}
			}
			if e := im.log(q, qb, "cards relayed"); e != nil {
				return e
			}
		}
		if _, err := im.tx.Exec(`INSERT INTO cards(episode, source, headline, relayed_id, time) VALUES (?, ?, ?, ?, ?)`,
			episode, string(sb), text, relayed, mtime(p)); err != nil {
			return importErr("card %s: %v", episode, err)
		}
		if e := im.log(p, b, "cards"); e != nil {
			return e
		}
	}
	return nil
}

// targets imports watch-targets.json (a list of {pane, session}, keyed by
// pane) and resume-targets.json (an object by name), each entry as its
// compact strict JSON.
func (im *importer) targets() *APIError {
	for _, kind := range []string{"watch", "resume"} {
		p := im.rt.Path(kind + "-targets.json")
		b, ok, e := im.read(p)
		if e != nil {
			return e
		}
		if !ok {
			continue
		}
		if inbox.StrictJSON(b) != nil {
			return importErr("%s is not strict JSON", im.rel(p))
		}
		v, _ := inbox.Decode(b)
		entries := map[string]any{}
		if kind == "watch" {
			list, ok := v.([]any)
			if !ok {
				return importErr("%s is not a list", im.rel(p))
			}
			for _, x := range list {
				pane, _ := inbox.String(inbox.Object(x), "pane")
				if pane == "" || entries[pane] != nil {
					return importErr("%s: an entry has no pane, or a pane twice", im.rel(p))
				}
				entries[pane] = x
			}
		} else if entries = inbox.Object(v); entries == nil {
			return importErr("%s is not an object", im.rel(p))
		}
		for _, name := range sortedKeys(entries) {
			if inbox.Object(entries[name]) == nil {
				return importErr("%s: target %s is not an object", im.rel(p), name)
			}
			var id bytes.Buffer
			eb, _ := json.Marshal(entries[name])
			_ = json.Compact(&id, eb)
			if _, err := im.tx.Exec(`INSERT INTO targets(kind, name, identity) VALUES (?, ?, ?)`, kind, name, id.String()); err != nil {
				return importErr("%s target %s: %v", kind, name, err)
			}
		}
		if e := im.log(p, b, "targets"); e != nil {
			return e
		}
	}
	return nil
}

// contract2 imports a contract 2 inbox's state/approval.json as an
// approvals evidence row (approval) and logs its other daemon files, which
// stay legacy evidence: events and checkpoints end with the old epoch,
// owner.json is written again at publish, the credential hashes are not
// imported and the secrets are reissued at the next approval, and the
// stale latch has no version 3 proof to mark (the route is HUMAN until
// setup writes one).
func (im *importer) contract2() *APIError {
	if e := im.approval(); e != nil {
		return e
	}
	for _, f := range []struct{ name, disposition string }{
		{"owner.json", "not imported: owner.json is written again at publish"},
		{"events", "not imported: a new epoch starts"},
		{"checkpoints", "not imported: a new epoch starts"},
		{"credentials.json", "legacy evidence: the credential hashes; secrets are reissued at the next approval"},
		{"credentials", "not imported: secrets are reissued at the next approval"},
		{"proof-stale", "legacy evidence: the route is HUMAN until setup writes version 3"},
	} {
		p := im.rt.Path("state", f.name)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		if e := im.logAny(p, f.disposition); e != nil {
			return e
		}
	}
	return nil
}

// approval imports a contract 2 state/approval.json as an approvals
// evidence row (approved_by 'import', D1): the registry checksum, the
// interaction and the answer it gave. Its credentials are not imported;
// an imported route gets them at its first pair approval (D1).
func (im *importer) approval() *APIError {
	p := im.rt.Path("state", "approval.json")
	b, ok, e := im.read(p)
	if e != nil || !ok {
		return e
	}
	if inbox.StrictJSON(b) != nil {
		return importErr("%s is not strict JSON", im.rel(p))
	}
	v, _ := inbox.Decode(b)
	a := inbox.Object(v)
	sum, _ := inbox.String(a, "sha256")
	interaction, iok := inbox.String(a, "interaction")
	creds, cok := a["credentials"].([]any)
	revoked, rok := a["revoked"].([]any)
	if len(a) != 4 || len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" || !iok || !cok || !rok {
		return importErr("%s is not an approval record (sha256, interaction, credentials, revoked)", im.rel(p))
	}
	// Typed, strictly: a credential is its name and option only, so no
	// credential value of any shape is ever copied into the evidence.
	var rec approvalRecord
	if strictDecode(b, &rec) != nil || len(creds) != len(rec.Credentials) || len(revoked) != len(rec.Revoked) {
		return importErr("%s is not an approval record (credentials of name and option, revoked names)", im.rel(p))
	}
	res := ApproveResult{Approved: true, Credentials: []ApprovedCredential{}, Revoked: []string{}}
	for _, c := range rec.Credentials {
		if !inbox.NameOK(c.Name) || !slices.Contains([]string{"identity", "file", "keychain"}, c.Option) {
			return importErr("%s: credential %q with option %q is not a party name and a credential option", im.rel(p), c.Name, c.Option)
		}
		res.Credentials = append(res.Credentials, ApprovedCredential{Name: c.Name, Option: c.Option})
	}
	for _, n := range rec.Revoked {
		if !inbox.NameOK(n) {
			return importErr("%s: revoked %q is not a party name", im.rel(p), n)
		}
		res.Revoked = append(res.Revoked, n)
	}
	result, _ := json.Marshal(res)
	request := sha256.Sum256(b)
	if _, err := im.tx.Exec(`INSERT INTO approvals(seq, registry_sha256, request_sha256, interaction, approved_by, result, time)
		VALUES (0, ?, ?, ?, 'import', ?, ?)`, sum, "import-"+hex.EncodeToString(request[:]), interaction, string(result), mtime(p)); err != nil {
		return importErr("approval: %v", err)
	}
	return im.log(p, b, "approvals (evidence)")
}

// deliveryHold imports state/route-paused and state/route-frozen as
// meta.delivery_hold, so the route comes out still paused or frozen
// (N-P1-5). With both, frozen (the stronger hold) wins, and each file's
// import_log row names both.
func (im *importer) deliveryHold() *APIError {
	files := map[string][]byte{}
	for _, h := range []string{"paused", "frozen"} {
		b, ok, e := im.read(im.rt.Path("state", "route-"+h))
		if e != nil {
			return e
		}
		if ok {
			files[h] = b
		}
	}
	hold := ""
	switch {
	case files["frozen"] != nil:
		hold = "frozen"
	case files["paused"] != nil:
		hold = "paused"
	default:
		return nil
	}
	if _, err := im.tx.Exec(`INSERT INTO meta(k, v) VALUES ('delivery_hold', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, hold); err != nil {
		return importErr("meta delivery_hold: %v", err)
	}
	disposition := "meta delivery_hold " + hold
	if len(files) == 2 {
		disposition = "meta delivery_hold frozen (route-paused and route-frozen both present; frozen wins)"
	}
	for h, b := range files {
		if e := im.log(im.rt.Path("state", "route-"+h), b, disposition); e != nil {
			return e
		}
	}
	return nil
}
