package hc_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
)

func TestGlobalEventsDoNotRequireATerminal(t *testing.T) {
	for _, kind := range []string{hc.EventDoc, "future-global-event"} {
		var event hc.Event
		if err := json.Unmarshal([]byte(`{"seq":1,"type":"`+kind+`","data":{}}`), &event); err != nil {
			t.Fatalf("global %s event broke the subscription: %v", kind, err)
		}
	}
	var event hc.Event
	if err := json.Unmarshal([]byte(`{"seq":1,"type":"status","data":{}}`), &event); err == nil {
		t.Fatal("a terminal status without its terminal was accepted")
	}
}

func subscribe(t *testing.T, core *hctest.Core, f hc.Filter, opts hc.Options) *hc.Subscription {
	t.Helper()
	core.Settle()
	s, err := hc.Subscribe(testCtx(t), core.Socket, f, opts)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func next(t *testing.T, s *hc.Subscription) hc.Update {
	t.Helper()
	u, err := s.Next(testCtx(t))
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return u
}

// An event that happened before the snapshot can arrive after it. Its
// sequence is not above the snapshot's, so it is dropped, and a later event
// still applies.
func TestEventRacingTheSnapshotIsDropped(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	a := run(t, c, map[string]string{"role": "impl"})
	run(t, c, nil)
	s := subscribe(t, core, hc.Filter{}, hc.Options{})
	seq := s.Seq()
	if seq == 0 || seq != core.Seq() {
		t.Fatalf("snapshot seq %d, core seq %d", seq, core.Seq())
	}
	core.EmitAt(seq, a.Address, hc.EventLabels, map[string]any{"labels": map[string]string{"role": "stale"}})
	core.EmitAt(seq-1, a.Address, hc.EventRemoved, map[string]any{})
	if _, err := c.Label(testCtx(t), a.Address, map[string]string{"role": "review"}); err != nil {
		t.Fatal(err)
	}
	u := next(t, s)
	if u.Kind != hc.UpdateEvent || u.Seq != seq+1 || u.Event.Type != hc.EventLabels {
		t.Fatalf("first update = %+v; the stale events were applied", u)
	}
	got, ok := s.Terminal(a.Address)
	if !ok || got.Labels["role"] != "review" {
		t.Fatalf("cache = %+v, %v", got, ok)
	}
	if len(s.Terminals()) != 2 {
		t.Fatalf("cache has %d terminals", len(s.Terminals()))
	}
}

// After a restart the core's sequence starts again at 0. A watch that carried
// the old sequence would drop every new event; it must start a new epoch
// from the fresh snapshot instead.
func TestRestartStartsANewEpochWithItsOwnSequence(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	a := run(t, c, map[string]string{"k": "1"})
	for range 5 {
		if _, err := c.Label(testCtx(t), a.Address, map[string]string{"n": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	updates := make(chan hc.Update, 32)
	ctx, cancel := context.WithCancel(testCtx(t))
	done := make(chan error, 1)
	go func() {
		done <- hc.Watch(ctx, core.Socket, hc.Filter{}, hc.Options{}, func(u hc.Update) error {
			updates <- u
			return nil
		})
	}()
	first := <-updates
	if first.Kind != hc.UpdateSnapshot || first.Seq != 6 {
		t.Fatalf("first update = %+v", first)
	}
	core.Restart()
	second := <-updates
	if second.Kind != hc.UpdateSnapshot || second.Seq != 0 || second.Epoch <= first.Epoch || len(second.Terminals) != 1 {
		t.Fatalf("after restart = %+v (first epoch %d)", second, first.Epoch)
	}
	c2 := dial(t, core)
	if _, err := c2.Label(testCtx(t), a.Address, map[string]string{"after": "restart"}); err != nil {
		t.Fatal(err)
	}
	third := <-updates
	if third.Kind != hc.UpdateEvent || third.Seq != 1 || third.Epoch != second.Epoch || third.Terminals[0].Labels["after"] != "restart" {
		t.Fatalf("event after restart = %+v", third)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("watch: %v", err)
	}
}

func TestDisconnectEndsTheSubscription(t *testing.T) {
	core := hctest.Start(t)
	s := subscribe(t, core, hc.Filter{}, hc.Options{})
	core.Restart()
	if _, err := s.Next(testCtx(t)); !errors.Is(err, hc.ErrDisconnected) {
		t.Fatalf("next after restart: %v", err)
	}
}

// missed replaces the cache with the fresh snapshot, opens a new epoch, and
// later events below its sequence are dropped.
func TestMissedReplacesTheCache(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	a := run(t, c, map[string]string{"k": "1"})
	b := run(t, c, map[string]string{"k": "1"})
	s := subscribe(t, core, hc.Filter{Labels: map[string]string{"k": "1"}}, hc.Options{})
	epoch := s.Epoch()
	core.Mutate(a.Address, func(t map[string]any) { t["title"] = "changed while behind" })
	core.Mutate(b.Address, func(t map[string]any) { t["labels"] = map[string]string{"k": "2"} })
	core.Missed()
	u := next(t, s)
	if u.Kind != hc.UpdateMissed || u.Epoch <= epoch || u.Seq != core.Seq() || s.Epoch() != u.Epoch {
		t.Fatalf("missed update = %+v (old epoch %d, core seq %d)", u, epoch, core.Seq())
	}
	if len(u.Terminals) != 1 || u.Terminals[0].Address != a.Address || u.Terminals[0].Title != "changed while behind" {
		t.Fatalf("cache after missed = %+v", u.Terminals)
	}
	core.EmitAt(u.Seq, a.Address, hc.EventTitle, map[string]any{"title": "stale"})
	if _, err := c.Keep(testCtx(t), a.Address, true); err != nil {
		t.Fatal(err)
	}
	u = next(t, s)
	if u.Event.Type != hc.EventKeep {
		t.Fatalf("update = %+v; the stale title was applied", u)
	}
	got, _ := s.Terminal(a.Address)
	if got.Title != "changed while behind" || !got.Keep {
		t.Fatalf("cache = %+v", got)
	}
}

func TestLabelChangesAreMembershipChanges(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	a := run(t, c, map[string]string{"herdsman.route": "r1"})
	b := run(t, c, nil)
	s := subscribe(t, core, hc.Filter{Labels: map[string]string{"herdsman.route": "r1"}}, hc.Options{})
	if _, err := c.Label(testCtx(t), a.Address, map[string]string{"herdsman.route": ""}); err != nil {
		t.Fatal(err)
	}
	if u := next(t, s); len(u.Terminals) != 0 {
		t.Fatalf("a terminal that lost the label stayed: %+v", u.Terminals)
	}
	if _, err := c.Label(testCtx(t), b.Address, map[string]string{"herdsman.route": "r1"}); err != nil {
		t.Fatal(err)
	}
	u := next(t, s)
	if len(u.Terminals) != 1 || u.Terminals[0].Address != b.Address {
		t.Fatalf("a terminal that gained the label is missing: %+v", u.Terminals)
	}
	if inc := s.Incomplete(); len(inc) != 1 || inc[0] != b.Address {
		t.Fatalf("incomplete = %v", inc)
	}
	core.Emit(b.Address, hc.EventCreated, core.Terminal(b.Address))
	next(t, s)
	if inc := s.Incomplete(); len(inc) != 0 {
		t.Fatalf("a full record left it incomplete: %v", inc)
	}
}

// A label event that brings a terminal into the filter may carry its full
// record under data.record, as core 0.1.20 sends it; the cache then holds
// the whole record and nothing is left for Get.
func TestALabelJoinWithItsRecordIsComplete(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	b := run(t, c, nil)
	s := subscribe(t, core, hc.Filter{Labels: map[string]string{"herdsman.route": "r1"}}, hc.Options{})
	core.Mutate(b.Address, func(t map[string]any) { t["labels"] = map[string]string{"herdsman.route": "r1"} })
	record := core.Terminal(b.Address)
	core.Emit(b.Address, hc.EventLabels, map[string]any{"labels": map[string]string{"herdsman.route": "r1"}, "record": record})
	u := next(t, s)
	if len(u.Terminals) != 1 || u.Terminals[0].Address != b.Address || u.Terminals[0].PID == nil {
		t.Fatalf("the joined terminal's record = %+v", u.Terminals)
	}
	if inc := s.Incomplete(); len(inc) != 0 {
		t.Fatalf("a join with its record left it incomplete: %v", inc)
	}
}

func TestEventsUpdateTheCache(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	s := subscribe(t, core, hc.Filter{}, hc.Options{})
	a := run(t, c, nil)
	if u := next(t, s); u.Event.Type != hc.EventCreated || len(u.Terminals) != 1 {
		t.Fatalf("created = %+v", u)
	}
	core.SetStatus(a.Address, "working")
	next(t, s)
	if got, _ := s.Terminal(a.Address); got.Status == nil || got.Status.State != "working" {
		t.Fatalf("status = %+v", got.Status)
	}
	core.Emit(a.Address, hc.EventForeground, map[string]any{"pid": 7, "name": "node", "argv": []string{"node"}, "cwd": "/w"})
	next(t, s)
	if got, _ := s.Terminal(a.Address); got.Foreground == nil || got.Foreground.Name != "node" {
		t.Fatalf("foreground = %+v", got.Foreground)
	}
	core.Emit(a.Address, "a_type_from_later", map[string]any{"cwd": "/elsewhere"})
	if u := next(t, s); u.Event.Type != "a_type_from_later" {
		t.Fatalf("unknown type = %+v", u)
	}
	if got, _ := s.Terminal(a.Address); got.Cwd != "/work" {
		t.Fatalf("an unknown event type changed the cache: %+v", got)
	}
	if _, err := c.Kill(testCtx(t), a.Address); err != nil {
		t.Fatal(err)
	}
	u := next(t, s)
	got, _ := s.Terminal(a.Address)
	if u.Event.Type != hc.EventFinished || got.Live() || got.Reason != "killed" || got.Status != nil && got.Status.State == "" {
		t.Fatalf("finished = %+v", got)
	}
	core.Emit(a.Address, hc.EventRemoved, map[string]any{})
	if u := next(t, s); len(u.Terminals) != 0 {
		t.Fatalf("removed terminal still cached: %+v", u.Terminals)
	}
	core.Emit("t77", hc.EventTitle, map[string]any{"title": "x"})
	next(t, s)
	if inc := s.Incomplete(); len(inc) != 1 || inc[0] != "t77" {
		t.Fatalf("an event for an unknown terminal: incomplete = %v", inc)
	}
}

func TestHeartbeatsKeepASubscriptionVerified(t *testing.T) {
	core := hctest.Start(t)
	c := dial(t, core)
	s := subscribe(t, core, hc.Filter{}, hc.Options{IdleTimeout: 200 * time.Millisecond})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 4 {
			time.Sleep(100 * time.Millisecond)
			core.Heartbeat()
		}
		run(t, c, nil)
	}()
	if u := next(t, s); u.Event.Type != hc.EventCreated {
		t.Fatalf("update = %+v", u)
	}
	wg.Wait()
	// Silence past the idle timeout makes it unverified.
	if _, err := s.Next(testCtx(t)); !errors.Is(err, hc.ErrUnverified) {
		t.Fatalf("next in silence: %v", err)
	}
}

func TestWatchRefusesAChangedCore(t *testing.T) {
	core := hctest.Start(t)
	err := hc.Watch(testCtx(t), core.Socket, hc.Filter{}, hc.Options{ExpectCore: "wiped"}, func(hc.Update) error { return nil })
	var changed *hc.CoreChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("watch: %v", err)
	}
}

func TestWatchStopsOnAMissingMethodAndOnItsCallback(t *testing.T) {
	core := hctest.Start(t)
	core.Drop(hc.MethodSubscribe)
	var missing *hc.MissingMethodError
	if err := hc.Watch(testCtx(t), core.Socket, hc.Filter{}, hc.Options{}, func(hc.Update) error { return nil }); !errors.As(err, &missing) {
		t.Fatalf("watch: %v", err)
	}
	core2 := hctest.Start(t)
	stop := errors.New("stop")
	if err := hc.Watch(testCtx(t), core2.Socket, hc.Filter{}, hc.Options{}, func(hc.Update) error { return stop }); err != stop {
		t.Fatalf("watch: %v", err)
	}
}

func TestSubscribeToAnUnknownTerminal(t *testing.T) {
	core := hctest.Start(t)
	if _, err := hc.Subscribe(testCtx(t), core.Socket, hc.Filter{Terminal: "t9"}, hc.Options{}); !hc.IsKind(err, hc.KindNotFound) {
		t.Fatalf("subscribe: %v", err)
	}
}
