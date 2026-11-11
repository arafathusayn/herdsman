package hc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hclive"
)

// nextUntil reads updates until cond holds of the subscription's cache.
func nextUntil(t *testing.T, ctx context.Context, s *hc.Subscription, what string, cond func() bool) {
	t.Helper()
	for !cond() {
		if _, err := s.Next(ctx); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
}

// The Events gate (spec section 17) against a private real core: the
// snapshot and an event that races it, a label membership change both ways,
// a finished record, a sequence that starts again after a core restart, and
// a subscriber that falls behind. Remote records need a second machine and
// are left to the fake core.
func TestLiveEvents(t *testing.T) {
	pc := hclive.Start(t, hclive.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := hc.Dial(ctx, pc.Socket, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	labels := map[string]string{"herdsman.live": "events"}
	run := func(name string, labels map[string]string) hc.Terminal {
		t.Helper()
		term, err := c.Run(ctx, hc.RunRequest{Argv: []string{"/bin/sleep", "300"}, Labels: labels, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return term
	}

	// Snapshot and event race: one terminal before the subscription, one
	// right after; each is seen once, in the snapshot or as an event.
	before := run("ev-before", labels)
	s, err := hc.Subscribe(ctx, pc.Socket, hc.Filter{Labels: labels}, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := run("ev-after", labels)
	if _, ok := s.Terminal(before.Address); !ok {
		t.Fatal("the snapshot lacks the terminal started before it")
	}
	nextUntil(t, ctx, s, "the terminal started after the snapshot", func() bool { _, ok := s.Terminal(after.Address); return ok })

	// Label membership: a terminal joins the filter when labelled and
	// leaves it when the label is removed.
	plain := run("ev-plain", nil)
	if _, err := c.Label(ctx, plain.Address, labels); err != nil {
		t.Fatal(err)
	}
	nextUntil(t, ctx, s, "join by label", func() bool { _, ok := s.Terminal(plain.Address); return ok })
	if _, err := c.Label(ctx, plain.Address, map[string]string{"herdsman.live": ""}); err != nil {
		t.Fatal(err)
	}
	nextUntil(t, ctx, s, "leave by label", func() bool { _, ok := s.Terminal(plain.Address); return !ok })

	// A finished record.
	if _, err := c.Kill(ctx, after.Address); err != nil {
		t.Fatal(err)
	}
	nextUntil(t, ctx, s, "the end", func() bool { got, ok := s.Terminal(after.Address); return ok && !got.Live() })
	if inc := s.Incomplete(); len(inc) != 0 {
		t.Errorf("records left incomplete: %v", inc)
	}

	// A core restart ends the subscription; a new one opens a new epoch
	// from a fresh snapshot and applies what follows, whatever its sequence.
	epoch, seq := s.Epoch(), s.Seq()
	_ = c.Close()
	pc.Restart()
	for {
		_, err := s.Next(ctx)
		if errors.Is(err, hc.ErrDisconnected) {
			break
		}
		if err != nil {
			t.Fatalf("after the restart: %v", err)
		}
	}
	s2, err := hc.Subscribe(ctx, pc.Socket, hc.Filter{Labels: labels}, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Epoch() <= epoch {
		t.Fatalf("epoch %d after %d", s2.Epoch(), epoch)
	}
	t.Logf("sequence %d before the restart, %d after", seq, s2.Seq())
	if c, err = hc.Dial(ctx, pc.Socket, hc.Options{}); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fresh := run("ev-fresh", labels)
	nextUntil(t, ctx, s2, "an event after the restart", func() bool { _, ok := s2.Terminal(fresh.Address); return ok })

	// A subscriber that falls behind: core either sends a fresh snapshot
	// (missed) or drops it; either way the next view is the truth.
	s3, err := hc.Subscribe(ctx, pc.Socket, hc.Filter{Labels: labels}, hc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	const n = 3000
	for i := range n {
		if _, err := c.Label(ctx, fresh.Address, map[string]string{"herdsman.n": fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	final := fmt.Sprint(n - 1)
	missed, events := false, 0
	for {
		got, ok := s3.Terminal(fresh.Address)
		if ok && got.Labels["herdsman.n"] == final {
			break
		}
		u, err := s3.Next(ctx)
		if errors.Is(err, hc.ErrDisconnected) {
			t.Log("core dropped the subscriber that fell behind")
			s4, err := hc.Subscribe(ctx, pc.Socket, hc.Filter{Labels: labels}, hc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s4.Close()
			if got, ok := s4.Terminal(fresh.Address); !ok || got.Labels["herdsman.n"] != final {
				t.Fatalf("the new snapshot is not the truth: %+v", got)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if u.Kind == hc.UpdateMissed {
			missed = true
		} else {
			events++
		}
	}
	t.Logf("after %d label changes: missed %v, %d events read", n, missed, events)
}
