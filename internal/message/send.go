package message

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// objectKeys lists a JSON object's top-level keys in file order.
func objectKeys(b []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

// rawObject is a JSON object read in key order with raw values.
func rawObject(b []byte) (kv, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var o kv
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		k, _ := t.(string)
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return nil, false
		}
		o = append(o, struct {
			K string
			V any
		}{k, v})
	}
	return o, true
}

var seqRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})$`)

// Send is message-send.sh <from> <to> <body-file> <kind> [reply-to-id].
func (e *Env) Send(args []string) int { return e.finish(e.send(args)) }

func (e *Env) send(args []string) error {
	if len(args) != 4 && len(args) != 5 {
		return die("send <from> <to> <body-file> <kind> [reply-to-id]")
	}
	from, to, bodyFile, kind := args[0], args[1], args[2], args[3]
	reply := ""
	if len(args) == 5 {
		reply = args[4]
	}
	if !inbox.NameOK(from) || !inbox.NameOK(to) {
		return die("invalid name")
	}
	if !inbox.Bounded("", bodyFile) {
		return die("body must be a bounded readable regular file")
	}
	body, err := os.ReadFile(bodyFile)
	if err != nil || len(body) > inbox.MaxBody {
		return die("body must be a bounded readable regular file")
	}
	if !slices.Contains(inbox.Kinds, kind) {
		return die("invalid kind")
	}
	if reply != "" && !inbox.IDOK(reply) {
		return die("invalid reply id")
	}
	if err := e.open(false); err != nil {
		return err
	}
	me, err := e.selfPeer(from)
	if err != nil {
		return err
	}
	var mutation *inbox.Lock
	if kind == "side_agent_data" {
		// Hold the input lock from the boundary check through the enqueue;
		// lock order: input lock, then queue lock.
		var lerr error
		if mutation, lerr = e.tryMutation(); lerr != nil {
			return lerr
		} else if mutation == nil {
			return die("input lock busy; side-agent data not queued")
		}
		if ok, fault, unknown := e.ch.Boundary(e.ctx(), e.Client); !ok {
			if err := e.ch.RecordExposure(fault, unknown, true); err != nil {
				return err
			}
			return die("HUMAN boundary; side-agent data not queued")
		}
	}
	lock, err := e.lockChannel()
	if err != nil {
		return err
	}
	if err := e.commitIdentity(me); err != nil {
		return err
	}
	recipient, ok := e.ch.Peer(to)
	if !ok {
		return die("unknown peer")
	}
	if reply != "" && !inbox.Regular(e.ch.Dir, e.path("ids", reply, "envelope")) {
		return die("unknown reply id")
	}
	if err := e.ch.BoxInit(to); err != nil {
		return err
	}
	backlog, service, sourceCount := 0, 0, 0
	var source any
	if kind == "side_agent_data" {
		m, ok := decodeObject(string(body))
		src := inbox.Object(m["source"])
		if !ok || src == nil || len(src) != 2 || !hasKey(src, "pane") || !hasKey(src, "session") {
			return die("side-agent source identity required")
		}
		source = src
	}
	box := e.path(to)
	for _, d := range []string{"new", "ready", "cur", "quarantine"} {
		for _, f := range inbox.MdFiles(filepath.Join(box, d)) {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			if e.ch.AckValid(box, strings.TrimSuffix(filepath.Base(f), ".md")) {
				continue
			}
			var m map[string]any
			if inbox.Bounded(e.ch.Dir, f) {
				v, _ := decodeFile(f)
				m = inbox.Object(v)
			}
			if m != nil && m["kind"] == "side_agent_data" {
				service++
				if kind == "side_agent_data" {
					bs, _ := m["body"].(string)
					if bm, ok := decodeObject(bs); ok && inbox.Equal(bm["source"], source) {
						sourceCount++
					}
				}
			} else {
				backlog++
			}
		}
	}
	if kind == "side_agent_data" {
		if service >= 32 || sourceCount >= 8 {
			return die("side-agent budget full; human inspection required")
		}
	} else if backlog >= 1000 {
		return die("pending backlog full; inspect status")
	}
	seqFile := e.path("state", "sequence")
	sequence := "0"
	if st, err := os.Stat(seqFile); err == nil && st.Mode().IsRegular() {
		sequence = inbox.CatText(seqFile)
	}
	n, _ := strconv.ParseInt(sequence, 10, 64)
	if !seqRe.MatchString(sequence) || n >= 999999999999 {
		return die("invalid/exhausted sequence")
	}
	n++
	if err := e.ch.WriteText(seqFile, strconv.FormatInt(n, 10)); err != nil {
		return writeFailed(err)
	}
	id := fmt.Sprintf("m%012d", n)
	if err := os.Mkdir(e.path("ids", id), 0o700); err != nil {
		return die("duplicate allocation; reconcile sequence")
	}
	env := obj("version", 1, "id", id, "from", from, "to", to,
		"from_identity", identityObj(me.peer.Identity), "to_identity", identityObj(recipient.Identity),
		"time", e.unix(), "kind", kind, "reply_to", reply, "body", string(body))
	data := pretty(env)
	tmp, err := os.CreateTemp(filepath.Join(box, "tmp"), ".send.")
	if err != nil {
		return die("cannot stage the envelope")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		return die("cannot stage the envelope")
	}
	if !inbox.Bounded(e.ch.Dir, tmpName) {
		return die("encoded envelope too large or invalid; not queued")
	}
	// Atomic (A26, D12): a crash never leaves a torn allocation copy.
	if err := e.ch.Atomic(e.path("ids", id, "envelope"), data); err != nil {
		return die("cannot record the allocation copy")
	}
	if err := e.ch.MoveUnique(tmpName, filepath.Join(box, "new", id+".md")); err != nil {
		// Not queued: retire the allocation under the queue lock, so no
		// allocated envelope stays without a queue entry or a terminal
		// disposition (A26, D12).
		_ = e.ch.Atomic(e.path("ids", id, "terminal.json"), pretty(obj("id", id, "state", "abandoned")))
		return err
	}
	if err := e.release(lock); err != nil {
		return err
	}
	if mutation != nil {
		if err := e.release(mutation); err != nil {
			return err
		}
	}
	if err := e.reportMetadata(e.pane(), "herdsman-message-out", "message_out", &id, 600000); err != nil {
		if _, fatal := inbox.IsFatal(err); fatal {
			return err
		}
		fmt.Fprintf(e.Stderr, "WARNING ring failed; %s is queued, not delivered\n", id)
	}
	if _, err := e.health(); err != nil {
		return err
	}
	fmt.Fprintf(e.Stdout, "%s queued; delivery requires receipt or status confirmation\n", id)
	return nil
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func identityObj(id inbox.Identity) kv {
	return obj("pane", id.Pane, "session", sessionObj(id.Session))
}

// writeFailed turns a failed state write into the script's ending: a
// fatal guard stays fatal; an uncommitted write ends with exit 1, as a
// failing command under set -e.
func writeFailed(err error) error {
	if _, ok := inbox.IsFatal(err); ok {
		return err
	}
	return &exitErr{code: 1}
}
