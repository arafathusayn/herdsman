package message

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

const holdUsage = "hold: <me> hold <name> <reason> | <me> unhold <name>"

// Hold is message-hold.sh <me> hold <name> <reason> | <me> unhold <name>:
// a named hold, one entry per owner, under the shared input lock.
func (e *Env) Hold(args []string) int { return e.finish(e.holdCmd(args)) }

func (e *Env) holdCmd(args []string) error {
	ok := false
	if len(args) >= 2 {
		switch args[1] {
		case "hold":
			ok = len(args) == 4
		case "unhold":
			ok = len(args) == 3
		}
	}
	if !ok || !inbox.NameOK(args[0]) || !inbox.NameOK(args[2]) {
		return die(holdUsage)
	}
	me, action, name := args[0], args[1], args[2]
	reason := ""
	if len(args) == 4 {
		reason = args[3]
	}
	if utf8.RuneCountInString(reason) > 300 {
		return die("hold reason longer than 300 characters")
	}
	if err := e.open(false); err != nil {
		return err
	}
	s, err := e.selfPeer(me)
	if err != nil {
		return err
	}
	if _, ok := e.ch.Peer(name); !ok {
		b, sok := inbox.ReadStrict(e.ch.Dir, e.path("resume-targets.json"))
		v, _ := inbox.Decode(b)
		m := inbox.Object(v)
		if !sok || m == nil || !hasKey(m, name) {
			return die("unknown hold target: %s", name)
		}
	}
	lock := e.tryMutation()
	if lock == nil {
		return die("input lock busy; retry with backoff, never auto-remove")
	}
	if err := e.commitIdentity(s); err != nil {
		return err
	}
	file := e.path("state", "hold-"+name)
	if err := inbox.SafeLeaf(e.ch.Dir, file); err != nil {
		return die("%s", err.Error())
	}
	holds := kv{}
	if inbox.Exists(file) {
		b, ok := inbox.ReadStrict(e.ch.Dir, file)
		if !ok || !holdFormat(b) {
			return die("hold record is not in the owner format; human inspection required")
		}
		top, _ := rawObject(b)
		holds, _ = rawObject(top[0].V.(json.RawMessage))
	}
	put := func(o kv, k string, v any) kv {
		for i := range o {
			if o[i].K == k {
				o[i].V = v
				return o
			}
		}
		return append(o, struct {
			K string
			V any
		}{k, v})
	}
	if action == "hold" {
		holds = put(holds, me, obj("reason", reason, "time", e.unix()))
		if err := e.ch.Atomic(file, pretty(obj("holds", holds))); err != nil {
			return writeFailed(err)
		}
		fmt.Fprintf(e.Stdout, "HOLD %s by %s\n", name, me)
	} else {
		found := -1
		for i := range holds {
			if holds[i].K == me {
				found = i
			}
		}
		if found < 0 {
			return die("no hold by %s for %s; another owner's hold stays", me, name)
		}
		holds = append(holds[:found], holds[found+1:]...)
		if len(holds) == 0 {
			_ = os.Remove(file)
		} else if err := e.ch.Atomic(file, pretty(obj("holds", holds))); err != nil {
			return writeFailed(err)
		}
		var owners []string
		for _, h := range holds {
			owners = append(owners, h.K)
		}
		sort.Strings(owners)
		if len(owners) > 0 {
			fmt.Fprintf(e.Stdout, "UNHOLD %s by %s; still held by: %s\n", name, me, strings.Join(owners, " "))
		} else {
			fmt.Fprintf(e.Stdout, "UNHOLD %s by %s; no hold remains\n", name, me)
		}
	}
	return e.release(lock)
}

// holdFormat checks the owner format: {"holds":{<owner>:{"reason":string,"time":number}}}.
func holdFormat(b []byte) bool {
	v, _ := inbox.Decode(b)
	m := inbox.Object(v)
	if m == nil || len(m) != 1 {
		return false
	}
	h, ok := m["holds"].(map[string]any)
	if !ok {
		return false
	}
	for _, x := range h {
		o := inbox.Object(x)
		if o == nil || len(o) != 2 {
			return false
		}
		if _, ok := o["reason"].(string); !ok {
			return false
		}
		if _, ok := inbox.Int(o, "time"); !ok {
			if _, isNum := o["time"].(interface{ Float64() (float64, error) }); !isNum {
				return false
			}
		}
	}
	return true
}

// holdOwners are the owners of a hold record, sorted, as `.holds|keys`.
func holdOwners(b []byte) (string, bool) {
	v, _ := inbox.Decode(b)
	h, ok := inbox.Object(v)["holds"].(map[string]any)
	if !ok {
		return "", false
	}
	var keys []string
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " "), true
}
