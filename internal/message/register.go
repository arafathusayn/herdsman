package message

import (
	"fmt"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Register is message-register.sh propose <name> adjutant|orchestrator |
// approve <checksum> | retire <name> <checksum>. Approve and retire are
// human-run registry administration, never a message action.
func (e *Env) Register(args []string) int { return e.finish(e.register(args)) }

func (e *Env) register(args []string) error {
	if len(args) < 1 {
		return die("usage: herdsman message register propose|approve|retire ...")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "propose":
		if len(rest) != 2 || !inbox.NameOK(rest[0]) {
			return die("propose <name> adjutant|orchestrator")
		}
		if rest[1] != "adjutant" && rest[1] != "orchestrator" {
			return die("invalid role")
		}
	case "approve":
		if len(rest) != 1 || !inbox.ChecksumShape(rest[0]) {
			return die("approve <inspected-checksum>")
		}
	case "retire":
		if len(rest) != 2 || !inbox.NameOK(rest[0]) || !inbox.ChecksumShape(rest[1]) {
			return die("retire <name> <inspected-checksum>")
		}
	default:
		return die("unknown registry action")
	}
	if err := e.open(false); err != nil {
		return err
	}
	var identity inbox.Identity
	if action == "propose" {
		if e.pane() == "" {
			return die("HERDR_PANE_ID: set own pane\ncannot enroll current session")
		}
		id, code, note := inbox.LiveIdentity(e.ctx(), e.Client, e.pane())
		if code != inbox.LiveOK {
			if note != "" {
				fmt.Fprintln(e.Stderr, note)
			}
			return die("cannot enroll current session")
		}
		identity = id
	}
	if _, err := e.lockChannel(); err != nil {
		return err
	}
	r := e.ch
	peers, approved := e.path("peers"), e.path("peers.approved")
	for _, p := range []string{peers, approved} {
		if !inbox.Exists(p) {
			if err := r.WriteText(p, "{}"); err != nil {
				return writeFailed(err)
			}
		}
	}
	pb, ok := inbox.ReadStrict(r.Dir, peers)
	if _, valid := inbox.ValidRegistry(pb); !ok || !valid {
		return die("invalid pending registry; inspect without committing it")
	}
	sum, _ := inbox.DigestFile(peers)
	switch action {
	case "propose":
		name, role := rest[0], rest[1]
		if inbox.Exists(e.path("enrolled", name+".retired")) {
			return die("retired name; use a fresh generation")
		}
		candidate := obj("role", role, "identity", identityObj(identity))
		if enrolled := e.path("enrolled", name+".json"); inbox.Exists(enrolled) {
			v, err := decodeFile(enrolled)
			if err != nil || !inbox.Equal(v, toAny(candidate)) {
				return die("name is immutable; use a fresh generation name")
			}
		}
		current, _ := inbox.Decode(pb)
		for k, x := range inbox.Object(current) {
			if k == name {
				continue
			}
			p := inbox.Object(x)
			pid := inbox.Object(p["identity"])
			if p["role"] == role || pid["pane"] == identity.Pane || inbox.Equal(pid["session"], toAny(sessionObj(identity.Session))) {
				return die("role/session already enrolled")
			}
		}
		c, _ := compact(candidate)
		if err := r.WriteText(e.path("enrolled", name+".json"), string(c)); err != nil {
			return writeFailed(err)
		}
		o, _ := rawObject(pb)
		replaced := false
		for i := range o {
			if o[i].K == name {
				o[i].V = candidate
				replaced = true
			}
		}
		if !replaced {
			o = append(o, struct {
				K string
				V any
			}{name, candidate})
		}
		if err := r.Atomic(peers, pretty(o)); err != nil {
			return writeFailed(err)
		}
		if err := r.BoxInit(name); err != nil {
			return err
		}
		d, _ := inbox.DigestFile(peers)
		fmt.Fprintf(e.Stdout, "pending registry %s\n", d)
	case "approve":
		if rest[0] != sum {
			return die("approve requires the inspected pending checksum")
		}
		ps, _ := inbox.ValidRegistry(pb)
		roles := map[string]bool{}
		for _, p := range ps {
			roles[p.Role] = true
		}
		if len(ps) != 2 || !roles["adjutant"] || !roles["orchestrator"] {
			return die("approve exactly one complete pair")
		}
		if err := r.Atomic(approved, pb); err != nil {
			return writeFailed(err)
		}
		fmt.Fprintln(e.Stdout, "registry approved; this grants no action authorization")
	case "retire":
		if rest[1] != sum {
			return die("retire <name> <inspected-checksum>")
		}
		if _, ok := r.Peer(rest[0]); !ok {
			return die("unknown retirement name")
		}
		if err := r.WriteText(e.path("enrolled", rest[0]+".retired"), "retired"); err != nil {
			return writeFailed(err)
		}
		o, _ := rawObject(pb)
		var kept kv
		for _, x := range o {
			if x.K != rest[0] {
				kept = append(kept, x)
			}
		}
		if kept == nil {
			kept = kv{}
		}
		if err := r.Atomic(peers, pretty(kept)); err != nil {
			return writeFailed(err)
		}
		fmt.Fprintln(e.Stdout, "retired; old mailbox remains for human reconciliation")
	}
	return nil
}
