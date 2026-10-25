package message

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// self is self_peer's result: the sender's approved entry and the
// registry's digest at observation.
type self struct {
	peer inbox.Peer
	sum  string
}

// selfPeer is self_peer: the registry approved, the name enrolled, and this
// pane's live identity the enrolled one.
func (e *Env) selfPeer(name string) (self, error) {
	if _, ok := e.ch.Registry(); !ok {
		return self{}, die("registry change awaiting human approval")
	}
	p, ok := e.ch.Peer(name)
	if !ok {
		return self{}, die("unknown peer")
	}
	sum, _ := inbox.DigestFile(e.path("peers.approved"))
	if e.pane() == "" {
		return self{}, die("HERDR_PANE_ID: set own pane\nown identity unavailable")
	}
	id, code, note := inbox.LiveIdentity(e.ctx(), e.Client, e.pane())
	if code != inbox.LiveOK {
		if note != "" {
			return self{}, die("%s\nown identity unavailable", note)
		}
		return self{}, die("own identity unavailable")
	}
	if !id.Same(p.Identity) {
		return self{}, die("own pane/session changed; enroll a new generation")
	}
	return self{peer: p, sum: sum}, nil
}

// commitIdentity is commit_identity: the registry unchanged since
// selfPeer.
func (e *Env) commitIdentity(s self) error {
	if _, ok := e.ch.Registry(); ok {
		if sum, _ := inbox.DigestFile(e.path("peers.approved")); sum == s.sum {
			return nil
		}
	}
	return die("registry changed during observation; retry")
}

// alert paths.
func (e *Env) alert(key, ext string) string { return e.path("alerts", key+ext) }

// alertLock is alert_lock: the incident's own lock, or nil when another
// process owns it.
func (e *Env) alertLock(key string) *inbox.Lock {
	return e.hold(inbox.TryDir(e.alert(key, ".lock"), 1, nil))
}

// notify records an incident and shows it once, as notify does: an
// incident already sent stays quiet; a new one is recorded pending and
// attempted.
func (e *Env) notify(key, title, body string) error {
	l := e.alertLock(key)
	if l == nil {
		return nil
	}
	defer e.release(l)
	for _, x := range []string{".sent", ".pending"} {
		if err := inbox.SafeLeaf(e.ch.Dir, e.alert(key, x)); err != nil {
			return die("%s", err.Error())
		}
	}
	if inbox.RegularFile(e.alert(key, ".sent")) {
		return nil
	}
	if !inbox.RegularFile(e.alert(key, ".pending")) {
		if err := e.ch.Atomic(e.alert(key, ".pending"), pretty(obj("title", title, "body", body))); err != nil {
			return err
		}
	}
	return e.alertAttemptLocked(key)
}

// alertAttempt is alert_attempt: one attempt under the incident's lock.
func (e *Env) alertAttempt(key string) error {
	l := e.alertLock(key)
	if l == nil {
		return nil
	}
	defer e.release(l)
	return e.alertAttemptLocked(key)
}

// alertAttemptLocked is alert_attempt_locked: a pending incident shown
// after its backoff (5·2^failures seconds, at most 300); a failure counts
// and logs.
func (e *Env) alertAttemptLocked(key string) error {
	stamp := e.alert(key, ".attempt")
	if err := inbox.SafeLeaf(e.ch.Dir, stamp); err != nil {
		return die("%s", err.Error())
	}
	b, ok := inbox.ReadStrict(e.ch.Dir, e.alert(key, ".pending"))
	if !ok {
		return nil
	}
	failures := 0
	if inbox.Regular(e.ch.Dir, e.alert(key, ".failures")) {
		n, err := inbox.Uint("alert-failures", inbox.CatText(e.alert(key, ".failures")), 0, 10)
		if err != nil {
			return die("%s", err.Error())
		}
		failures = n
	} else if _, err := inbox.Uint("alert-failures", "0", 0, 10); err != nil {
		return die("%s", err.Error())
	}
	delay := int64(5) << failures
	if delay > 300 {
		delay = 300
	}
	if t, err := inbox.Mtime(stamp); err == nil && e.unix()-t < delay {
		return nil
	}
	if err := e.ch.SafeTouch(stamp); err != nil {
		return err
	}
	v, _ := inbox.Decode(b)
	m := inbox.Object(v)
	title, tok := inbox.String(m, "title")
	body, bok := inbox.String(m, "body")
	if !tok || !bok {
		return errAlertPayload
	}
	if e.showNotification(title, body) {
		if err := e.ch.SafeTouch(e.alert(key, ".sent")); err != nil {
			return err
		}
		_ = os.Remove(e.alert(key, ".pending"))
		_ = os.Remove(e.alert(key, ".failures"))
		return nil
	}
	if failures < 10 {
		failures++
	}
	if err := e.ch.WriteText(e.alert(key, ".failures"), strconv.Itoa(failures)); err != nil {
		return err
	}
	return e.ch.LogLine("ALERT PENDING " + key)
}

var errAlertPayload = &exitErr{code: 1}

// retryAlerts is retry_alerts: each pending incident attempted.
func (e *Env) retryAlerts() {
	ents, _ := os.ReadDir(e.path("alerts"))
	for _, ent := range ents {
		n := ent.Name()
		if strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".pending") {
			continue
		}
		if err := e.alertAttempt(strings.TrimSuffix(n, ".pending")); err != nil {
			e.Stderr.Write([]byte("HEALTH alert retry failed\n"))
		}
	}
}

// resolveAlert is resolve_alert: retries end; the payload is kept as
// .resolved history.
func (e *Env) resolveAlert(key string) error {
	l := e.alertLock(key)
	if l == nil {
		return nil
	}
	defer e.release(l)
	if inbox.Regular(e.ch.Dir, e.alert(key, ".pending")) {
		if err := inbox.SafeLeaf(e.ch.Dir, e.alert(key, ".resolved")); err != nil {
			return die("%s", err.Error())
		}
		_ = os.Rename(e.alert(key, ".pending"), e.alert(key, ".resolved"))
	}
	for _, x := range []string{".sent", ".attempt", ".failures"} {
		_ = os.Remove(e.alert(key, x))
	}
	return nil
}

// healthSnapshot is health_snapshot: the flags of the file-only health
// check, in the scripts' order.
func (e *Env) healthSnapshot() []string {
	var flags []string
	r := e.ch
	now := e.unix()
	for _, role := range []string{"courier", "watch"} {
		if role == "watch" && e.envDefault("WATCH_REQUIRED", "1") != "1" {
			continue
		}
		stamp := r.Path("state", "heartbeat")
		if role == "watch" {
			stamp = r.Path("state", "watch-heartbeat")
		}
		age := int64(999999)
		if inbox.Regular(r.Dir, stamp) {
			if t, err := inbox.Mtime(stamp); err == nil {
				age = now - t
			}
		}
		if age > int64(r.HealthAfter) {
			flags = append(flags, role+"-down")
		}
	}
	for _, role := range []string{"herdr-mutation", "channel"} {
		p := r.Path(role + ".lock")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if t, err := inbox.Mtime(p); err == nil && now-t > int64(r.HealthAfter) {
				if role == "herdr-mutation" {
					role = "mutation"
				}
				flags = append(flags, role+"-lock-stale")
			}
		}
	}
	locks, _ := filepath.Glob(filepath.Join(r.Path("alerts"), "*.lock"))
	for _, p := range locks {
		if strings.HasPrefix(filepath.Base(p), ".") {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if t, err := inbox.Mtime(p); err == nil && now-t > int64(r.HealthAfter) {
				flags = append(flags, "alert-lock-stale")
				break
			}
		}
	}
	if r.ProofStale {
		flags = append(flags, "setup-proof-stale")
	}
	if r.TimingStale {
		flags = append(flags, "timing-invalid")
	}
	if inbox.Exists(r.Path("state", "automation-held")) {
		flags = append(flags, "automation-held")
	}
	return flags
}

// health is health: courier and watcher outages alerted or resolved, and
// every flag printed to stderr; false when any flag is up.
func (e *Env) health() (bool, error) {
	flags := e.healthSnapshot()
	has := func(f string) bool {
		for _, x := range flags {
			if x == f {
				return true
			}
		}
		return false
	}
	for _, role := range []string{"courier", "watch"} {
		var err error
		if has(role + "-down") {
			err = e.notify(role+"-down", "Message "+role+" unavailable", "Inspect heartbeat and queues; restart only under the recorded grant.")
		} else {
			err = e.resolveAlert(role + "-down")
		}
		if err != nil {
			if _, fatal := inbox.IsFatal(err); fatal {
				return false, err
			}
		}
	}
	if len(flags) > 0 {
		e.Stderr.Write([]byte("HEALTH " + strings.Join(flags, "\n") + "\n"))
		return false, nil
	}
	return true, nil
}
