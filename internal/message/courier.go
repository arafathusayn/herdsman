package message

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/shnum"
)

// Courier is herdsman message courier: it moves each verified message from new
// to ready and each failed one to quarantine (with a permanent disposition
// and a sender notice), reports the recipients' counts, alerts on
// incidents, keeps the heartbeat, and sweeps on event hints with SWEEP
// seconds as the safety net, for RUN_FOR seconds (0: until stopped).
func (e *Env) Courier(args []string) int { return e.finish(e.courier(args)) }

const proofStaleBody = "A pinned policy or protected script changed. The courier moves nothing until the human repeats setup; it then resumes on its next sweep."

type courier struct {
	*Env
	sweepSecs  int64
	alertAfter int64
}

func (e *Env) courier(args []string) error {
	if len(args) != 0 {
		return die("usage: herdsman message courier")
	}
	sweep, err := e.durEnv("SWEEP", "60", 1, 3600)
	if err != nil {
		return err
	}
	alertAfter, err := e.durEnv("ALERT_AFTER", "600", 1, 86400)
	if err != nil {
		return err
	}
	runFor, err := e.durEnv("RUN_FOR", "0", 0, 86400)
	if err != nil {
		return err
	}
	if err := e.open(false); err != nil {
		return err
	}
	if e.ch.HealthAfter <= sweep+10 {
		return die("HEALTH_AFTER must exceed SWEEP plus scan allowance")
	}
	if l, _ := e.take(e.path("courier.lock"), 1, false, nil); l == nil {
		return die("courier lock exists; inspect owner and heartbeat")
	}
	if err := e.resolveAlert("setup-proof-stale"); err != nil {
		return err
	}
	if err := e.ch.Atomic(e.path("state", "timing.json"), pretty(obj("sweep", sweep, "health", e.ch.HealthAfter))); err != nil {
		return writeFailed(err)
	}
	c := &courier{Env: e, sweepSecs: int64(sweep), alertAfter: int64(alertAfter)}
	if err := e.ch.LogLine("courier start"); err != nil {
		return err
	}
	if err := c.sweep(); err != nil {
		return err
	}
	started := e.unix()
	next := started + int64(sweep)
	var stream Stream
	var backoff reopen
	retryAt := int64(0)
	defer func() {
		if stream != nil {
			stream.Close()
		}
	}()
	subs := []map[string]any{{"type": "pane.updated"}, {"type": "pane.closed"}, {"type": "pane.exited"}}
	for {
		t := e.unix()
		swept := false
		if runFor != 0 && t-started >= int64(runFor) {
			break
		}
		if t >= next {
			if err := c.sweep(); err != nil {
				return err
			}
			swept = true
			next = e.unix() + int64(sweep)
		}
		if stream == nil && t < retryAt {
			// No subscription yet: sweep on schedule until the next try.
			until := min(next, retryAt)
			if runFor != 0 {
				until = min(until, started+int64(runFor))
			}
			if until <= t {
				until = t + 1
			}
			e.sleep(time.Duration(until-t) * time.Second)
			continue
		}
		if stream == nil {
			s, err := e.openStream(subs)
			if err != nil {
				retryAt = t + int64(backoff.next()/time.Second)
				continue
			}
			stream = s
			if !swept {
				if err := c.sweep(); err != nil {
					return err
				}
			}
			next = e.unix() + int64(sweep)
		}
		t = e.unix()
		waitFor := next - t
		if waitFor <= 0 {
			continue
		}
		if runFor != 0 && waitFor > int64(runFor)-(t-started) {
			waitFor = int64(runFor) - (t - started)
		}
		if waitFor <= 0 {
			continue
		}
		ev, ok := e.nextHint(&stream, waitFor, &backoff)
		backoff.after(e.unix(), &retryAt)
		if !ok {
			continue
		}
		switch {
		case strings.Contains(ev, "message_out"):
			pane, ring := eventPane(ev)
			if inbox.IDOK(ring) {
				key := inbox.Digest([]byte(pane + ":" + ring))
				if st, err := os.Stat(e.path("state", "ring-"+key)); err != nil || !st.Mode().IsRegular() {
					if err := c.sweep(); err != nil {
						return err
					}
					if err := e.ch.WriteText(e.path("state", "ring-"+key), ring); err != nil {
						return writeFailed(err)
					}
				}
			}
		case strings.Contains(ev, "pane_closed"), strings.Contains(ev, "pane_exited"),
			strings.Contains(ev, "pane.closed"), strings.Contains(ev, "pane.exited"):
			if err := c.sweep(); err != nil {
				return err
			}
		}
	}
	return e.ch.LogLine("courier stop")
}

// quarantineBody is the notice of a quarantined message.
const quarantineBody = "An envelope failed checks. Show headers only; resend with a fresh ID."

// processEntry verifies one new entry and moves it, as process_entry run in
// its own subshell: 0 done, 75 the queue lock was busy, 2 the entry is held
// (a die); an alert follows from the caller.
func (c *courier) processEntry(f, name string) int {
	sub := c.child()
	code := c.processEntryIn(sub, f, name)
	sub.releaseAll()
	return code
}

func (c *courier) processEntryIn(e *Env, f, name string) int {
	r := e.ch
	fail := func(err error) int {
		if msg, ok := inbox.IsFatal(err); ok {
			fmt.Fprintln(e.Stderr, msg)
			return 2
		}
		if x, ok := err.(*exitErr); ok {
			return x.code
		}
		return 1
	}
	box := e.path(name)
	if !inbox.Bounded(r.Dir, f) {
		return fail(die("suspicious queue entry held"))
	}
	id := strings.TrimSuffix(filepath.Base(f), ".md")
	// The bytes the checks below verify: delivery happens only if the queue
	// entry still holds exactly these bytes under channel.lock (A26).
	snap, _ := inbox.ReadBounded(f, inbox.MaxSize)
	target, ok := r.Peer(name)
	if !ok {
		return fail(die("orphaned mailbox"))
	}
	rc := 0
	from := ""
	m, ok := inbox.Envelope(r.Dir, f)
	if !ok {
		rc = 3
	} else {
		from, _ = m["from"].(string)
		expected, ok := r.Peer(from)
		reply, _ := m["reply_to"].(string)
		switch {
		case !ok:
			rc = 3
		case m["to"] != name || !inbox.Equal(m["from_identity"], toAny(identityObjMap(expected.Identity))) ||
			!inbox.Equal(m["to_identity"], toAny(identityObjMap(target.Identity))):
			rc = 3
		case reply != "" && !inbox.Regular(r.Dir, e.path("ids", reply, "envelope")):
			rc = 3
		case !inbox.Regular(r.Dir, e.path("ids", id, "envelope")) || !sameBytes(f, e.path("ids", id, "envelope")):
			rc = 3
		default:
			fi := inbox.Object(m["from_identity"])
			pane, _ := fi["pane"].(string)
			observed, code, note := inbox.LiveIdentity(e.ctx(), e.Client, pane)
			if note != "" {
				fmt.Fprintln(e.Stderr, note)
			}
			switch {
			case code != inbox.LiveOK:
				rc = code
			case !observed.Same(expected.Identity):
				rc = 3
			}
		}
	}
	if e.tryChannel() == nil {
		return 75
	}
	if _, ok := r.Registry(); !ok {
		return fail(die("registry changed during verification"))
	}
	if !e.ch.ProofValid() {
		e.releaseAll()
		if err := c.proofStaleAlert(e); err != nil {
			return fail(err)
		}
		return 0
	}
	if !inbox.Exists(f) {
		return 0
	}
	if inbox.Exists(e.path("ids", id, "terminal.json")) || inbox.Exists(filepath.Join(box, "ready", id+".md")) ||
		inbox.Exists(filepath.Join(box, "cur", id+".md")) || inbox.Exists(filepath.Join(box, "quarantine", id+".md")) {
		e.releaseAll()
		if err := e.notify("duplicate-"+name+"-"+id, "Duplicate message held", "Preserve both copies; reconcile terminal disposition before archive."); err != nil {
			return fail(err)
		}
		return 0
	}
	if rc == 0 {
		now, err1 := inbox.ReadBounded(f, inbox.MaxSize)
		alloc, err2 := inbox.ReadBounded(e.path("ids", id, "envelope"), inbox.MaxSize)
		switch {
		case err1 != nil || err2 != nil || !bytes.Equal(now, alloc):
			rc = 3 // changed against its allocation copy while verified
		case !bytes.Equal(now, snap):
			e.releaseAll() // replaced by an equal copy: verify it again next sweep
			return 0
		}
	}
	switch rc {
	case 0:
		if err := r.MoveUnique(f, filepath.Join(box, "ready", id+".md")); err != nil {
			return fail(err)
		}
		if err := r.LogLine("VERIFIED " + id + " for " + name); err != nil {
			return fail(err)
		}
	case 3:
		if err := r.MoveUnique(f, filepath.Join(box, "quarantine", id+".md")); err != nil {
			return fail(err)
		}
		if inbox.IDOK(id) && isDir(e.path("ids", id)) {
			if err := r.Atomic(e.path("ids", id, "terminal.json"), pretty(obj("id", id, "state", "quarantined"))); err != nil {
				return fail(die("terminal record failed"))
			}
		}
		if err := r.LogLine("QUARANTINED " + id + " for " + name); err != nil {
			return fail(err)
		}
		if _, ok := r.Peer(from); inbox.NameOK(from) && ok {
			if err := r.BoxInit(from); err != nil {
				return fail(err)
			}
			notice := pretty(obj("id", id, "to", name, "state", "quarantined", "authority", "notice-only"))
			if err := r.Atomic(e.path(from, "notices", id+".md.json"), notice); err != nil {
				return fail(die("sender notice failed"))
			}
			if inbox.IDOK(id) && isDir(e.path("ids", id)) {
				if err := r.SafeTouch(e.path("ids", id, "notice-published")); err != nil {
					return fail(err)
				}
			}
		}
		e.releaseAll()
		if err := e.notify("quarantine-"+name+"-"+id, "Messages quarantined", quarantineBody); err != nil {
			return fail(err)
		}
		return 0
	default:
		e.releaseAll()
		if err := e.notify("verification-"+name+"-"+id, "Message verification unavailable", "Retrying queued work; no permanent rejection on transient CLI errors."); err != nil {
			return fail(err)
		}
		return 0
	}
	e.releaseAll()
	if err := e.resolveAlert("verification-" + name + "-" + id); err != nil {
		return fail(err)
	}
	return 0
}

func isDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

func sameBytes(a, b string) bool {
	x, err1 := inbox.ReadFile(a)
	y, err2 := inbox.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func (c *courier) proofStaleAlert(e *Env) error {
	return e.notify("setup-proof-stale", "Message setup proof stale", proofStaleBody)
}

// recoverQuarantine repairs a quarantine whose terminal or sender notice
// is missing, in its own subshell: a failure alerts and other work goes on.
func (c *courier) recoverQuarantine(f, name string) error {
	e := c.child()
	defer e.releaseAll()
	r := e.ch
	id := strings.TrimSuffix(filepath.Base(f), ".md")
	if !inbox.IDOK(id) || !isDir(e.path("ids", id)) || !inbox.NoLinks(e.path("ids", id)) {
		return nil
	}
	from := ""
	if v, err := decodeFile(f); err == nil {
		if s, ok := inbox.Object(v)["from"].(string); ok {
			from = s
		}
	}
	_, peerOK := r.Peer(from)
	noticeDue := inbox.NameOK(from) && peerOK && !inbox.Exists(e.path("ids", id, "notice-published"))
	terminal := e.path("ids", id, "terminal.json")
	terminalOK := false
	if inbox.Exists(terminal) {
		b, ok := inbox.ReadStrict(r.Dir, terminal)
		v, _ := inbox.Decode(b)
		m := inbox.Object(v)
		if !ok || m["id"] != id || m["state"] != "quarantined" {
			return e.notify("terminal-conflict-"+name+"-"+id, "Quarantine terminal conflict", "Hold original bytes and inspect the permanent disposition.")
		}
		terminalOK = true
	}
	if terminalOK && !noticeDue {
		return e.notify("quarantine-"+name+"-"+id, "Messages quarantined", quarantineBody)
	}
	if e.tryChannel() == nil {
		return nil
	}
	if !e.ch.ProofValid() {
		e.releaseAll()
		return c.proofStaleAlert(e)
	}
	if !terminalOK {
		if inbox.Exists(terminal) {
			return nil
		}
		if err := r.Atomic(terminal, pretty(obj("id", id, "state", "quarantined"))); err != nil {
			return die("quarantine recovery failed")
		}
	}
	if noticeDue {
		if err := r.BoxInit(from); err != nil {
			return err
		}
		if err := r.Atomic(e.path(from, "notices", id+".md.json"), pretty(obj("id", id, "to", name, "state", "quarantined", "authority", "notice-only"))); err != nil {
			return die("notice recovery failed")
		}
		if err := r.SafeTouch(e.path("ids", id, "notice-published")); err != nil {
			return err
		}
	}
	e.releaseAll()
	return e.notify("quarantine-"+name+"-"+id, "Messages quarantined", quarantineBody)
}

// sweep is one pass over every mailbox, as the script's sweep.
func (c *courier) sweep() error {
	e := c.Env
	r := e.ch
	if err := r.SafeTouch(e.path("state", "heartbeat")); err != nil {
		return err
	}
	e.retryAlerts()
	// Recheck the setup proof before each sweep's record changes: a stale
	// proof pauses ring pruning and allocation recovery with delivery.
	if !e.ch.ProofValid() {
		r.ProofStale = true
		return c.proofStaleAlert(e)
	}
	if r.ProofStale {
		r.ProofStale = false
		if err := e.resolveAlert("setup-proof-stale"); err != nil {
			return err
		}
	}
	if err := c.pruneRings(); err != nil {
		return err
	}
	if err := c.retireAbandoned(); err != nil {
		return err
	}
	if _, ok := r.Registry(); !ok {
		baseline, key := "unavailable", "unavailable"
		if inbox.Regular(r.Dir, e.path("peers.approved")) {
			baseline, _ = inbox.DigestFile(e.path("peers.approved"))
		}
		if inbox.Regular(r.Dir, e.path("peers")) {
			key, _ = inbox.DigestFile(e.path("peers"))
		}
		summary := registrySummary(e.path("peers.approved"), e.path("peers"))
		return e.notify("peers-"+key, "Message peers changed", "Held: approved "+baseline+", pending "+key+". Old then new prefixes: "+summary)
	}
	old := false
	fp := ""
	invalidBox := false
	g403, err := glob(r.Dir, "*")
	if err != nil {
		return err
	}
	for _, box := range g403 {
		if !isDirFollow(filepath.Join(box, "new")) {
			continue
		}
		if err := r.SafeTouch(e.path("state", "heartbeat")); err != nil {
			return err
		}
		name := filepath.Base(box)
		if !inbox.NoLinks(box) || !inbox.NameOK(name) {
			invalidBox = true
			if err := e.notify("invalid-box", "Suspicious queue entry", "Invalid mailbox held; inspect without opening its body."); err != nil {
				return err
			}
			continue
		}
		if _, ok := r.Peer(name); !ok {
			if err := e.notify("orphan-"+name, "Orphaned message mailbox", "Reconcile or archive pending work; no automatic handover."); err != nil {
				return err
			}
			continue
		}
		if err := e.resolveAlert("orphan-" + name); err != nil {
			return err
		}
		if err := r.BoxInit(name); err != nil {
			if err := e.notify("box-"+name, "Suspicious queue entry", "Mailbox state held."); err != nil {
				return err
			}
			continue
		}
		if err := e.resolveAlert("box-" + name); err != nil {
			return err
		}
		for _, f := range inbox.MdFiles(filepath.Join(box, "quarantine")) {
			if !inbox.Regular(r.Dir, f) {
				continue
			}
			if err := c.recoverQuarantine(f, name); err != nil {
				if msg, ok := inbox.IsFatal(err); ok {
					fmt.Fprintln(e.Stderr, msg)
				}
				if err := e.notify("quarantine-recovery-"+name+"-"+strings.TrimSuffix(filepath.Base(f), ".md"), "Quarantine recovery held", "Inspect this disposition; unrelated work continues."); err != nil {
					return err
				}
			}
		}
		for _, f := range inbox.MdFiles(filepath.Join(box, "new")) {
			if err := r.SafeTouch(e.path("state", "heartbeat")); err != nil {
				return err
			}
			key := inbox.Digest([]byte(f))
			switch c.processEntry(f, name) {
			case 0:
				if err := e.resolveAlert("entry-" + key); err != nil {
					return err
				}
			case 75:
				if err := r.LogLine("DEFERRED queue lock " + name); err != nil {
					return err
				}
			default:
				_ = e.notify("entry-"+key, "Suspicious queue entry", "Entry held after a transition or verification error; unrelated work continues.")
			}
		}
		count := len(r.Unacked(box))
		p, _ := r.Peer(name)
		observed, code, note := inbox.LiveIdentity(e.ctx(), e.Client, p.Identity.Pane)
		if note != "" {
			fmt.Fprintln(e.Stderr, note)
		}
		if code == inbox.LiveOK && observed.Same(p.Identity) {
			if err := e.resolveAlert("identity-" + name); err != nil {
				return err
			}
			state := e.path("state", "count-"+name)
			previous, stamp := "", int64(0)
			if inbox.Regular(r.Dir, state) {
				previous, stamp = readCount(inbox.CatText(state))
			}
			t := e.unix()
			if previous != strconv.Itoa(count) || t-stamp >= c.sweepSecs {
				var err error
				if count > 0 {
					v := strconv.Itoa(count)
					err = e.reportMetadata(p.Identity.Pane, "herdsman-message-count", "messages", &v, int(c.sweepSecs)*3000)
				} else {
					err = e.reportMetadata(p.Identity.Pane, "herdsman-message-count", "messages", nil, 0)
				}
				if _, fatal := inbox.IsFatal(err); fatal {
					return err
				}
				if err == nil {
					if err := r.WriteText(state, fmt.Sprintf("%d %d", count, t)); err != nil {
						return writeFailed(err)
					}
				}
			}
		} else if err := e.notify("identity-"+name, "Message peer identity unavailable", "Reconcile pane/session; pending work stays bound to its generation."); err != nil {
			return err
		}
		for _, d := range []string{"new", "ready", "cur"} {
			for _, f := range inbox.MdFiles(filepath.Join(box, d)) {
				if !inbox.Regular(r.Dir, f) || r.AckValid(box, strings.TrimSuffix(filepath.Base(f), ".md")) {
					continue
				}
				stamp, err := inbox.Mtime(f)
				if err != nil {
					continue
				}
				if e.unix()-stamp >= c.alertAfter {
					old = true
					fp += " " + name + "/" + filepath.Base(f)
				}
			}
		}
		partial := false
		if ents, _ := os.ReadDir(filepath.Join(box, "tmp")); len(ents) > 0 {
			partial = true
			if err := e.notify("partial-"+name, "Interrupted message write", "Inspect incomplete state before removal."); err != nil {
				return err
			}
		}
		if !partial {
			if err := e.resolveAlert("partial-" + name); err != nil {
				return err
			}
		}
	}
	if !invalidBox {
		if err := e.resolveAlert("invalid-box"); err != nil {
			return err
		}
	}
	if old {
		if err := e.notify("unread-"+inbox.Digest([]byte(fp)), "Unread messages", "Messages exceeded the configured age; includes new and unacknowledged cur."); err != nil {
			return err
		}
	}
	return r.SafeTouch(e.path("state", "heartbeat"))
}

func isDirFollow(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// readCount is `read -r previous stamp < count-<name>`; the stamp is a
// plain integer the courier wrote (shnum.Test, no arithmetic evaluator,
// A27): an empty or bad stamp counts as 0 (a resend, never a crash).
func readCount(s string) (string, int64) {
	f := strings.Fields(strings.SplitN(s, "\n", 2)[0])
	prev, stamp := "", ""
	if len(f) > 0 {
		prev = f[0]
	}
	if len(f) > 1 {
		stamp = strings.Join(f[1:], " ")
	}
	n, ok := shnum.Test(stamp)
	if !ok {
		n = 0
	}
	return prev, n
}

// registrySummary is the courier's session prefixes of the approved then
// the pending registry, at most 300 bytes; "unavailable" when either
// cannot be read.
func registrySummary(paths ...string) string {
	var out []byte
	for _, p := range paths {
		b, err := inbox.ReadFile(p)
		if err != nil {
			return "unavailable"
		}
		o, ok := rawObject(b)
		if !ok {
			return "unavailable"
		}
		arr := []any{}
		for _, x := range o {
			v, _ := inbox.Decode(x.V.(json.RawMessage))
			var session any
			if val, ok := inbox.Object(inbox.Object(inbox.Object(v)["identity"])["session"])["value"].(string); ok {
				rs := []rune(val)
				if len(rs) > 8 {
					rs = rs[:8]
				}
				session = string(rs)
			}
			arr = append(arr, obj("name", x.K, "session", session))
		}
		out = append(out, line(arr)...)
	}
	if len(out) > 300 {
		out = out[:300]
	}
	return strings.TrimRight(string(out), "\n")
}

// ringMaxAge is how long a state/ring-<key> marker is kept. A marker only
// stops a repeated ring event from sweeping again; the sweep itself
// delivers by scanning, so an old marker has no further use.
const ringMaxAge = 3600

// pruneRings removes ring markers older than ringMaxAge (A26, D8: the
// script kept one marker per ring forever).
func (c *courier) pruneRings() error {
	e := c.Env
	rings, err := glob(e.path("state"), "ring-*")
	if err != nil {
		return err
	}
	now := e.unix()
	for _, p := range rings {
		if !inbox.RegularFile(p) {
			continue
		}
		if stamp, err := inbox.Mtime(p); err == nil && now-stamp > ringMaxAge {
			_ = os.Remove(p)
		}
	}
	return nil
}
