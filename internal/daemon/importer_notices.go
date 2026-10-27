package daemon

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// notices imports <box>/notices/<id>.md.json, the sender notices of the
// file-mode courier, for the mailbox's own party.
func (im *importer) notices(box string) *APIError {
	dir := im.rt.Path(box, "notices")
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		idText, ok := strings.CutSuffix(n, ".md.json")
		id, idOK := messageID(idText)
		if !ok || !idOK {
			if e := im.logAny(p, "partial-write"); e != nil {
				return e
			}
			continue
		}
		b, o, e := im.strict(p)
		if e != nil {
			return e
		}
		state, sok := inbox.String(o, "state")
		authority, _ := inbox.String(o, "authority")
		if o["id"] != idText || !sok {
			return importErr("%s is not a notice of %s", im.rel(p), idText)
		}
		if _, err := im.tx.Exec(`INSERT INTO notices(id, recipient, state, authority, time) VALUES (?, ?, ?, ?, ?)`,
			id, box, state, authority, mtime(p)); err != nil {
			return importErr("notice %s (its message must be imported): %v", idText, err)
		}
		if e := im.log(p, b, "notices"); e != nil {
			return e
		}
		if q := im.rt.Path("ids", idText, "notice-published"); inbox.Regular(im.rt.Dir, q) {
			if e := im.log(q, nil, "notices"); e != nil {
				return e
			}
		}
	}
	return nil
}

// resumeIntents imports the resume forms of state/: resume-<t>.sent (the
// sent time; never sent again), resume-held-<t> (held for the human) and
// resume-<t> (a pending intent). When .sent and the pending form exist
// together, .sent wins and the pending file is logged as superseded.
// The read-back resume-<t>.readback stays legacy evidence.
func (im *importer) resumeIntents() *APIError {
	dir := im.rt.Path("state")
	names, e := im.names(dir)
	if e != nil {
		return e
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, n := range names {
		p := filepath.Join(dir, n)
		rest, ok := strings.CutPrefix(n, "resume-")
		if !ok {
			continue
		}
		var target, state string
		var t int64
		switch {
		case strings.HasSuffix(rest, ".readback"):
			if e := im.logAny(p, "legacy evidence: resume read-back"); e != nil {
				return e
			}
			continue
		case strings.HasSuffix(rest, ".sent"):
			target, state = strings.TrimSuffix(rest, ".sent"), "sent"
		case strings.HasPrefix(rest, "held-"):
			target, state = strings.TrimPrefix(rest, "held-"), "held"
		default:
			target, state = rest, "pending"
			if have["resume-"+target+".sent"] {
				if e := im.logAny(p, "superseded by .sent"); e != nil {
					return e
				}
				continue
			}
		}
		b, e := im.raw(p)
		if e != nil {
			return e
		}
		text := strings.TrimSpace(string(b))
		t = mtime(p)
		if state == "sent" {
			// "<unix time> intent /goal resume; restore tab <tab>"
			f, _, _ := strings.Cut(text, " ")
			n, err := strconv.ParseInt(f, 10, 64)
			if err != nil || n <= 0 {
				return importErr("%s does not start with its sent time", im.rel(p))
			}
			t = n
		}
		if target == "" {
			return importErr("%s names no target", im.rel(p))
		}
		intent, _ := json.Marshal(map[string]string{"state": state, "text": text})
		if _, err := im.tx.Exec(`INSERT INTO resume_intents(target, intent, time) VALUES (?, ?, ?)`, target, string(intent), t); err != nil {
			return importErr("resume intent %s: %v", target, err)
		}
		if e := im.log(p, b, "resume_intents "+state); e != nil {
			return e
		}
	}
	return nil
}
