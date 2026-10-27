package message

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// Resume is `herdsman message resume-goal <name>`: one approved Codex recovery
// episode. NOOP or SENT exit 0; HELD exit 3; an error exit 2.
func (e *Env) Resume(args []string) int { return e.finish(e.resume(args)) }

var (
	workingRe  = regexp.MustCompile(`(?i)^[ \t]*(Working|Pursuing goal|Goal active|Incubating|thinking)|esc to interrupt|… \([0-9]`)
	achievedRe = regexp.MustCompile(`(?i)^[ \t■]*(Goal achieved)`)
	blockedRe  = regexp.MustCompile(`(?i)^[ \t■]*(Queued follow-up inputs|Type your answer|Usage limit|Selected model is at capacity|This content can.t be shown|Do you|Allow |Permission|Password|Replace current goal|Resume paused goal)`)
	pausedRe   = regexp.MustCompile(`^[ \t]*Goal paused( \(/goal resume\))?[ \t]*$`)
	sentRe     = regexp.MustCompile(`(?i)^[ \t]*(Working|Pursuing goal|Goal active)|esc to interrupt`)
)

// anyLine reports whether a line of text matches re, as grep -q.
func anyLine(re *regexp.Regexp, text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

func held(format string, a ...any) error {
	return &heldErr{msg: fmt.Sprintf(format, a...)}
}

type heldErr struct{ msg string }

func (h *heldErr) Error() string { return h.msg }

type resumer struct {
	*Env
	name     string
	expected inbox.Identity
	pane     string
	status   string
	screen   string
	// beforeLock runs just before the mutation lock is taken (a test hook
	// for an overlapping helper).
	beforeLock func()
}

func (e *Env) resume(args []string) error {
	err := e.resumeRun(args, nil)
	if h, ok := err.(*heldErr); ok {
		e.statusLine("resume", "held", "HELD "+h.msg)
		return exitWith(3)
	}
	return err
}

func (e *Env) resumeRun(args []string, beforeLock func()) error {
	if len(args) != 1 || !inbox.NameOK(args[0]) {
		return die("resume <registered-name>")
	}
	if err := e.open(false); err != nil {
		return err
	}
	if ok, fault, unknown := e.ch.Boundary(e.ctx(), e.Client); !ok {
		// Every check records what it saw (A26, e93bcc1 B3-004).
		if err := e.recordExposure(fault, unknown); err != nil {
			return err
		}
		return held("HUMAN boundary; no automatic input")
	}
	g := &resumer{Env: e, name: args[0], beforeLock: beforeLock}
	target, ok := g.targetRecord()
	if !ok {
		return die("unknown approved target")
	}
	role, _ := target["role"].(string)
	if role != "orchestrator" && role != "task-agent" {
		return die("target must be orchestrator or task-agent, never adjutant")
	}
	idRaw, _ := compact(target["identity"])
	g.expected, _ = inbox.ParseIdentity(idRaw)
	g.pane = g.expected.Pane
	if g.pane == g.adjutantPane() {
		return die("never target the adjutant pane")
	}
	if g.expected.Session.Agent != "codex" {
		return die("target is not Codex")
	}
	key := inbox.Digest([]byte(sortedCompact(target["identity"]) + "\n"))
	sent := e.path("state", "resume-"+key+".sent")
	if !g.guards() {
		return g.pauseHeld()
	}
	if err := g.observe(); err != nil {
		return err
	}
	switch g.classify() {
	case "working", "achieved":
		state := g.classify()
		_ = os.Remove(sent)
		e.statusLine("resume", "noop", "NOOP verified "+state)
		return nil
	case "blocked":
		return held("turn is not idle or is blocked")
	case "unknown":
		if st, err := os.Stat(sent); err == nil && st.Mode().IsRegular() {
			return held("reconcile unknown read-back after previous resume")
		}
		e.statusLine("resume", "noop", "NOOP no current paused-goal marker")
		return nil
	}
	if inbox.Exists(sent) {
		return held("reconcile the previous resume")
	}
	if g.beforeLock != nil {
		g.beforeLock()
	}
	if l, lerr := e.takeMutation(1); lerr != nil {
		return lerr
	} else if l == nil {
		return held("mutation lock busy")
	}
	if inbox.Exists(sent) {
		return held("reconcile the previous resume")
	}
	if !g.guards() {
		return g.pauseHeld()
	}
	if ok, fault, unknown := e.ch.Boundary(e.ctx(), e.Client); !ok {
		// Every check records what it saw (A26, e93bcc1 B3-004).
		if err := e.recordExposure(fault, unknown); err != nil {
			return err
		}
		return held("HUMAN boundary changed")
	}
	current, ok := g.targetRecord()
	if !ok || sortedCompact(current) != sortedCompact(target) {
		return held("target changed")
	}
	if err := g.observe(); err != nil {
		return err
	}
	if g.classify() != "paused" {
		return held("turn changed before input")
	}
	if !g.guards() {
		return g.pauseHeld()
	}
	panes, err := e.Client.PaneList(e.ctx())
	tabs := map[string]bool{}
	for _, p := range panes {
		if p.Focused {
			tabs[p.TabID] = true
		}
	}
	if err != nil || len(tabs) != 1 {
		return die("cannot capture focus")
	}
	focus := ""
	for t := range tabs {
		focus = t
	}
	if err := e.ch.WriteText(sent, fmt.Sprintf("%d intent /goal resume; restore tab %s", e.unix(), focus)); err != nil {
		return writeFailed(err)
	}
	perr := e.Client.AgentPrompt(e.ctx(), g.pane, "/goal resume")
	if err := e.Client.TabFocus(e.ctx(), focus); err != nil {
		return die("focus restoration uncertain; human inspection required")
	}
	screen, err := e.Client.ReadText(e.ctx(), g.pane, herdr.SourceRecentUnwrapped, 60)
	if err != nil {
		return die("resume read-back unavailable")
	}
	if err := e.ch.WriteText(e.path("state", "resume-"+key+".readback"), strings.TrimRight(screen, "\n")); err != nil {
		return writeFailed(err)
	}
	if perr != nil {
		return held("resume submission uncertain; inspect read-back")
	}
	if anyLine(sentRe, screen) {
		e.statusLine("resume", "sent", "SENT /goal resume; working read-back retained")
		return nil
	}
	return held("resume read-back not working; inspect retained evidence")
}

// targetRecord is target_record: the registered peer, or the approved
// task-agent of resume-targets.json, under RESUME_TARGET_SUM when set.
func (g *resumer) targetRecord() (map[string]any, bool) {
	e := g.Env
	if _, ok := e.ch.Registry(); !ok {
		return nil, false
	}
	if want := e.env("RESUME_TARGET_SUM"); want != "" {
		actual := "absent"
		if inbox.Regular(e.ch.Dir, e.path("resume-targets.json")) {
			actual, _ = inbox.DigestFile(e.path("resume-targets.json"))
		}
		if actual != want {
			return nil, false
		}
	}
	if b, ok := inbox.ReadStrict(e.ch.Dir, e.path("peers.approved")); ok {
		v, _ := inbox.Decode(b)
		if p := inbox.Object(inbox.Object(v)[g.name]); p != nil {
			return p, true
		}
	}
	b, ok := inbox.ReadStrict(e.ch.Dir, e.path("resume-targets.json"))
	if !ok {
		return nil, false
	}
	v, _ := inbox.Decode(b)
	t := inbox.Object(inbox.Object(v)[g.name])
	if t == nil || t["role"] != "task-agent" {
		return nil, false
	}
	return t, true
}

func (g *resumer) adjutantPane() string {
	v, _ := decodeFile(g.ch.Preflight)
	pair := inbox.Object(inbox.Object(inbox.Object(v)["boundary"])["pair"])
	for _, p := range pair {
		m := inbox.Object(p)
		if m["role"] == "adjutant" {
			pane, _ := inbox.Object(m["identity"])["pane"].(string)
			return pane
		}
	}
	return ""
}

func (g *resumer) guards() bool {
	e := g.Env
	return !inbox.Exists(e.path("state", "route-paused")) && !inbox.Exists(e.path("state", "route-frozen")) &&
		!inbox.Exists(e.path("state", "hold-"+g.name))
}

func (g *resumer) pauseHeld() error {
	e := g.Env
	file := e.path("state", "hold-"+g.name)
	owners := "unreadable"
	if b, ok := inbox.ReadStrict(e.ch.Dir, file); ok {
		if o, ok := holdOwners(b); ok {
			owners = o
		}
	} else if !inbox.Exists(file) {
		owners = "route"
	}
	return held("user pause or freeze; holds: %s", owners)
}

func (g *resumer) observe() error {
	e := g.Env
	a, err := e.Client.AgentGet(e.ctx(), g.pane)
	if err != nil {
		return die("identity unavailable")
	}
	v, _ := inbox.Decode(a.Raw)
	m := inbox.Object(v)
	lp := m["launch_pending"]
	if m["agent"] != "codex" || m["interactive_ready"] != true || (lp != nil && lp != false) {
		return die("target is not Codex or ready")
	}
	sess := inbox.Object(m["agent_session"])
	str := func(k string) string { s, _ := sess[k].(string); return s }
	pane, _ := m["pane_id"].(string)
	id := inbox.Identity{Pane: pane, Session: inbox.Session{Agent: str("agent"), Kind: str("kind"), Source: str("source"), Value: str("value")}}
	if sess == nil || !id.Same(g.expected) {
		return die("identity changed")
	}
	g.status = raw(m["agent_status"])
	if m["agent_status"] == nil {
		g.status = "null"
	}
	screen, err := e.Client.ReadText(e.ctx(), g.pane, herdr.SourceRecentUnwrapped, 60)
	if err != nil {
		return die("pane read failed")
	}
	g.screen = screen
	return nil
}

func (g *resumer) classify() string {
	switch {
	case g.status == "working" || anyLine(workingRe, g.screen):
		return "working"
	case anyLine(achievedRe, g.screen):
		return "achieved"
	case g.status != "idle" || anyLine(blockedRe, g.screen):
		return "blocked"
	case anyLine(pausedRe, g.screen):
		return "paused"
	}
	return "unknown"
}
