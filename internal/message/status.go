package message

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// decodeObject decodes a JSON object (nil, false otherwise).
func decodeObject(s string) (map[string]any, bool) {
	v, err := inbox.Decode([]byte(s))
	m := inbox.Object(v)
	return m, err == nil && m != nil
}

// glob lists dir entries matching pattern, as a bash glob without dot
// files, sorted.
func glob(dir, pattern string) []string {
	ms, _ := filepath.Glob(filepath.Join(dir, pattern))
	var out []string
	for _, m := range ms {
		if !strings.HasPrefix(filepath.Base(m), ".") {
			out = append(out, m)
		}
	}
	return out
}

// Status is message-status.sh [me]: the channel's metadata, read-only (no
// lock, no herdr call, no bodies, no claim tokens).
func (e *Env) Status(args []string) int { return e.finish(e.status(args)) }

func (e *Env) status(args []string) error {
	if len(args) > 1 {
		return die("status [me]")
	}
	me := ""
	if len(args) == 1 {
		me = args[0]
	}
	if me != "" && !inbox.NameOK(me) {
		return die("invalid name")
	}
	if err := e.open(true); err != nil {
		return err
	}
	r := e.ch
	out := e.Stdout
	rep := &StatusReport{Health: []string{}, Roles: []RoleStatus{}, Peers: []any{}, Holds: []string{}, Mailboxes: []MailboxStatus{}, Alerts: []any{}, DiagnosticLogs: []string{}}
	if e.JSON {
		out = io.Discard
	}
	for _, f := range e.healthSnapshot() {
		fmt.Fprintln(out, f)
		rep.Health = append(rep.Health, f)
	}
	now := e.unix()
	for _, role := range []string{"courier", "watch"} {
		pid, age := "none", "unknown"
		if st, err := os.Stat(r.Path(role + ".lock")); err == nil && st.IsDir() {
			if err := inbox.SafeLeaf(r.Dir, r.Path(role+".lock", "pid")); err != nil {
				return die("%s", err.Error())
			}
		}
		if inbox.Regular(r.Dir, r.Path(role+".lock", "pid")) {
			pid = inbox.CatText(r.Path(role+".lock", "pid"))
		}
		stamp := r.Path("state", "heartbeat")
		if role == "watch" {
			stamp = r.Path("state", "watch-heartbeat")
		}
		if t, err := inbox.Mtime(stamp); err == nil {
			age = strconv.FormatInt(now-t, 10)
		}
		fmt.Fprintf(out, "%s pid=%s heartbeat-age=%s; PID existence alone is not proof of health\n", role, pid, age)
		rep.Roles = append(rep.Roles, RoleStatus{Role: role, PID: pid, HeartbeatAge: age})
	}
	pb, pok := inbox.ReadStrict(r.Dir, r.Path("peers"))
	ab, aok := inbox.ReadStrict(r.Dir, r.Path("peers.approved"))
	_, pvalid := inbox.ValidRegistry(pb)
	approvedPeers, avalid := inbox.ValidRegistry(ab)
	pvalid, avalid = pvalid && pok, avalid && aok
	valid, approved := "no", "no"
	if pvalid && avalid {
		valid = "yes"
		if _, ok := r.Registry(); ok {
			approved = "yes"
		}
	}
	pending := "yes"
	if approved == "yes" {
		pending = "no"
	}
	fmt.Fprintf(out, "registry valid=%s approved=%s pending=%s\n", valid, approved, pending)
	rep.Registry = RegistryStatus{Valid: valid == "yes", Approved: approved == "yes", Pending: pending == "yes"}
	if avalid {
		// to_entries keeps the file's key order.
		for _, p := range orderedPeers(ab, approvedPeers) {
			po := obj("name", p.Name, "role", p.Role, "pane", p.Identity.Pane, "session", sessionObj(p.Identity.Session))
			out.Write(line(po))
			rep.Peers = append(rep.Peers, po)
		}
	}
	fmt.Fprintln(out, "authority=foreground-check required-adapters=sandbox,provenance,capture,wake,audit,checkpoint")
	var holds []string
	for _, p := range []string{r.Path("state", "route-paused"), r.Path("state", "route-frozen"), r.Path("state", "automation-held")} {
		if _, err := os.Stat(p); err == nil {
			holds = append(holds, p)
		}
	}
	for _, pat := range []string{"hold-*", "resume-*.sent", "resume-held-*"} {
		for _, p := range glob(r.Path("state"), pat) {
			if _, err := os.Stat(p); err == nil {
				holds = append(holds, p)
			}
		}
	}
	for _, p := range holds {
		fmt.Fprintf(out, "hold-or-intent: %s\n", filepath.Base(p))
		rep.Holds = append(rep.Holds, filepath.Base(p))
	}
	pendingAlerts := 0
	for _, p := range glob(r.Path("alerts"), "*.pending") {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			pendingAlerts++
		}
	}
	fmt.Fprintf(out, "pending-alerts=%d\n", pendingAlerts)
	rep.PendingAlerts = pendingAlerts
	for _, box := range glob(r.Dir, "*") {
		if st, err := os.Stat(filepath.Join(box, "new")); err != nil || !st.IsDir() {
			continue
		}
		if !inbox.NoLinks(box) {
			return die("mailbox symlink")
		}
		name := filepath.Base(box)
		orphan := "yes"
		if _, ok := r.Peer(name); ok {
			orphan = "no"
		}
		fmt.Fprintf(out, "%s orphan=%s new=%d ready=%d cur=%d quarantine=%d\n", name, orphan,
			inbox.CountState(filepath.Join(box, "new")), inbox.CountState(filepath.Join(box, "ready")),
			inbox.CountState(filepath.Join(box, "cur")), inbox.CountState(filepath.Join(box, "quarantine")))
		claim := "none"
		if r.ClaimValid(box) {
			claim = "held"
		} else if st, err := os.Stat(filepath.Join(box, "claim")); err == nil && st.IsDir() {
			claim = "incomplete"
		}
		fmt.Fprintf(out, "unacknowledged=%d claim=%s\n", len(r.Unacked(box)), claim)
		mb := MailboxStatus{Name: name, Orphan: orphan == "yes", New: inbox.CountState(filepath.Join(box, "new")), Ready: inbox.CountState(filepath.Join(box, "ready")),
			Cur: inbox.CountState(filepath.Join(box, "cur")), Quarantine: inbox.CountState(filepath.Join(box, "quarantine")), Unacknowledged: len(r.Unacked(box)), Claim: claim,
			Messages: []any{}, Suspicious: []string{}, Partial: []string{}, Notices: []any{}}
		for _, state := range []string{"new", "ready", "cur", "quarantine"} {
			if !inbox.NoLinks(filepath.Join(box, state)) {
				return die("state symlink")
			}
			for _, f := range inbox.MdFiles(filepath.Join(box, state)) {
				m, ok := inbox.Envelope(r.Dir, f)
				if !ok {
					fmt.Fprintf(out, "suspicious entry in %s/%s; no body displayed\n", name, state)
					mb.Suspicious = append(mb.Suspicious, state)
					continue
				}
				if me != "" && m["from"] != me && m["to"] != me {
					continue
				}
				ack := r.AckValid(box, strings.TrimSuffix(filepath.Base(f), ".md"))
				mo := obj("id", m["id"], "from", m["from"], "to", m["to"], "kind", m["kind"], "reply_to", m["reply_to"], "state", state, "acknowledged", ack)
				out.Write(line(mo))
				mb.Messages = append(mb.Messages, mo)
			}
		}
		tmp, _ := os.ReadDir(filepath.Join(box, "tmp"))
		for _, t := range tmp {
			if n := t.Name(); n != "." && n != ".." {
				fmt.Fprintf(out, "partial write: %s\n", filepath.Join(box, "tmp", n))
				mb.Partial = append(mb.Partial, filepath.Join(box, "tmp", n))
			}
		}
		for _, f := range glob(filepath.Join(box, "notices"), "*.json") {
			if st, err := os.Stat(f); err != nil || !st.Mode().IsRegular() {
				continue
			}
			v, err := decodeFile(f)
			m := inbox.Object(v)
			if err != nil || m == nil {
				fmt.Fprintln(out, "invalid notice")
				continue
			}
			no := obj("id", m["id"], "to", m["to"], "state", m["state"], "authority", m["authority"])
			out.Write(line(no))
			mb.Notices = append(mb.Notices, no)
		}
		rep.Mailboxes = append(rep.Mailboxes, mb)
	}
	for _, f := range glob(r.Path("alerts"), "*.pending") {
		if inbox.Regular(r.Dir, f) {
			b, _ := os.ReadFile(f)
			out.Write(b)
			if v, err := inbox.Decode(b); err == nil {
				rep.Alerts = append(rep.Alerts, v)
			}
		}
	}
	for _, f := range glob(r.Path("state"), "diagnostic-*.log") {
		if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
			fmt.Fprintf(out, "diagnostic log: %s\n", f)
			rep.DiagnosticLogs = append(rep.DiagnosticLogs, f)
		}
	}
	if e.JSON {
		e.Stdout.Write(line(rep))
	}
	return nil
}

func sessionObj(s inbox.Session) kv {
	return obj("agent", s.Agent, "kind", s.Kind, "source", s.Source, "value", s.Value)
}

// orderedPeers returns the peers in the file's key order.
func orderedPeers(b []byte, peers []inbox.Peer) []inbox.Peer {
	keys := objectKeys(b)
	by := map[string]inbox.Peer{}
	for _, p := range peers {
		by[p.Name] = p
	}
	var out []inbox.Peer
	for _, k := range keys {
		if p, ok := by[k]; ok {
			out = append(out, p)
		}
	}
	return out
}

// StatusReport is `herdsman message status --json`: one object with the
// text output's facts. Fields:
//
//	health           the health flags (courier-down, watch-down, *-lock-stale, setup-proof-stale, timing-invalid, automation-held)
//	roles            the courier and the watcher: role, pid ("none") and heartbeat_age (seconds, or "unknown")
//	registry         valid, approved and pending, as booleans
//	peers            the approved peers: name, role, pane, session
//	holds            the hold and resume intent file names under state/
//	pending_alerts   how many alerts wait to be shown
//	mailboxes        each mailbox's counts, claim (held, incomplete, none), headers, notices and partial writes
//	alerts           the pending alert payloads
//	diagnostic_logs  the courier's diagnostic log paths
type StatusReport struct {
	Health         []string        `json:"health"`
	Roles          []RoleStatus    `json:"roles"`
	Registry       RegistryStatus  `json:"registry"`
	Peers          []any           `json:"peers"`
	Holds          []string        `json:"holds"`
	PendingAlerts  int             `json:"pending_alerts"`
	Mailboxes      []MailboxStatus `json:"mailboxes"`
	Alerts         []any           `json:"alerts"`
	DiagnosticLogs []string        `json:"diagnostic_logs"`
}

// RoleStatus is a singleton's lock owner and heartbeat age.
type RoleStatus struct {
	Role         string `json:"role"`
	PID          string `json:"pid"`
	HeartbeatAge string `json:"heartbeat_age"`
}

// RegistryStatus is the registry's state.
type RegistryStatus struct {
	Valid    bool `json:"valid"`
	Approved bool `json:"approved"`
	Pending  bool `json:"pending"`
}

// MailboxStatus is one mailbox: headers only, never bodies or claim tokens.
type MailboxStatus struct {
	Name           string   `json:"name"`
	Orphan         bool     `json:"orphan"`
	New            int      `json:"new"`
	Ready          int      `json:"ready"`
	Cur            int      `json:"cur"`
	Quarantine     int      `json:"quarantine"`
	Unacknowledged int      `json:"unacknowledged"`
	Claim          string   `json:"claim"`
	Messages       []any    `json:"messages"`
	Suspicious     []string `json:"suspicious"`
	Partial        []string `json:"partial"`
	Notices        []any    `json:"notices"`
}
