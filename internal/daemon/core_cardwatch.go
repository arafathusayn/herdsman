package daemon

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/notice"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
)

// The card watch on herdr-core does what the TUI's card watch does, through
// the runtime: it reads each member's screen with the provider's Read, relays
// side-agent cards to the adjutant as side_agent_data from the orchestrator
// through the route's daemon channel, and raises alerts for what needs a
// person. An alert is a pending record in the route's alerts folder, which
// the route's status lists, and one needs_attention event on the runtime
// topic when it is first raised; it is resolved when the cause is gone.
// Nothing is typed and nothing is answered.

// dialogRe is the screen of a dialog that only a person ends: a permission or
// trust question, a password, a usage limit. It adds a blocker; it never
// removes one the status reports, and it never confers identity.
var dialogRe = regexp.MustCompile(`(?im)^[ \t]*(([>]?[ \t]*[0-9]+[.)]?[ \t]*)?)(Do you want to proceed|Do you trust|Allow reads outside|Allow once|Allow always|Password for|Permission required|approval required|Yes, proceed|Yes, and don|escalated execution requires|Type your answer|Usage limit|Selected model is at capacity)`)

// cardRe is a side-agent card's first line, as the TUI's card watch reads it.
var cardRe = regexp.MustCompile(`^[ \t]*✦ Heads up`)

// The card watch's limits, the TUI's: at most four cards a screen are
// relayed, and a fifth is an overflow a person inspects.
const (
	coreCardsPerScreen = 4
	coreCardLines      = 60
)

// The reasons of needs_attention the card watch raises besides "dialog".
const (
	AttentionSideAgentCards = "side_agent_cards"
	AttentionRelayFailed    = "relay_failed"
	AttentionReadFailed     = "read_failed"
	AttentionIdentityLost   = "identity_lost"
)

// cardWatcher is one route's card watch on core. The blocked and dialog
// sightings live in memory; card markers and alerts live in the route's
// daemon-owned inbox, so a restarted daemon relays no card twice.
type cardWatcher struct {
	s  *coreServices
	sp route.ServiceSpec
	// seen is each execution's dialog reason at its last pass.
	seen map[string]string
	// raised are the alerts raised without a route channel to record them.
	raised map[string]bool
}

// cardWatch is the card watch's pass on core.
func (s *coreServices) cardWatch(sp route.ServiceSpec) func(context.Context) error {
	w := &cardWatcher{s: s, sp: sp, seen: map[string]string{}, raised: map[string]bool{}}
	return w.pass
}

func (w *cardWatcher) pass(ctx context.Context) error {
	c := w.s.c
	all, err := c.records.List(ctx, w.sp.Repo, w.sp.Route)
	if err != nil {
		return err
	}
	rt := w.channel()
	p := c.provider("")
	live := map[string]bool{}
	for _, e := range all {
		if e.Backend != runtime.BackendCore || e.Desired != runtime.DesiredRunning || e.Evidence.PID <= 0 {
			continue
		}
		live[e.ID] = true
		key := "core-" + sha256Hex([]byte("core\x00" + e.ID))[:24]
		switch e.Observation.Process {
		case runtime.ProcessLost, runtime.ProcessReplaced:
			w.alert(ctx, rt, "watch-drift-"+key, "Notice monitor identity changed", "Inspect the route's agents and start a new execution with herdsman up --continue.",
				&notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: AttentionIdentityLost,
					Detail: e.Agent + " no longer runs its registered process; nothing is read from or typed into its terminal"})
			continue
		}
		w.resolve(ctx, rt, "watch-drift-"+key)
		t := runtime.Target{ExecutionID: e.ID, Generation: e.Generation, TerminalAddress: e.TerminalAddress}
		text, err := p.Read(ctx, t, runtime.ReadRequest{Lines: coreCardLines, MaxBytes: 32 << 10})
		if err != nil {
			w.alert(ctx, rt, "watch-read-"+key, "Notice monitor read failed", "Inspect agent "+e.Agent+" in the app.",
				&notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: AttentionReadFailed,
					Detail: "the card watch could not read " + e.Agent + "'s screen through the runtime"})
			continue
		}
		w.resolve(ctx, rt, "watch-read-"+key)
		lines := strings.Split(strings.TrimRight(text.Text, "\n"), "\n")
		w.cards(ctx, rt, e, key, lines)
		w.blocked(ctx, rt, e, key, lines)
	}
	for id := range w.seen {
		if !live[id] {
			delete(w.seen, id)
		}
	}
	return nil
}

// channel is the route's daemon channel, or nil when the route has none.
func (w *cardWatcher) channel() *Route {
	if w.sp.Route == "" {
		return nil
	}
	if _, ok := w.s.c.d.routeEntry(w.sp.Route); !ok {
		return nil
	}
	rt, e := w.s.c.d.route(w.sp.Route)
	if e != nil {
		return nil
	}
	return rt
}

// alert raises an incident once: a pending record in the route's alerts
// folder and its event, when it has one. A raised incident stays quiet until
// it is resolved.
func (w *cardWatcher) alert(ctx context.Context, rt *Route, key, title, body string, ev *notice.Event) {
	if rt == nil {
		if w.raised[key] {
			return
		}
		w.raised[key] = true
		if ev != nil {
			w.s.c.d.emitNotice(*ev)
		}
		return
	}
	fresh := false
	_ = rt.do(ctx, func() *APIError {
		p := rt.path("alerts", key+".pending")
		if regular(p) {
			return nil
		}
		if err := os.MkdirAll(rt.path("alerts"), 0o700); err != nil {
			return Errf("internal", "alerts: %v", err)
		}
		if err := writeDurable(p, prettyJSON(obj("title", title, "body", body)), 0o600); err != nil {
			return Errf("internal", "alert %s: %v", key, err)
		}
		fresh = true
		return nil
	})
	if fresh && ev != nil {
		w.s.c.d.emitNotice(*ev)
	}
}

// resolve closes an incident: its pending record becomes resolved.
func (w *cardWatcher) resolve(ctx context.Context, rt *Route, key string) {
	if rt == nil {
		delete(w.raised, key)
		return
	}
	_ = rt.do(ctx, func() *APIError {
		p := rt.path("alerts", key+".pending")
		if regular(p) {
			_ = os.Rename(p, rt.path("alerts", key+".resolved"))
		}
		return nil
	})
}

// cards relays the side-agent cards on one member's screen, once each.
func (w *cardWatcher) cards(ctx context.Context, rt *Route, e runtime.Execution, key string, lines []string) {
	var cards []string
	for _, l := range lines {
		if cardRe.MatchString(l) && strings.TrimSpace(l) != "" {
			cards = append(cards, l)
		}
	}
	if len(cards) > coreCardsPerScreen {
		w.alert(ctx, rt, "card-overflow-"+key, "Side-agent cards need inspection", "Capture cap reached; inspect the agent in the app.",
			&notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: AttentionSideAgentCards,
				Detail: fmt.Sprintf("%s shows %d side-agent cards; the first %d are relayed", e.Agent, len(cards), coreCardsPerScreen)})
		cards = cards[:coreCardsPerScreen]
	} else {
		w.resolve(ctx, rt, "card-overflow-"+key)
	}
	if rt == nil {
		return
	}
	from, to := w.pair(rt)
	visible := map[string]bool{}
	for _, card := range cards {
		cardKey := sha256Hex([]byte(key + "\n" + card))
		visible[cardKey] = true
		marker := rt.path("state", "card-visible-"+cardKey)
		if regular(marker) || from == "" || to == "" {
			continue
		}
		id := inbox.ExecutionIdentity(e.HarnessKind, e.ID)
		body := string(prettyJSON(obj("version", 1, "type", "side_agent_data", "id", "core-"+cardKey[:24], "source", identityObj(id), "text", card)))
		var sent *APIError
		_ = rt.do(ctx, func() *APIError {
			_, sent = rt.send(from, SendRequest{To: to, Kind: "side_agent_data", Body: body, Key: cardKey[:32]})
			if sent == nil {
				if err := writeDurable(marker, []byte(card), 0o600); err != nil {
					return Errf("internal", "card marker: %v", err)
				}
			}
			return nil
		})
		source := "core-" + sha256Hex([]byte(e.ID))[:24]
		switch {
		case sent == nil:
			w.resolve(ctx, rt, "relay-overflow-"+source)
			w.resolve(ctx, rt, "relay-failed-"+source)
		case sent.Code == "budget_full":
			w.alert(ctx, rt, "relay-overflow-"+source, "Side-agent cards need inspection", "Source relay budget unavailable; the card stays on the agent's screen for a person.",
				&notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: AttentionSideAgentCards,
					Detail: "the adjutant's side-agent budget is full; " + e.Agent + "'s card is not relayed"})
		case sent.Code == "registry_pending" || sent.Code == "proof_stale":
			// The human boundary: nothing moves until a person acts, and
			// the card is relayed on a later pass.
		default:
			w.alert(ctx, rt, "relay-failed-"+source, "Side-agent relay failed", "The send was refused ("+sent.Code+"); inspect message status.",
				&notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: AttentionRelayFailed,
					Detail: "the relay of " + e.Agent + "'s side-agent card was refused: " + sent.Code})
		}
	}
	// A card no longer on the screen may be relayed again when it returns.
	_ = rt.do(ctx, func() *APIError {
		files, _ := os.ReadDir(rt.path("state"))
		for _, f := range files {
			name := f.Name()
			k, ok := strings.CutPrefix(name, "card-visible-")
			if !ok || visible[k] {
				continue
			}
			b, err := readSmall(rt.path("state", name), 4096)
			if err != nil || sha256Hex([]byte(key+"\n"+string(b))) != k {
				continue // another member's card
			}
			_ = os.Remove(rt.path("state", name))
		}
		return nil
	})
}

// pair is the route's approved orchestrator and adjutant: the relay's sender
// and receiver, as on the TUI.
func (w *cardWatcher) pair(rt *Route) (from, to string) {
	_ = rt.do(context.Background(), func() *APIError {
		peers, ok := rt.approvedPeers()
		if !ok {
			return nil
		}
		for _, p := range peers {
			switch p.Role {
			case "orchestrator":
				from = p.Name
			case "adjutant":
				to = p.Name
			}
		}
		return nil
	})
	return from, to
}

// blocked raises needs_attention for a dialog when it is first seen, and an
// alert when the next pass sees the same one, as the TUI's card watch
// confirms a sighting before it alerts. It is resolved when it is gone.
func (w *cardWatcher) blocked(ctx context.Context, rt *Route, e runtime.Execution, key string, lines []string) {
	scan := lines
	if e.Observation.Program == runtime.ProgramWorking && len(scan) > 5 {
		scan = scan[len(scan)-5:]
	}
	reason := ""
	if m := dialogRe.FindStringSubmatch(strings.Join(scan, "\n")); m != nil {
		reason = strings.TrimSpace(m[3])
	} else if e.Observation.Program == runtime.ProgramBlocked {
		reason = "the program reports blocked"
	}
	prev, had := w.seen[e.ID]
	if reason == "" {
		delete(w.seen, e.ID)
		if had {
			w.resolve(ctx, rt, "blocked-"+sha256Hex([]byte(key + ":" + prev))[:32])
		}
		return
	}
	alertKey := "blocked-" + sha256Hex([]byte(key + ":" + reason))[:32]
	if had && prev == reason {
		// Confirmed: the alert is recorded for the route's status. Its event
		// was raised at the first sighting.
		w.alert(ctx, rt, alertKey, "Agent notice or blocked state", "Inspect agent "+e.Agent+" in the app. No prompt was answered; a timeout remains a refusal.", nil)
		return
	}
	if had {
		w.resolve(ctx, rt, "blocked-"+sha256Hex([]byte(key + ":" + prev))[:32])
	}
	w.seen[e.ID] = reason
	w.s.c.d.emitNotice(notice.Event{Kind: notice.EventNeedsAttention, Repo: e.Repo, Route: e.Route, Execution: e.ID, Reason: "dialog",
		Detail: fmt.Sprintf("%s shows a dialog only a person ends (%s); nothing is typed into it", e.Agent, reason)})
}
