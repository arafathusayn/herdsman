package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The importer's route state (S14): holds, alerts, the card journal, the
// watch and resume targets, and the contract 2 files kept as evidence.
// Each source file read gets its import_log row, as in the core.

// state imports the route state after the core.
func (im *importer) state() *APIError {
	for _, f := range []func() *APIError{im.holds, im.alerts, im.cards, im.targets, im.contract2} {
		if e := f(); e != nil {
			return e
		}
	}
	return nil
}

// names lists a folder under the inbox, sorted; a link on the way is
// refused, a missing folder is empty.
func (im *importer) names(dir string) ([]string, *APIError) {
	if !inbox.NoLinks(im.rt.Dir, dir) {
		return nil, importErr("%s is a link", im.rel(dir))
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, importErr("%s: %v", im.rel(dir), err)
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// raw reads a bounded regular file under the inbox, never a link.
func (im *importer) raw(p string) ([]byte, *APIError) {
	if !inbox.Regular(im.rt.Dir, p) {
		return nil, importErr("%s is not a regular file under the inbox", im.rel(p))
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, importErr("%s: %v", im.rel(p), err)
	}
	return b, nil
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
		if q := im.rt.Path("state", "card-"+episode+".queued"); inbox.Regular(im.rt.Dir, q) {
			qb, e := im.raw(q)
			if e != nil {
				return e
			}
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
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		b, ok := inbox.ReadStrict(im.rt.Dir, p)
		if !ok {
			return importErr("%s is not a strict JSON file under the inbox", im.rel(p))
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

// contract2 logs a contract 2 inbox's daemon files, which stay legacy
// evidence: events and checkpoints end with the old epoch, owner.json is
// written again at publish, the approval and credential records wait for
// an imported approvals row (handoff), the secrets are reissued at the
// next approval, and the stale latch has no version 3 proof to mark (the
// route is HUMAN until setup writes one).
func (im *importer) contract2() *APIError {
	for _, f := range []struct{ name, disposition string }{
		{"owner.json", "not imported: owner.json is written again at publish"},
		{"events", "not imported: a new epoch starts"},
		{"checkpoints", "not imported: a new epoch starts"},
		{"approval.json", "legacy evidence: the contract 2 approval"},
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
