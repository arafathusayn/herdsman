package hc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"slices"
	"sync/atomic"
	"time"
)

// Errors that end a subscription. Either way the ordering it held is gone: a
// new subscription starts a new epoch from a fresh snapshot.
var (
	// ErrDisconnected is a subscription whose connection ended (core stopped,
	// restarted, or dropped a subscriber that fell far behind).
	ErrDisconnected = errors.New("hc: subscription connection ended")
	// ErrUnverified is a subscription that heard nothing, not even a
	// heartbeat, for its idle timeout.
	ErrUnverified = errors.New("hc: subscription silent past its idle timeout")
)

// Filter narrows a subscription. The zero value follows every terminal.
type Filter struct {
	Labels   map[string]string // only terminals carrying all of these
	Terminal string            // only this terminal
}

func (f Filter) matches(t Terminal) bool {
	if f.Terminal != "" && t.Address != f.Terminal && t.Name != f.Terminal {
		return false
	}
	for k, v := range f.Labels {
		if t.Labels[k] != v {
			return false
		}
	}
	return true
}

type subscribeParams struct {
	Labels   map[string]string `json:"labels,omitempty"`
	Terminal string            `json:"terminal,omitempty"`
}

// snapshot is the answer to events.subscribe and the body of a missed
// notification: the matching terminals and the sequence they are current at.
type snapshot struct {
	Seq       uint64          `json:"seq"`
	Terminals []Terminal      `json:"terminals"`
	Channel   json.RawMessage `json:"channel"`
}

func (s *snapshot) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "snapshot", "seq!", "terminals!"); err != nil {
		return err
	}
	type plain snapshot
	return json.Unmarshal(b, (*plain)(s))
}

// Event is one fact change. Seq belongs to the subscription's epoch and means
// nothing in another one.
type Event struct {
	Seq      uint64          `json:"seq"`
	Terminal string          `json:"terminal"`
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
}

func (e *Event) UnmarshalJSON(b []byte) error {
	if err := requireFields(b, "event", "seq!", "terminal", "type!"); err != nil {
		return err
	}
	type plain Event
	return json.Unmarshal(b, (*plain)(e))
}

// UpdateKind says what an Update carries.
type UpdateKind int

const (
	// UpdateSnapshot opens an epoch: the cache is the snapshot.
	UpdateSnapshot UpdateKind = iota + 1
	// UpdateEvent is one event applied within the current epoch.
	UpdateEvent
	// UpdateMissed opens an epoch after core said the subscriber fell
	// behind: the cache was replaced from the fresh snapshot, and anything
	// derived from the old cache must be reconciled again.
	UpdateMissed
)

// Update is one change a subscription delivers.
type Update struct {
	Kind      UpdateKind
	Epoch     uint64
	Seq       uint64
	Event     Event      // UpdateEvent only
	Terminals []Terminal // the cache after the update, by address
}

// epochs numbers local observation epochs across every subscription of this
// process, so an epoch is never reused after a reconnect.
var epochs atomic.Uint64

// Subscription is one subscription connection and the cache it keeps. It is
// read by one goroutine.
type Subscription struct {
	conn   net.Conn
	r      *bufio.Reader
	opts   Options
	filter Filter
	hello  HelloResult

	epoch   uint64
	seq     uint64
	cache   map[string]Terminal
	partial map[string]bool
}

// Subscribe opens a subscription connection: hello at the pinned generation,
// the capability check, then events.subscribe. The returned snapshot is the
// first epoch.
func Subscribe(ctx context.Context, socket string, filter Filter, opts Options) (*Subscription, error) {
	conn, r, hello, err := open(ctx, socket, opts)
	if err != nil {
		return nil, err
	}
	var snap snapshot
	err = syncCall(ctx, conn, r, opts, 1, MethodSubscribe, subscribeParams(filter), &snap)
	if err == nil && len(snap.Channel) > 0 && string(snap.Channel) != "null" {
		err = fmt.Errorf("hc %s: core answered with a framed channel", MethodSubscribe)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	s := &Subscription{conn: conn, r: r, opts: opts, filter: filter, hello: hello}
	s.install(snap)
	return s, nil
}

// install replaces the cache with a snapshot and opens a new epoch at its
// sequence.
func (s *Subscription) install(snap snapshot) {
	s.epoch = epochs.Add(1)
	s.seq = snap.Seq
	s.cache = make(map[string]Terminal, len(snap.Terminals))
	s.partial = map[string]bool{}
	for _, t := range snap.Terminals {
		s.cache[t.Address] = t
	}
}

// Core returns the identity of the core this subscription follows.
func (s *Subscription) Core() CoreInfo { return s.hello.Core }

// Epoch returns the current local epoch.
func (s *Subscription) Epoch() uint64 { return s.epoch }

// Seq returns the last sequence applied in the current epoch.
func (s *Subscription) Seq() uint64 { return s.seq }

// Terminals returns the cache, by address.
func (s *Subscription) Terminals() []Terminal {
	return slices.SortedFunc(maps.Values(s.cache), func(a, b Terminal) int {
		if a.Address < b.Address {
			return -1
		}
		if a.Address > b.Address {
			return 1
		}
		return 0
	})
}

// Terminal returns one cached terminal.
func (s *Subscription) Terminal(address string) (Terminal, bool) {
	t, ok := s.cache[address]
	return t, ok
}

// Incomplete lists the cached terminals whose record an event changed without
// carrying it whole, or that joined the filter by a label change: fetch them
// with Get.
func (s *Subscription) Incomplete() []string {
	return slices.Sorted(maps.Keys(s.partial))
}

// Snapshot returns the current epoch's state as an UpdateSnapshot.
func (s *Subscription) Snapshot() Update {
	return Update{Kind: UpdateSnapshot, Epoch: s.epoch, Seq: s.seq, Terminals: s.Terminals()}
}

// Close ends the subscription.
func (s *Subscription) Close() error { return s.conn.Close() }

// Next waits for the next event or missed snapshot and applies it. Heartbeats
// and events from before the epoch's snapshot are consumed silently. An
// ended context closes the subscription.
func (s *Subscription) Next(ctx context.Context) (Update, error) {
	stop := context.AfterFunc(ctx, func() { _ = s.conn.Close() })
	defer stop()
	idle := s.opts.idleTimeout()
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(idle))
		line, err := readFrame(s.r, s.opts.maxFrame())
		if err != nil {
			_ = s.conn.Close()
			var ne net.Error
			switch {
			case ctx.Err() != nil:
				return Update{}, ctx.Err()
			case errors.As(err, &ne) && ne.Timeout():
				return Update{}, ErrUnverified
			case errors.Is(err, ErrFrameTooLarge):
				return Update{}, err
			}
			return Update{}, fmt.Errorf("%w: %w", ErrDisconnected, err)
		}
		f, err := parseFrame(line)
		if err == nil && f.kind != frameNotification {
			err = badFrame("a response on a subscription connection")
		}
		if err != nil {
			_ = s.conn.Close()
			return Update{}, err
		}
		switch f.method {
		case NotifyEvent:
			var ev Event
			if err := json.Unmarshal(f.params, &ev); err != nil {
				_ = s.conn.Close()
				return Update{}, err
			}
			// A sequence at or below the snapshot's was already in it. A gap
			// is not a failure: the stream is filtered.
			if ev.Seq <= s.seq {
				continue
			}
			s.seq = ev.Seq
			s.apply(ev)
			return Update{Kind: UpdateEvent, Epoch: s.epoch, Seq: s.seq, Event: ev, Terminals: s.Terminals()}, nil
		case NotifyMissed:
			var snap snapshot
			if err := json.Unmarshal(f.params, &snap); err != nil {
				_ = s.conn.Close()
				return Update{}, err
			}
			s.install(snap)
			return Update{Kind: UpdateMissed, Epoch: s.epoch, Seq: s.seq, Terminals: s.Terminals()}, nil
		default:
			// heartbeat, or a notification a later core adds: traffic only.
		}
	}
}

// apply folds one event into the cache.
func (s *Subscription) apply(ev Event) {
	addr := ev.Terminal
	if ev.Type == EventRemoved {
		delete(s.cache, addr)
		delete(s.partial, addr)
		return
	}
	if !slices.Contains(EventTypes, ev.Type) || ev.Type == EventDoc || addr == "" {
		return
	}
	if full, ok := fullRecord(ev.Data); ok {
		s.put(full)
		delete(s.partial, full.Address)
		return
	}
	t, known := s.cache[addr]
	if !known {
		if ev.Type == EventLabels {
			var d struct {
				Labels map[string]string `json:"labels"`
				Record json.RawMessage   `json:"record"`
			}
			if json.Unmarshal(ev.Data, &d) == nil && s.filter.matches(Terminal{Address: addr, Labels: d.Labels}) {
				// It joined the filter. Core may send its full record with the
				// event; otherwise the record comes from Get.
				if full, ok := fullRecord(d.Record); ok && full.Address == addr {
					s.put(full)
					delete(s.partial, addr)
					return
				}
				s.cache[addr] = Terminal{Address: addr, Labels: d.Labels, State: "live"}
				s.partial[addr] = true
			}
			return
		}
		s.partial[addr] = true
		return
	}
	switch ev.Type {
	case EventForeground:
		var fg *Foreground
		if json.Unmarshal(ev.Data, &fg) == nil {
			t.Foreground = fg
		} else {
			s.partial[addr] = true
		}
	case EventStatus:
		var st Status
		if requireFields(ev.Data, "status", "state!") == nil && json.Unmarshal(ev.Data, &st) == nil {
			t.Status = &st
		} else if !merge(&t, ev.Data) {
			s.partial[addr] = true
		}
	default:
		if !merge(&t, ev.Data) {
			s.partial[addr] = true
		}
		if ev.Type == EventFinished {
			t.State = "finished"
			t.Foreground = nil
		}
	}
	s.put(t)
}

// put stores a record, or drops it when it no longer matches the filter (a
// label removal is a membership change).
func (s *Subscription) put(t Terminal) {
	if !s.filter.matches(t) {
		delete(s.cache, t.Address)
		delete(s.partial, t.Address)
		return
	}
	s.cache[t.Address] = t
}

// fullRecord reads event data that carries a whole terminal record.
func fullRecord(data json.RawMessage) (Terminal, bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) != nil {
		return Terminal{}, false
	}
	if _, ok := probe["address"]; !ok {
		return Terminal{}, false
	}
	var t Terminal
	if json.Unmarshal(data, &t) != nil {
		return Terminal{}, false
	}
	return t, true
}

// merge lays the fields of an event's data object over a record. It reports
// false when the data is not an object or does not fit the record.
func merge(t *Terminal, data json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	base, err := json.Marshal(t)
	if err != nil {
		return false
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(base, &record) != nil {
		return false
	}
	maps.Copy(record, fields)
	merged, err := json.Marshal(record)
	if err != nil {
		return false
	}
	type plain Terminal
	var out plain
	if json.Unmarshal(merged, &out) != nil {
		return false
	}
	*t = Terminal(out)
	return true
}

// Retry bounds for Watch: jittered exponential backoff (spec §11).
const (
	retryMin = 250 * time.Millisecond
	retryMax = 10 * time.Second
)

// handlerError marks an error returned by Watch's callback.
type handlerError struct{ err error }

func (e handlerError) Error() string { return e.err.Error() }
func (e handlerError) Unwrap() error { return e.err }

// Watch follows a core until ctx ends: it subscribes, hands fn the snapshot
// of each new epoch and then every update, and after a disconnect or a
// silent connection subscribes again from a fresh snapshot. No sequence is
// carried from one connection to the next. The core id seen first is pinned
// (or opts.ExpectCore); a changed core, another generation, a missing method
// or an error from fn ends the watch.
func Watch(ctx context.Context, socket string, filter Filter, opts Options, fn func(Update) error) error {
	wait := retryMin
	for {
		s, err := Subscribe(ctx, socket, filter, opts)
		if err == nil {
			opts.ExpectCore = s.Core().ID
			wait = retryMin
			err = follow(ctx, s, fn)
			_ = s.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if permanent(err) {
			var h handlerError
			if errors.As(err, &h) {
				return h.err
			}
			return err
		}
		jitter := time.Duration(rand.Int64N(int64(wait)/2 + 1))
		t := time.NewTimer(wait/2 + jitter)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		wait = min(wait*2, retryMax)
	}
}

func follow(ctx context.Context, s *Subscription, fn func(Update) error) error {
	if err := fn(s.Snapshot()); err != nil {
		return handlerError{err}
	}
	for {
		u, err := s.Next(ctx)
		if err != nil {
			return err
		}
		if err := fn(u); err != nil {
			return handlerError{err}
		}
	}
}

// permanent reports whether retrying cannot help.
func permanent(err error) bool {
	var (
		h  handlerError
		g  *GenerationError
		m  *MissingMethodError
		c  *CoreChangedError
		mf *MissingFieldError
		fe *FrameError
	)
	return errors.As(err, &h) || errors.As(err, &g) || errors.As(err, &m) || errors.As(err, &c) ||
		errors.As(err, &mf) || errors.As(err, &fe) || errors.Is(err, ErrFrameTooLarge)
}
