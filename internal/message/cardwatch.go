package message

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/inbox"
)

// CardWatch is card-watch.sh: the supervised card and blocked-prompt
// monitor. Side-agent cards on the watched panes are journaled and relayed
// to the adjutant as side_agent_data from the orchestrator's transport;
// blocked prompts alert; approved Codex goals resume; nothing else is
// typed. Every INTERVAL seconds, for RUN_FOR seconds (0: until stopped).
func (e *Env) CardWatch(args []string) int { return e.finish(e.cardWatch(args)) }

var (
	cardRe    = regexp.MustCompile(`^[ \t]*✦ Heads up`)
	promptRe  = regexp.MustCompile(`^[ \t]*❯[ \t]*`)
	squareRe  = regexp.MustCompile(`^[ \t]*■[ \t]*`)
	blockedPr = regexp.MustCompile(`(?i)^[ \t]*(([>]?[ \t]*[0-9]+[.)]?[ \t]*)?)(Do you want to proceed|Do you trust|Allow reads outside|Allow once|Allow always|Password for|Device not configured|Permission required|approval required|Switch to gpt|Yes, proceed|Yes, and don|escalated execution requires|Queued follow-up inputs|Type your answer|Usage limit|Selected model is at capacity|This content can.t be shown)`)
	queuedRe  = regexp.MustCompile(`^m[0-9]{12} queued`)
)

type watcher struct {
	*Env
	targets    []any
	watchSum   string
	resumeSum  string
	resumes    []byte
	sender     string
	receiver   string
	senderPane string
	auto       bool
}

func (e *Env) cardWatch(args []string) error {
	if len(args) != 0 {
		return die("usage: herdsman message card-watch")
	}
	interval, err := e.uintEnv("INTERVAL", "5", 1, 60)
	if err != nil {
		return err
	}
	runFor, err := e.uintEnv("RUN_FOR", "0", 0, 86400)
	if err != nil {
		return err
	}
	if err := e.open(false); err != nil {
		return err
	}
	peers, ok := e.ch.Registry()
	if !ok {
		return die("unapproved pair")
	}
	w := &watcher{Env: e, resumeSum: "absent", resumes: []byte("{}")}
	wt := e.path("watch-targets.json")
	b, ok := inbox.ReadStrict(e.ch.Dir, wt)
	v, _ := inbox.Decode(b)
	arr, isArr := v.([]any)
	if !ok || !isArr || len(arr) == 0 || !validTargets(arr) {
		return die("invalid watch targets")
	}
	w.targets = arr
	w.watchSum, _ = inbox.DigestFile(wt)
	rt := e.path("resume-targets.json")
	if inbox.Exists(rt) {
		b, ok := inbox.ReadStrict(e.ch.Dir, rt)
		v, _ := inbox.Decode(b)
		m, isObj := v.(map[string]any)
		if !ok || !isObj || !validResumes(m) {
			return die("invalid resume targets")
		}
		w.resumes = b
		w.resumeSum, _ = inbox.DigestFile(rt)
	}
	if err := os.Mkdir(e.path("watch.lock"), 0o700); err != nil {
		return die("watcher already owned; inspect lock")
	}
	e.hold(inbox.Adopt(e.path("watch.lock")))
	_ = os.WriteFile(e.path("watch.lock", "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	if !inbox.NoLinks(e.path("card-events")) {
		return die("card journal symlink")
	}
	if err := os.MkdirAll(e.path("card-events"), 0o700); err != nil {
		return die("cannot create card-events")
	}
	for _, p := range peers {
		switch p.Role {
		case "orchestrator":
			w.sender, w.senderPane = p.Name, p.Identity.Pane
		case "adjutant":
			w.receiver = p.Name
		}
	}
	started := e.unix()
	for {
		if err := w.pass(); err != nil {
			return err
		}
		if runFor != 0 && e.unix()-started >= int64(runFor) {
			return nil
		}
		e.sleep(time.Duration(interval) * time.Second)
	}
}

func validTargets(arr []any) bool {
	for _, t := range arr {
		m := inbox.Object(t)
		if m == nil || len(m) != 2 || !hasKey(m, "session") {
			return false
		}
		if _, ok := m["pane"].(string); !ok {
			return false
		}
		s := inbox.Object(m["session"])
		if s == nil || len(s) != 4 || !hasKey(s, "agent") || !hasKey(s, "kind") || !hasKey(s, "source") || !hasKey(s, "value") {
			return false
		}
	}
	return true
}

func validResumes(m map[string]any) bool {
	for k, v := range m {
		t := inbox.Object(v)
		id := inbox.Object(t["identity"])
		if !inbox.NameShape(k) || t["role"] != "task-agent" || id == nil || len(id) != 2 || !hasKey(id, "pane") || !hasKey(id, "session") {
			return false
		}
	}
	return true
}

// coverage reports whether the watch and resume targets are the snapshot.
func (w *watcher) coverage() bool {
	e := w.Env
	current := "absent"
	if inbox.Regular(e.ch.Dir, e.path("resume-targets.json")) {
		current, _ = inbox.DigestFile(e.path("resume-targets.json"))
	}
	wt := e.path("watch-targets.json")
	sum, _ := inbox.DigestFile(wt)
	return inbox.Regular(e.ch.Dir, wt) && sum == w.watchSum && current == w.resumeSum
}

const heldText = "HUMAN: inspect cards and paused goals; no relay or input. Rebuild protected setup after any unsandboxed launch."

// pass is one watcher interval.
func (w *watcher) pass() error {
	e := w.Env
	r := e.ch
	if err := r.SafeTouch(e.path("state", "watch-heartbeat")); err != nil {
		return err
	}
	e.retryAlerts()
	if _, err := e.health(); err != nil {
		return err
	}
	coverage := w.coverage()
	w.auto = false
	ok, fault, unknown := false, "", []inbox.Unknown(nil)
	if coverage {
		ok, fault, unknown = r.Boundary(e.ctx(), e.Client)
	}
	if coverage && ok {
		w.auto = true
		_ = os.Remove(e.path("state", "automation-held"))
		_ = os.Remove(e.path("state", "agent-list-failures"))
		if err := e.resolveAlert("automation-held"); err != nil {
			return err
		}
	} else {
		if coverage {
			if err := r.RecordExposure(fault, unknown, false); err != nil {
				return writeFailed(err)
			}
		}
		failures := 0
		lf := e.path("state", "agent-list-failures")
		if coverage && fault == inbox.FaultList {
			if inbox.Regular(r.Dir, lf) {
				n, err := inbox.Uint("agent-list-failures", inbox.CatText(lf), 0, 99999999)
				if err != nil {
					return die("%s", err.Error())
				}
				failures = n
			}
			failures++
			if err := r.WriteText(lf, strconv.Itoa(failures)); err != nil {
				return writeFailed(err)
			}
		} else {
			_ = os.Remove(lf)
		}
		if failures != 1 {
			if err := r.WriteText(e.path("state", "automation-held"), heldText); err != nil {
				return writeFailed(err)
			}
			if err := e.notify("automation-held", "Automatic coordination held", "Human inspection required. No goal resume or card relay; the adjutant must tell the user."); err != nil {
				return err
			}
		}
	}
	for _, t := range w.targets {
		if err := w.watchTarget(t); err != nil {
			return err
		}
	}
	if !w.coverage() {
		w.auto = false
		if err := r.WriteText(e.path("state", "automation-held"), "HUMAN changed watch coverage; restart under human setup"); err != nil {
			return writeFailed(err)
		}
		if err := e.notify("automation-held", "Automatic coordination held", "Watch coverage changed; no resume or card relay. Tell the user."); err != nil {
			return err
		}
	}
	if w.auto {
		if err := w.relay(); err != nil {
			return err
		}
		if err := w.resumeAll(); err != nil {
			return err
		}
	}
	return nil
}

// countJournal is count_journal: the source's journal records whose relay
// the adjutant has not acknowledged; under the cap the journal alert is
// resolved.
func (w *watcher) countJournal(pane, key string) (int, error) {
	e := w.Env
	active := 0
	for _, ev := range glob(e.path("card-events"), "*.json") {
		if !inbox.Regular(e.ch.Dir, ev) {
			continue
		}
		v, err := decodeFile(ev)
		if err != nil || inbox.Object(inbox.Object(v)["source"])["pane"] != pane {
			continue
		}
		queued := e.path("state", "card-"+strings.TrimSuffix(filepath.Base(ev), ".json")+".queued")
		if inbox.Regular(e.ch.Dir, queued) {
			relayed := strings.Fields(inbox.CatText(queued) + " ")
			if len(relayed) > 0 && inbox.IDOK(relayed[0]) {
				term := e.path("ids", relayed[0], "terminal.json")
				if tv, err := decodeFile(term); err == nil && inbox.Regular(e.ch.Dir, term) && inbox.Object(tv)["state"] == "handled" {
					continue
				}
			}
		}
		active++
	}
	if active < 8 {
		if err := e.resolveAlert("card-journal-" + key); err != nil {
			return active, err
		}
	}
	return active, nil
}

func (w *watcher) watchTarget(t any) error {
	e := w.Env
	r := e.ch
	tm := inbox.Object(t)
	pane, _ := tm["pane"].(string)
	key := inbox.Digest([]byte(sortedCompact(t) + "\n"))
	idRaw, _ := compact(t)
	want, _ := inbox.ParseIdentity(idRaw)
	observed, code, _ := inbox.LiveIdentity(e.ctx(), e.Client, pane)
	if code != inbox.LiveOK || !observed.Same(want) {
		return e.notify("watch-drift-"+key, "Notice monitor identity changed", "Inspect configured route panes and rebuild approved targets.")
	}
	if err := e.resolveAlert("watch-drift-" + key); err != nil {
		return err
	}
	screen, err := e.Client.ReadText(e.ctx(), pane, herdr.SourceRecentUnwrapped, 60)
	if err != nil {
		return e.notify("watch-read-"+key, "Notice monitor read failed", "Inspect configured pane manually.")
	}
	if err := e.resolveAlert("watch-read-" + key); err != nil {
		return err
	}
	screen = strings.TrimRight(screen, "\n")
	lines := strings.Split(screen, "\n")
	var cards []string
	for _, l := range lines {
		if cardRe.MatchString(l) {
			cards = append(cards, l)
		}
	}
	count := 0
	for _, c := range cards {
		if strings.TrimSpace(c) != "" {
			count++
		}
	}
	if count > 4 {
		if err := e.notify("card-overflow-"+key, "Side-agent cards need inspection", "Capture cap reached; inspect the source pane and durable harness journal."); err != nil {
			return err
		}
	} else if err := e.resolveAlert("card-overflow-" + key); err != nil {
		return err
	}
	active := -1
	if inbox.Regular(r.Dir, e.alert("card-journal-"+key, ".pending")) || inbox.Regular(r.Dir, e.alert("card-journal-"+key, ".sent")) {
		if active, err = w.countJournal(pane, key); err != nil {
			return err
		}
	}
	first := cards
	if len(first) > 4 {
		first = first[:4]
	}
	for _, card := range first {
		if card == "" {
			continue
		}
		cardKey := inbox.Digest([]byte(key + "\n" + card))
		marker := e.path("state", "card-visible-"+cardKey)
		if st, err := os.Stat(marker); (err != nil || !st.Mode().IsRegular()) && w.auto {
			if active < 0 {
				if active, err = w.countJournal(pane, key); err != nil {
					return err
				}
			}
			if active >= 8 {
				if err := e.notify("card-journal-"+key, "Side-agent cards need inspection", "Source journal cap reached; human disposition required."); err != nil {
					return err
				}
			} else {
				f, err := os.CreateTemp(e.path("card-events"), cardKey+".")
				if err != nil {
					return die("cannot journal the card")
				}
				name := f.Name()
				_ = f.Close()
				_ = os.Remove(name)
				ev := pretty(obj("version", 1, "type", "side_agent_data", "id", filepath.Base(name), "source", t, "text", card))
				if err := r.Atomic(name+".json", ev); err != nil {
					return writeFailed(err)
				}
				active++
			}
		}
		if w.auto {
			if err := r.WriteText(marker, card); err != nil {
				return writeFailed(err)
			}
		}
	}
	for _, marker := range glob(e.path("state"), "card-visible-*") {
		if !inbox.Regular(r.Dir, marker) {
			continue
		}
		old := inbox.CatText(marker)
		oldKey := inbox.Digest([]byte(key + "\n" + old))
		if filepath.Base(marker) != "card-visible-"+oldKey {
			continue
		}
		visible := false
		for _, c := range cards {
			if c == old {
				visible = true
			}
		}
		if !visible {
			_ = os.Remove(marker)
		}
	}
	phrase := ""
	for _, l := range lines {
		l = promptRe.ReplaceAllString(l, " ")
		l = squareRe.ReplaceAllString(l, " ")
		if blockedPr.MatchString(l) {
			phrase = l
			break
		}
	}
	marker := e.path("state", "blocked-"+key)
	if phrase != "" {
		phraseKey := inbox.Digest([]byte(key + ":" + phrase))
		previous := ""
		if inbox.Regular(r.Dir, marker) {
			previous = inbox.CatText(marker)
		}
		if previous == phraseKey {
			return e.notify("blocked-"+phraseKey, "Agent notice or blocked state", "Inspect pane "+pane+". No prompt was answered; a timeout remains a refusal.")
		}
		if previous != "" {
			if err := e.resolveAlert("blocked-" + previous); err != nil {
				return err
			}
		}
		if err := r.WriteText(marker, phraseKey); err != nil {
			return writeFailed(err)
		}
	} else if inbox.Regular(r.Dir, marker) {
		previous := inbox.CatText(marker)
		if err := e.resolveAlert("blocked-" + previous); err != nil {
			return err
		}
		_ = os.Remove(marker)
	}
	return nil
}

// sub is a child command run with extra environment, its output captured.
func (e *Env) sub(extra map[string]string, stdout, stderr *bytes.Buffer) *Env {
	parent := e.env
	return &Env{Getenv: func(k string) string {
		if v, ok := extra[k]; ok {
			return v
		}
		return parent(k)
	}, Stdout: stdout, Stderr: stderr, Client: e.Client, Now: e.Now, Sleep: e.Sleep, Events: e.Events, Executable: e.Executable, Ctx: e.Ctx}
}

func (w *watcher) relay() error {
	e := w.Env
	r := e.ch
	invalid := false
	for _, ev := range glob(e.path("card-events"), "*.json") {
		if !inbox.Exists(ev) {
			continue
		}
		b, ok := inbox.ReadStrict(r.Dir, ev)
		v, _ := inbox.Decode(b)
		m := inbox.Object(v)
		_, idOK := m["id"].(string)
		_, textOK := m["text"].(string)
		if !ok || m == nil || !inbox.Equal(m["version"], toAny(1)) || m["type"] != "side_agent_data" || !idOK || inbox.Object(m["source"]) == nil || !textOK {
			invalid = true
			continue
		}
		eventKey := strings.TrimSuffix(filepath.Base(ev), ".json")
		queued := e.path("state", "card-"+eventKey+".queued")
		if st, err := os.Stat(queued); err == nil && st.Mode().IsRegular() {
			continue
		}
		sourceKey := inbox.Digest([]byte(sortedCompact(m["source"]) + "\n"))
		if ok, fault, unknown := r.Boundary(e.ctx(), e.Client); !ok {
			if err := r.RecordExposure(fault, unknown, false); err != nil {
				return writeFailed(err)
			}
			break
		}
		var out bytes.Buffer
		code := e.sub(map[string]string{"HERDR_PANE_ID": w.senderPane}, &out, &out).Send([]string{w.sender, w.receiver, ev, "side_agent_data"})
		receipt := strings.TrimRight(out.String(), "\n")
		if code == 0 {
			first := ""
			for _, l := range strings.Split(receipt, "\n") {
				if queuedRe.MatchString(l) {
					first = l
					break
				}
			}
			if err := r.WriteText(queued, first); err != nil {
				return writeFailed(err)
			}
			if err := e.resolveAlert("relay-overflow-" + sourceKey); err != nil {
				return err
			}
			if err := e.resolveAlert("relay-failed-" + sourceKey); err != nil {
				return err
			}
			continue
		}
		switch {
		case strings.Contains(receipt, "side-agent budget full"):
			if err := e.notify("relay-overflow-"+sourceKey, "Side-agent cards need inspection", "Source relay budget unavailable; durable journal retained for human inspection."); err != nil {
				return err
			}
		case strings.Contains(receipt, "HUMAN boundary"):
			goto done
		default:
			if err := e.notify("relay-failed-"+sourceKey, "Side-agent relay failed", "The send was refused; durable journal retained. Inspect message status."); err != nil {
				return err
			}
		}
	}
done:
	if invalid {
		return e.notify("card-invalid", "Invalid side-agent record", "Retain and inspect journal record.")
	}
	return e.resolveAlert("card-invalid")
}

// resumeNames are the approved Codex peers that are not the adjutant, then
// the Codex resume targets, in file order.
func (w *watcher) resumeNames() []string {
	var names []string
	collect := func(b []byte, skipAdjutant bool) {
		o, _ := rawObject(b)
		for _, x := range o {
			v, _ := inbox.Decode(x.V.(json.RawMessage))
			m := inbox.Object(v)
			if skipAdjutant && m["role"] == "adjutant" {
				continue
			}
			if inbox.Object(inbox.Object(m["identity"])["session"])["agent"] == "codex" {
				names = append(names, x.K)
			}
		}
	}
	ab, _ := os.ReadFile(w.path("peers.approved"))
	collect(ab, true)
	collect(w.resumes, false)
	return names
}

func (w *watcher) resumeAll() error {
	e := w.Env
	r := e.ch
	for _, name := range w.resumeNames() {
		var out bytes.Buffer
		code := e.sub(map[string]string{"RESUME_TARGET_SUM": w.resumeSum}, &out, nil).resumeCapture(name, e.Stderr)
		output := strings.TrimRight(out.String(), "\n")
		heldFile := e.path("state", "resume-held-"+name)
		switch code {
		case 0:
			if err := e.resolveAlert("resume-" + name); err != nil {
				return err
			}
			_ = os.Remove(heldFile)
		case 2:
			if err := e.notify("resume-"+name, "Goal resume needs inspection", "Inspect identity, focus and retained read-back; no blind retry."); err != nil {
				return err
			}
		default:
			switch {
			case strings.HasPrefix(output, "HELD mutation lock busy"):
			case strings.HasPrefix(output, "HELD user pause or freeze"), strings.HasPrefix(output, "HELD turn is not idle or is blocked"),
				strings.HasPrefix(output, "HELD turn changed before input"):
				if err := r.WriteText(heldFile, output); err != nil {
					return writeFailed(err)
				}
			default:
				if inbox.Regular(r.Dir, heldFile) {
					if err := e.notify("resume-"+name, "Goal resume held", output); err != nil {
						return err
					}
				} else if err := r.WriteText(heldFile, output); err != nil {
					return writeFailed(err)
				}
			}
		}
	}
	return nil
}

// resumeCapture runs Resume with its stderr going to the watcher's.
func (e *Env) resumeCapture(name string, stderr interface{ Write([]byte) (int, error) }) int {
	e.Stderr = stderr
	return e.Resume([]string{name})
}

