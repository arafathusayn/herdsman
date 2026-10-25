package message

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Read is message-read.sh <me> [claim-token] [--release]: claim the ready
// messages and print manifests, never bodies; or release a fully
// acknowledged claim.
func (e *Env) Read(args []string) int { return e.finish(e.read(args)) }

func (e *Env) read(args []string) error {
	if len(args) < 1 || len(args) > 3 || !inbox.NameOK(args[0]) {
		return die("read <me> [claim-token] [--release]")
	}
	me := args[0]
	token, action := "", ""
	if len(args) > 1 {
		token = args[1]
	}
	if len(args) > 2 {
		action = args[2]
	}
	if action != "" && action != "--release" {
		return die("unknown read option")
	}
	if err := e.open(false); err != nil {
		return err
	}
	s, err := e.selfPeer(me)
	if err != nil {
		return err
	}
	if _, err := e.lockChannel(); err != nil {
		return err
	}
	if err := e.commitIdentity(s); err != nil {
		return err
	}
	if err := e.ch.BoxInit(me); err != nil {
		return err
	}
	r := e.ch
	box := e.path(me)
	claim := filepath.Join(box, "claim")
	if !inbox.NoLinks(claim) {
		return die("claim symlink")
	}
	if st, err := os.Stat(claim); err == nil && st.IsDir() {
		if !r.ClaimValid(box) || token == "" || token != inbox.CatText(filepath.Join(claim, "token")) {
			return die("reader claim held; recover with the recorded token, do not start another reader")
		}
	} else {
		if token != "" || action != "" {
			return die("no such active claim")
		}
		stage, err := os.MkdirTemp(filepath.Join(box, "tmp"), ".claim.")
		if err != nil {
			return die("claim publication failed")
		}
		token = filepath.Base(stage)
		if err := r.WriteText(filepath.Join(stage, "token"), token); err != nil {
			return writeFailed(err)
		}
		if err := r.WriteText(filepath.Join(stage, "owner"), e.pane()+":"+strconv.Itoa(os.Getpid())); err != nil {
			return writeFailed(err)
		}
		if !inbox.Exists(claim) {
			_ = os.Rename(stage, claim)
		}
		if inbox.Exists(stage) || !inbox.Regular(r.Dir, filepath.Join(claim, "token")) {
			return die("claim publication failed")
		}
	}
	if action == "--release" {
		if len(r.Unacked(box)) > 0 {
			return die("acknowledge all delivered work before releasing claim")
		}
		released, err := os.MkdirTemp(filepath.Join(box, "tmp"), ".released.")
		if err != nil {
			return die("claim release failed")
		}
		_ = os.Remove(released)
		_ = os.Rename(claim, released)
		if inbox.Exists(claim) {
			return die("claim release failed")
		}
		_ = os.Remove(filepath.Join(released, "token"))
		_ = os.Remove(filepath.Join(released, "owner"))
		_ = os.Remove(released)
		fmt.Fprintln(e.Stdout, `{"type":"released"}`)
		return nil
	}
	sameRecipient := func(m map[string]any) bool {
		return inbox.Equal(m["to_identity"], toAny(identityObjMap(s.peer.Identity)))
	}
	for _, f := range inbox.MdFiles(filepath.Join(box, "ready")) {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".md")
		m, ok := inbox.Envelope(r.Dir, f)
		if !ok {
			return die("invalid ready envelope")
		}
		if inbox.Exists(e.path("ids", id, "terminal.json")) {
			return die("terminal message replay held; reconcile archive")
		}
		if !sameRecipient(m) {
			return die("recipient generation mismatch")
		}
		if err := r.WriteText(e.path("ids", id, "claim"), token); err != nil {
			return writeFailed(err)
		}
		if err := r.MoveUnique(f, filepath.Join(box, "cur", filepath.Base(f))); err != nil {
			return err
		}
	}
	e.Stdout.Write(line(obj("type", "claim", "token", token)))
	for _, f := range inbox.MdFiles(filepath.Join(box, "cur")) {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(f), ".md")
		if r.AckValid(box, id) {
			continue
		}
		if inbox.Exists(e.path("ids", id, "terminal.json")) {
			return die("terminal cur replay held; reconcile archive")
		}
		m, ok := inbox.Envelope(r.Dir, f)
		if !ok {
			return die("invalid cur envelope")
		}
		if !sameRecipient(m) {
			return die("recipient generation mismatch")
		}
		if err := r.WriteText(e.path("ids", id, "claim"), token); err != nil {
			return writeFailed(err)
		}
		e.Stdout.Write(line(obj("type", "manifest", "id", m["id"], "from", m["from"], "to", m["to"], "kind", m["kind"],
			"time", m["time"], "reply_to", m["reply_to"], "path", f, "claim", token, "authority", "data-only")))
	}
	e.Stdout.Write(line(obj("type", "summary", "pending_new", inbox.CountState(filepath.Join(box, "new")),
		"quarantine", inbox.CountState(filepath.Join(box, "quarantine")))))
	return nil
}

func identityObjMap(id inbox.Identity) map[string]any {
	return map[string]any{"pane": id.Pane, "session": map[string]any{"agent": id.Session.Agent, "kind": id.Session.Kind,
		"source": id.Session.Source, "value": id.Session.Value}}
}

// Ack is message-ack.sh <me> <claim-token> <id> <processing-record-file>.
func (e *Env) Ack(args []string) int { return e.finish(e.ack(args)) }

func (e *Env) ack(args []string) error {
	if len(args) != 4 || !inbox.NameOK(args[0]) || !inbox.IDOK(args[2]) {
		return die("ack <me> <claim-token> <id> <processing-record-file>")
	}
	me, token, id, record := args[0], args[1], args[2], args[3]
	// Guard first (regular, not a link, bounded), then read at most the
	// bound: a FIFO or a huge file is refused without being read.
	if !inbox.Bounded("", record) {
		return die("nonempty processing record required")
	}
	rec, err := inbox.ReadBounded(record, inbox.MaxSize)
	if err != nil || len(rec) == 0 {
		return die("nonempty processing record required")
	}
	if err := e.open(false); err != nil {
		return err
	}
	s, err := e.selfPeer(me)
	if err != nil {
		return err
	}
	if _, err := e.lockChannel(); err != nil {
		return err
	}
	if err := e.commitIdentity(s); err != nil {
		return err
	}
	if err := e.ch.BoxInit(me); err != nil {
		return err
	}
	r := e.ch
	box := e.path(me)
	if !r.ClaimValid(box) || inbox.CatText(filepath.Join(box, "claim", "token")) != token {
		return die("wrong active reader claim")
	}
	cur := filepath.Join(box, "cur", id+".md")
	m, ok := inbox.Envelope(r.Dir, cur)
	if !ok {
		return die("no claimed envelope")
	}
	if !inbox.Equal(m["to_identity"], toAny(identityObjMap(s.peer.Identity))) {
		return die("recipient generation mismatch")
	}
	terminal := e.path("ids", id, "terminal.json")
	sum, _ := inbox.DigestFile(cur)
	want := toAny(map[string]any{"id": id, "state": "handled", "envelope_checksum": sum})
	terminalDone := false
	if inbox.Exists(terminal) {
		b, ok := inbox.ReadStrict(r.Dir, terminal)
		v, _ := inbox.Decode(b)
		if !ok || !inbox.Equal(v, want) {
			return die("conflicting or invalid terminal disposition held for inspection; not overwritten")
		}
		terminalDone = true
	}
	if !r.AckValid(box, id) {
		ack := pretty(obj("id", id, "claim", token, "time", e.unix(), "processing_record", string(rec), "envelope_checksum", sum))
		// The record is JSON-escaped into the ACK: a bounded record can
		// still encode past the bound, and an ACK over it never validates.
		// Refuse it before anything is written.
		if len(ack) > inbox.MaxSize {
			return die("processing record too large once encoded; message remains unhandled")
		}
		if err := r.Atomic(filepath.Join(box, "ack", id+".md.json"), ack); err != nil {
			return writeFailed(err)
		}
		if !r.AckValid(box, id) {
			return die("ack validation failed; message remains unhandled")
		}
	}
	if !terminalDone {
		if err := r.Atomic(terminal, pretty(obj("id", id, "state", "handled", "envelope_checksum", sum))); err != nil {
			return writeFailed(err)
		}
	}
	fmt.Fprintf(e.Stdout, "acknowledged %s\n", id)
	return nil
}
