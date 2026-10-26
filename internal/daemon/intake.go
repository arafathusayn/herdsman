package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Intake of legacy new/ entries (12.2 step 6, 4.4). A file-mode sender
// wrote <to>/new/<id>.md before adoption, and the courier would have
// verified it; the daemon verifies it the same way and records the
// disposition: "verified" moves it to ready/ and publishes the message,
// "quarantined" moves it to quarantine/, writes its terminal record and
// the sender's notice. An entry whose sender cannot be observed now stays
// in new/ and is tried again later; a duplicate stays for the human.

// intakeRetry spaces intake attempts while an entry waits.
const intakeRetry = 30 * time.Second

// intakeEntries lists the new/ entries of every mailbox, in id order.
func (r *Route) intakeEntries() [][2]string {
	var out [][2]string
	for _, box := range r.boxes() {
		ents, err := os.ReadDir(r.path(box, "new"))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if id, ok := strings.CutSuffix(e.Name(), ".md"); ok && inbox.IDOK(id) {
				out = append(out, [2]string{box, id})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][1] < out[j][1] })
	return out
}

// intake verifies and disposes of every legacy new/ entry; it reports
// whether one still waits.
func (r *Route) intake() bool {
	r.intakeAt = r.now()
	waiting := false
	for _, e := range r.intakeEntries() {
		switch state := r.verifyEntry(e[0], e[1]); state {
		case "verified", "quarantined":
			if err := r.dispose(e[0], e[1], state); err != nil {
				r.d.logf("route %s: intake of %s: %v", r.name, e[1], err)
				waiting = true
			}
		case "wait":
			waiting = true
		}
	}
	r.intakeDue = waiting
	return waiting
}

// verifyEntry checks one new/ entry as the courier did: "verified",
// "quarantined", "wait" (the sender's pane cannot be observed now), or
// "held" (a duplicate or an entry of an unknown mailbox, left for the
// human).
func (r *Route) verifyEntry(box, id string) string {
	f := r.path(box, "new", id+".md")
	if !inbox.Bounded(r.inbox, f) {
		r.d.logf("route %s: suspicious queue entry %s held", r.name, f)
		return "held"
	}
	if exists(r.path("ids", id, "terminal.json")) || r.inQueues(box, id, "ready", "cur", "quarantine") {
		r.d.logf("route %s: duplicate message %s in %s held; preserve both copies", r.name, id, box)
		return "held"
	}
	target, ok := r.root().Peer(box)
	if !ok {
		r.d.logf("route %s: %s/new/%s.md is in a mailbox with no approved party; held", r.name, box, id)
		return "held"
	}
	m, ok := inbox.Envelope(r.inbox, f)
	if !ok {
		return "quarantined"
	}
	from, _ := m["from"].(string)
	reply, _ := m["reply_to"].(string)
	sender, ok := r.root().Peer(from)
	snap, err1 := readSmall(f, inbox.MaxSize)
	alloc, err2 := readSmall(r.path("ids", id, "envelope"), inbox.MaxSize)
	switch {
	case !ok, m["id"] != id, m["to"] != box,
		!inbox.Equal(m["from_identity"], decoded(identityObj(sender.Identity))),
		!inbox.Equal(m["to_identity"], decoded(identityObj(target.Identity))),
		reply != "" && !regular(r.path("ids", reply, "envelope")),
		err1 != nil || err2 != nil || !bytes.Equal(snap, alloc):
		return "quarantined"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, code, note := inbox.LiveIdentity(ctx, r.d.herdrFor(r.name), sender.Identity.Pane)
	switch {
	case code == inbox.LiveGone:
		return "quarantined"
	case code != inbox.LiveOK:
		if note != "" {
			r.d.logf("route %s: intake of %s waits: %s", r.name, id, note)
		}
		return "wait"
	case !observed.Same(sender.Identity):
		return "quarantined"
	}
	return "verified"
}

// decoded is an ordered object as the generic decoded value.
func decoded(o ordered) any {
	v, _ := inbox.Decode(mustEncode(o))
	return v
}

// inQueues reports whether a mailbox holds id in one of the queues.
func (r *Route) inQueues(box, id string, queues ...string) bool {
	for _, q := range queues {
		if exists(r.path(box, q, id+".md")) {
			return true
		}
	}
	return false
}

// dispose commits an entry's disposition and applies it.
func (r *Route) dispose(box, id, state string) error {
	sender := ""
	if b, err := readSmall(r.path(box, "new", id+".md"), inbox.MaxSize); err == nil {
		v, _ := inbox.Decode(b)
		sender, _ = inbox.String(inbox.Object(v), "from")
	}
	if !inbox.NameOK(sender) {
		sender = ""
	}
	rec := &Record{Op: "intake", ID: id, To: box, From: sender, State: state}
	if state == "verified" {
		env, _ := inbox.Envelope(r.inbox, r.path(box, "new", id+".md"))
		kind, _ := env["kind"].(string)
		reply, _ := env["reply_to"].(string)
		rec.Kind, rec.ReplyTo = kind, reply
		rec.Event = &Event{Type: "message", ID: id, From: sender, To: box, Kind: kind, ReplyTo: &reply}
	} else if sender != "" {
		rec.Event = &Event{Type: "notice", ID: id, To: sender, State: "quarantined"}
	}
	if err := r.commit(rec); err != nil {
		return err
	}
	if err := r.redoIntake(rec); err != nil {
		_ = r.unfinish(rec, err)
		return err
	}
	r.publish(rec)
	return nil
}

// redoIntake applies a recorded disposition, idempotently.
func (r *Route) redoIntake(rec *Record) error {
	src := r.path(rec.To, "new", rec.ID+".md")
	if rec.State == "verified" {
		if exists(src) && !exists(r.path(rec.To, "ready", rec.ID+".md")) {
			if err := mkdirs(r.inbox, rec.To+"/ready"); err != nil {
				return err
			}
			return renameDurable(src, r.path(rec.To, "ready", rec.ID+".md"))
		}
		return nil
	}
	if exists(src) && !exists(r.path(rec.To, "quarantine", rec.ID+".md")) {
		if err := mkdirs(r.inbox, rec.To+"/quarantine"); err != nil {
			return err
		}
		if err := renameDurable(src, r.path(rec.To, "quarantine", rec.ID+".md")); err != nil {
			return err
		}
	}
	if inbox.IDOK(rec.ID) && regular(r.path("ids", rec.ID, "envelope")) && !exists(r.path("ids", rec.ID, "terminal.json")) {
		if err := writeDurable(r.path("ids", rec.ID, "terminal.json"), prettyJSON(obj("id", rec.ID, "state", "quarantined")), 0o600); err != nil {
			return err
		}
	}
	if rec.From != "" {
		if err := r.boxInit(rec.From); err != nil {
			return err
		}
		p := filepath.Join(r.path(rec.From, "notices"), rec.ID+".md.json")
		if !exists(p) {
			notice := prettyJSON(obj("id", rec.ID, "to", rec.To, "state", "quarantined", "authority", "notice-only"))
			if err := writeDurable(p, notice, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}
