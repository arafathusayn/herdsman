package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
)

// corePair opens harbor's daemon channel with an approved adjutant and
// orchestrator on core.
func corePair(t *testing.T, h *harness, w *coretest.World) (repo, box string, adj, orc runtime.Execution) {
	t.Helper()
	ctx := context.Background()
	repo = filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	adj = coreParty(t, h, w, repo, execAdj, "proj-harbor-adjutant", "adjutant", 1)
	orc = coreParty(t, h, w, repo, execOrc, "proj-harbor-orchestrator", "orchestrator", 1)
	ch := coreChannel{h.d}
	box = filepath.Join(h.ph, "proj-harbor")
	if err := ch.Open(ctx, route.ChannelOpen{Route: "harbor", Repo: repo, Inbox: box, PolicyFiles: []string{h.rules}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []runtime.Execution{adj, orc} {
		if err := ch.Enroll(ctx, "harbor", e.Agent, e.Role, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := ch.Approve(ctx, "harbor", []runtime.Execution{adj, orc}); err != nil {
		t.Fatal(err)
	}
	return repo, box, adj, orc
}

func setScreen(w *coretest.World, addr, screen string) {
	w.Core.Mutate(addr, func(t map[string]any) { t["test_screen"] = screen })
}

// relayed are the side_agent_data messages queued for the adjutant.
func relayed(t *testing.T, h *harness, from, to string) []MessageHeader {
	t.Helper()
	rt, e := h.d.route("harbor")
	if e != nil {
		t.Fatal(e)
	}
	var out []MessageHeader
	_ = rt.do(context.Background(), func() *APIError {
		for _, m := range rt.fullStatus().Messages {
			if m.Kind == "side_agent_data" && m.From == from && m.To == to {
				out = append(out, m)
			}
		}
		return nil
	})
	return out
}

func pendingAlerts(t *testing.T, box, prefix string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(box, "alerts", prefix+"*.pending"))
	return files
}

func attention(t *testing.T, h *harness, reason, exec string) int {
	n := 0
	for _, ev := range runtimeEvents(t, h) {
		if ev.GetKind() == "needs_attention" && ev.GetReason() == reason && ev.GetExecutionId() == exec {
			n++
		}
	}
	return n
}

// The card watch on core does the TUI's: a side-agent card on a member's
// screen is relayed once to the adjutant as side_agent_data from the
// orchestrator, read through the runtime; too many cards, a dialog seen twice
// and a lost member raise alerts the route's status lists, each with one
// event, resolved when the cause is gone. Nothing is typed.
func TestCoreCardWatchRelaysCardsAndRaisesAlerts(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	repo, box, adj, orc := corePair(t, h, w)
	watch := &cardWatcher{s: h.d.coreRT().services, sp: route.ServiceSpec{Repo: repo, Route: "harbor", Name: route.ServiceCardWatch, Inbox: box},
		seen: map[string]string{}, raised: map[string]bool{}}
	pass := func() {
		t.Helper()
		if err := watch.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}

	setScreen(w, orc.TerminalAddress, "working on it\n ✦ Heads up: the build agent finished\n")
	pass()
	got := relayed(t, h, orc.Agent, adj.Agent)
	if len(got) != 1 {
		t.Fatalf("relayed %d cards: %+v", len(got), got)
	}
	body, err := os.ReadFile(filepath.Join(box, "ids", got[0].ID, "envelope"))
	if err != nil || !strings.Contains(string(body), "Heads up: the build agent finished") || !strings.Contains(string(body), execOrc) {
		t.Fatalf("the relayed envelope: %s %v", body, err)
	}
	pass() // the same card on the screen is not relayed again
	if n := len(relayed(t, h, orc.Agent, adj.Agent)); n != 1 {
		t.Fatalf("a visible card was relayed %d times", n)
	}

	// A fifth card is an overflow: an alert, one event, the first four relayed.
	setScreen(w, orc.TerminalAddress, strings.Repeat(" ✦ Heads up: a\n ✦ Heads up: b\n", 2)+" ✦ Heads up: c\n")
	pass()
	pass()
	if len(pendingAlerts(t, box, "card-overflow-")) != 1 || attention(t, h, AttentionSideAgentCards, execOrc) != 1 {
		t.Fatalf("overflow: alerts %v, events %d", pendingAlerts(t, box, "card-overflow-"), attention(t, h, AttentionSideAgentCards, execOrc))
	}
	setScreen(w, orc.TerminalAddress, "idle\n")
	pass()
	if len(pendingAlerts(t, box, "card-overflow-")) != 0 {
		t.Fatal("the overflow alert outlived its cause")
	}

	// A dialog: needs_attention at once, an alert when the next pass sees it
	// again, resolved when it is gone.
	setScreen(w, adj.TerminalAddress, "Edit file?\n❯ 1. Yes\n  2. No\n Do you want to proceed?")
	pass()
	if attention(t, h, "dialog", execAdj) != 1 || len(pendingAlerts(t, box, "blocked-")) != 0 {
		t.Fatalf("first sighting: events %d, alerts %v", attention(t, h, "dialog", execAdj), pendingAlerts(t, box, "blocked-"))
	}
	pass()
	pass()
	if attention(t, h, "dialog", execAdj) != 1 || len(pendingAlerts(t, box, "blocked-")) != 1 {
		t.Fatalf("confirmed: events %d, alerts %v", attention(t, h, "dialog", execAdj), pendingAlerts(t, box, "blocked-"))
	}
	if st := statusAlerts(t, h); !strings.Contains(st, "Agent notice or blocked state") {
		t.Fatalf("the route's status lists no alert: %s", st)
	}
	setScreen(w, adj.TerminalAddress, "done\n")
	pass()
	if len(pendingAlerts(t, box, "blocked-")) != 0 {
		t.Fatal("the dialog's alert outlived the dialog")
	}

	// A member whose process was lost is not read; it is an alert.
	lost := adj
	lost.Observation.Process = runtime.ProcessLost
	if err := h.d.recordExecution(ctx, lost); err != nil {
		t.Fatal(err)
	}
	pass()
	pass()
	if len(pendingAlerts(t, box, "watch-drift-")) != 1 || attention(t, h, AttentionIdentityLost, execAdj) != 1 {
		t.Fatalf("a lost member: alerts %v, events %d", pendingAlerts(t, box, "watch-drift-"), attention(t, h, AttentionIdentityLost, execAdj))
	}

	if n := w.Core.Calls("terminal.send") + w.Core.Calls("terminal.keys"); n != 0 {
		t.Errorf("the card watch typed %d times", n)
	}
}

// The relay waits at the human boundary: with a pair change waiting on a
// person, no card moves and no alert is raised.
func TestCoreCardWatchWaitsAtTheHumanBoundary(t *testing.T) {
	h, w := newCoreHarness(t)
	ctx := context.Background()
	repo, box, adj, orc := corePair(t, h, w)
	// A pending pair change holds every delivery.
	if err := os.WriteFile(filepath.Join(box, "peers"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watch := &cardWatcher{s: h.d.coreRT().services, sp: route.ServiceSpec{Repo: repo, Route: "harbor", Name: route.ServiceCardWatch, Inbox: box},
		seen: map[string]string{}, raised: map[string]bool{}}
	setScreen(w, orc.TerminalAddress, " ✦ Heads up: the build agent finished\n")
	if err := watch.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(relayed(t, h, orc.Agent, adj.Agent)); n != 0 {
		t.Fatalf("a held pair relayed %d cards", n)
	}
	if a := pendingAlerts(t, box, "relay-"); len(a) != 0 {
		t.Fatalf("the human boundary raised alerts: %v", a)
	}
}

func statusAlerts(t *testing.T, h *harness) string {
	t.Helper()
	rt, e := h.d.route("harbor")
	if e != nil {
		t.Fatal(e)
	}
	var b strings.Builder
	_ = rt.do(context.Background(), func() *APIError {
		for _, a := range rt.fullStatus().Alerts {
			b.Write(a)
		}
		return nil
	})
	return b.String()
}
