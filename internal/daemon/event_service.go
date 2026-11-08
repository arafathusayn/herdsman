package daemon

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/events"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// Limits of the event hub and of what one caller can hold.
const (
	// Each topic keeps this much history for subscribers to read.
	topicRetainEvents = 256
	topicRetainBytes  = 4 << 20
	// A subscriber this far behind a live topic is told it lagged. It is
	// smaller than the history so that resuming loses nothing.
	subscriberLagEvents = 64
	maxHubTopics        = 1024
	// maxEventTopics bounds one connection's or one Wait's topics.
	maxEventTopics = 64
	// One Wait reply.
	maxWaitEvents = 64
	maxWaitReply  = 2 << 20
	maxWaitHold   = 30 * time.Second
	// One writing pass over one topic, so that no topic starves the others.
	streamPassEvents = 16
	streamPassBytes  = 1 << 20
	streamPing       = 20 * time.Second
	// maxPendingPongs bounds the answers owed to a client that pings faster
	// than it reads.
	maxPendingPongs = 16
	eventSlotKey    = "events"
)

// Reasons a Lagged frame gives.
const (
	lagQueueFull     = "queue_full"
	lagCursorExpired = "cursor_expired"
	lagEpochChanged  = "epoch_changed"
)

// eventHub holds a bounded history per topic. Publishing never waits for a
// reader: readers chase the history and the hub's memory does not depend on
// how many there are or how slow they are.
type eventHub struct {
	mu      sync.Mutex
	topics  map[string]*eventTopic
	changed chan struct{}
	stopped chan struct{}
	closed  bool
	clock   uint64

	retain      int
	retainBytes int
	lag         int64
	maxTopics   int
	ping        time.Duration
	// beforeWrite, when set, runs before each writing pass; tests hold a
	// connection's writer with it.
	beforeWrite func()
}

type eventTopic struct {
	epoch   string
	seq     int64
	ring    []*daemonv1.Event
	bytes   int
	touched uint64
}

func newEventHub() *eventHub {
	return &eventHub{topics: map[string]*eventTopic{}, changed: make(chan struct{}), stopped: make(chan struct{}),
		retain: topicRetainEvents, retainBytes: topicRetainBytes, lag: subscriberLagEvents, maxTopics: maxHubTopics, ping: streamPing}
}

// close ends every stream and long poll.
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.stopped)
	}
}

func (h *eventHub) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

// watch is closed by the next publish. Take it before reading, so that an
// event published after the read still wakes the reader.
func (h *eventHub) watch() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.changed
}

// publish appends one event and returns its cursor. A publisher splits a large
// payload; the hub does not. After close it drops the event and returns "".
func (h *eventHub) publish(topic, kind string, payload []byte) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ""
	}
	t := h.topics[topic]
	if t == nil {
		h.evict()
		t = &eventTopic{epoch: randomHex(16)}
		h.topics[topic] = t
	}
	h.clock++
	t.touched = h.clock
	t.seq++
	event := &daemonv1.Event{Topic: topic, Cursor: events.FormatCursor(t.epoch, t.seq), Type: kind, Payload: payload}
	t.ring = append(t.ring, event)
	t.bytes += len(payload)
	for len(t.ring) > h.retain || (t.bytes > h.retainBytes && len(t.ring) > 1) {
		t.bytes -= len(t.ring[0].GetPayload())
		t.ring = t.ring[1:]
	}
	close(h.changed)
	h.changed = make(chan struct{})
	return event.GetCursor()
}

// evict forgets the least recently published topic when the table is full. A
// reader of it is told the epoch changed.
func (h *eventHub) evict() {
	if len(h.topics) < h.maxTopics {
		return
	}
	oldest, at := "", ^uint64(0)
	for name, t := range h.topics {
		if t.touched < at {
			oldest, at = name, t.touched
		}
	}
	delete(h.topics, oldest)
}

// eventPosition is a place in a topic. An empty epoch means the topic's start.
type eventPosition struct {
	epoch string
	seq   int64
}

func (p eventPosition) cursor() string {
	if p.epoch == "" {
		return ""
	}
	return events.FormatCursor(p.epoch, p.seq)
}

func parsePosition(cursor string) (eventPosition, bool) {
	if cursor == "" {
		return eventPosition{}, true
	}
	epoch, seq, ok := events.ParseCursor(cursor)
	return eventPosition{epoch, seq}, ok
}

// eventRead is what one read of a topic found.
type eventRead struct {
	events []*daemonv1.Event
	next   eventPosition
	lagged *daemonv1.Lagged
}

// head is the topic's newest sequence, or zero for a topic with no events.
func (h *eventHub) head(topic string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.topics[topic]; t != nil {
		return t.seq
	}
	return 0
}

// read returns events after p up to the limits. A limit of zero on lag turns
// the lag check off; barrier is the head when the reader subscribed, so that
// catching up on history never counts as falling behind.
func (h *eventHub) read(topic string, p eventPosition, maxEvents, maxBytes int, lag, barrier int64) eventRead {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.topics[topic]
	lost := func(reason string, next eventPosition) eventRead {
		return eventRead{next: next, lagged: &daemonv1.Lagged{Topic: topic, Cursor: next.cursor(), Reason: reason}}
	}
	if t == nil {
		if p.epoch != "" {
			return lost(lagEpochChanged, eventPosition{})
		}
		return eventRead{next: p}
	}
	// The ring ends at seq, so its length gives the position before it.
	start := eventPosition{t.epoch, t.seq - int64(len(t.ring))}
	switch {
	case p.epoch == "":
		p = start
	case p.epoch != t.epoch:
		return lost(lagEpochChanged, start)
	case p.seq > t.seq || p.seq < start.seq:
		return lost(lagCursorExpired, start)
	}
	if lag > 0 && t.seq-max(p.seq, barrier) > lag {
		return lost(lagQueueFull, p)
	}
	first := int(p.seq - start.seq)
	out := eventRead{next: p}
	size := 0
	for _, event := range t.ring[first:] {
		if len(out.events) >= maxEvents || (size > 0 && size+len(event.GetPayload()) > maxBytes) {
			break
		}
		out.events = append(out.events, event)
		size += len(event.GetPayload())
		out.next.seq++
	}
	return out
}

// eventService serves the topics of a hub over the stream and the long poll.
type eventService struct {
	hub *eventHub
	// admit takes a slot for a stream or a long poll. Nil admits all.
	admit func(key string) (release func(), err error)
	// attached runs once a stream or a long poll has settled in, so that it
	// stops counting against the daemon's requests in flight.
	attached func(context.Context)

	mu    sync.Mutex
	conns map[*eventConn]struct{}
}

func newEventService(hub *eventHub, admit func(string) (func(), error), attached func(context.Context)) *eventService {
	if admit == nil {
		admit = func(string) (func(), error) { return func() {}, nil }
	}
	if attached == nil {
		attached = func(context.Context) {}
	}
	return &eventService{hub: hub, admit: admit, attached: attached, conns: map[*eventConn]struct{}{}}
}

// procedures are the service's two paths on the RPC mux.
func (s *eventService) procedures() []rpc.Procedure {
	return []rpc.Procedure{
		rpc.Unary(daemonv1.EventServiceName, "Wait", s.Wait),
		{Path: events.Path, Handler: http.HandlerFunc(s.stream)},
	}
}

// Wait answers with the events after each cursor, as soon as there are any.
func (s *eventService) Wait(ctx context.Context, req *daemonv1.EventServiceWaitRequest) (*daemonv1.EventServiceWaitResponse, error) {
	topics := req.GetTopics()
	if len(topics) == 0 || len(topics) > maxEventTopics {
		return nil, rpc.Errorf(rpc.InvalidArgument, "wait takes 1 to %d topics", maxEventTopics)
	}
	positions := make([]eventPosition, len(topics))
	seen := make(map[string]bool, len(topics))
	for i, topic := range topics {
		name := topic.GetTopic()
		position, ok := parsePosition(topic.GetCursor())
		if !events.ValidTopic(name) || !ok || seen[name] {
			return nil, rpc.Errorf(rpc.InvalidArgument, "bad topic or cursor: %q %q", name, topic.GetCursor())
		}
		positions[i], seen[name] = position, true
	}
	release, err := s.admit(eventSlotKey)
	if err != nil {
		return nil, err
	}
	defer release()
	s.attached(ctx)

	hold := min(time.Duration(req.GetWaitMs())*time.Millisecond, maxWaitHold)
	timer := time.NewTimer(hold)
	defer timer.Stop()
	for {
		changed := s.hub.watch()
		res := s.collect(topics, positions)
		if len(res.GetEvents()) > 0 || len(res.GetLagged()) > 0 || hold <= 0 {
			return res, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			return res, nil
		case <-s.hub.stopped:
			return res, nil
		case <-ctx.Done():
			return nil, rpc.Errorf(rpc.Canceled, "wait ended: %v", ctx.Err())
		}
	}
}

// collect reads every requested topic into one bounded reply.
func (s *eventService) collect(topics []*daemonv1.TopicCursor, positions []eventPosition) *daemonv1.EventServiceWaitResponse {
	res := new(daemonv1.EventServiceWaitResponse)
	budget, bytes := maxWaitEvents, maxWaitReply
	for i, topic := range topics {
		found := s.hub.read(topic.GetTopic(), positions[i], max(budget, 0), max(bytes, 1), 0, 0)
		if found.lagged != nil {
			res.Lagged = append(res.Lagged, found.lagged)
		}
		res.Events = append(res.Events, found.events...)
		res.Cursors = append(res.Cursors, &daemonv1.TopicCursor{Topic: topic.GetTopic(), Cursor: found.next.cursor()})
		budget -= len(found.events)
		for _, event := range found.events {
			bytes -= len(event.GetPayload())
		}
	}
	return res
}

// eventSubscription is one topic of one stream.
type eventSubscription struct {
	position eventPosition
	barrier  int64
}

// eventConn is one open stream.
type eventConn struct {
	service *eventService
	conn    *events.Conn
	wake    chan struct{}
	gone    chan struct{}

	mu    sync.Mutex
	subs  map[string]*eventSubscription
	pongs []string
}

// stream serves one WebSocket until either side ends it.
func (s *eventService) stream(w http.ResponseWriter, r *http.Request) {
	release, err := s.admit(eventSlotKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	defer release()
	conn, err := events.Accept(w, r, events.AcceptOptions{})
	if err != nil {
		return
	}
	s.attached(r.Context())
	c := &eventConn{service: s, conn: conn, wake: make(chan struct{}, 1), gone: make(chan struct{}), subs: map[string]*eventSubscription{}}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	go c.readLoop()
	c.writeLoop()
	conn.Close(events.CloseNormal, "")
	<-c.gone
}

// readLoop applies the client's frames until the connection ends.
func (c *eventConn) readLoop() {
	defer close(c.gone)
	for {
		frame, err := c.conn.Read()
		if err != nil {
			return
		}
		if reason := c.apply(frame); reason != "" {
			c.conn.Close(events.ClosePolicyViolated, reason)
			return
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// apply takes one client frame and returns why it cannot be served, if so. A
// frame of a kind this daemon does not know is ignored.
func (c *eventConn) apply(frame *daemonv1.StreamFrame) string {
	var pongs []string
	defer func() {
		for _, nonce := range pongs {
			c.queuePing(nonce)
		}
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	switch body := frame.GetFrame().(type) {
	case *daemonv1.StreamFrame_Subscribe:
		topic := body.Subscribe.GetTopic()
		position, ok := parsePosition(body.Subscribe.GetCursor())
		if !events.ValidTopic(topic) || !ok {
			return "bad topic or cursor"
		}
		if _, have := c.subs[topic]; !have && len(c.subs) >= maxEventTopics {
			return "too many topics"
		}
		c.subs[topic] = &eventSubscription{position: position, barrier: c.service.hub.head(topic)}
	case *daemonv1.StreamFrame_Unsubscribe:
		delete(c.subs, body.Unsubscribe.GetTopic())
	case *daemonv1.StreamFrame_Ping:
		pongs = append(pongs, body.Ping.GetNonce())
	case *daemonv1.StreamFrame_Event, *daemonv1.StreamFrame_Lagged:
		return "a client does not send this frame"
	}
	return ""
}

// writeLoop sends what the subscriptions have until the connection or the hub
// ends.
func (c *eventConn) writeLoop() {
	hub := c.service.hub
	ping := time.NewTicker(hub.ping)
	defer ping.Stop()
	for {
		changed := hub.watch()
		if hub.beforeWrite != nil {
			hub.beforeWrite()
		}
		wrote, err := c.pass()
		if err != nil {
			return
		}
		if wrote && !hub.isClosed() {
			continue
		}
		select {
		case <-changed:
		case <-c.wake:
		case <-c.gone:
			return
		case <-hub.stopped:
			c.conn.Close(events.CloseGoingAway, "daemon stopping")
			return
		case <-ping.C:
			c.queuePing("")
		}
	}
}

func (c *eventConn) queuePing(nonce string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pongs) < maxPendingPongs {
		c.pongs = append(c.pongs, nonce)
	}
}

func pingFrame(nonce string) *daemonv1.StreamFrame {
	return &daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Ping{Ping: &daemonv1.Ping{Nonce: nonce}}}
}

// pass writes one bounded share of each subscribed topic. It reports whether
// it wrote anything.
func (c *eventConn) pass() (bool, error) {
	wrote := false
	for _, nonce := range c.takePongs() {
		if err := c.conn.Write(pingFrame(nonce)); err != nil {
			return false, err
		}
		wrote = true
	}
	hub := c.service.hub
	for _, item := range c.snapshot() {
		topic, sub := item.topic, item.sub
		found := hub.read(topic, sub.position, streamPassEvents, streamPassBytes, hub.lag, sub.barrier)
		if found.lagged != nil {
			if err := c.conn.Write(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Lagged{Lagged: found.lagged}}); err != nil {
				return false, err
			}
			c.finish(topic, sub)
			wrote = true
			continue
		}
		for _, event := range found.events {
			if err := c.conn.Write(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Event{Event: event}}); err != nil {
				return false, err
			}
			wrote = true
		}
		c.advance(sub, found.next)
	}
	return wrote, nil
}

func (c *eventConn) takePongs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	pongs := c.pongs
	c.pongs = nil
	return pongs
}

type namedSubscription struct {
	topic string
	sub   *eventSubscription
}

// snapshot lists the subscriptions in topic order.
func (c *eventConn) snapshot() []namedSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := make([]namedSubscription, 0, len(c.subs))
	for name, sub := range c.subs {
		items = append(items, namedSubscription{name, sub})
	}
	slices.SortFunc(items, func(a, b namedSubscription) int { return strings.Compare(a.topic, b.topic) })
	return items
}

// finish drops a subscription unless the client has replaced it meanwhile.
func (c *eventConn) finish(topic string, sub *eventSubscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs[topic] == sub {
		delete(c.subs, topic)
	}
}

func (c *eventConn) advance(sub *eventSubscription, next eventPosition) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sub.position = next
}

// registryView publishes what the daemon's routes do: the membership on one
// topic and each route's events on its own. It carries what a route's events
// carry, which never includes a message body.
type registryView struct {
	hub *eventHub

	mu    sync.Mutex
	feeds map[string]*routeFeed
}

func newRegistryView(hub *eventHub) *registryView {
	return &registryView{hub: hub, feeds: map[string]*routeFeed{}}
}

// attach starts publishing one route's events and announces it.
func (v *registryView) attach(rt *Route) {
	v.mu.Lock()
	defer v.mu.Unlock()
	feed := &routeFeed{hub: v.hub, rt: rt, stop: make(chan struct{}), done: make(chan struct{})}
	v.feeds[rt.name] = feed
	go feed.run()
	v.publishRegistry()
}

// remove stops a route's events and announces that it is gone.
func (v *registryView) remove(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	feed := v.feeds[name]
	if feed == nil {
		return
	}
	delete(v.feeds, name)
	feed.halt()
	v.publishEvent(events.RouteTopic(name), &Event{Type: "route_removed", Route: name})
	v.publishRegistry()
}

// close stops every feed.
func (v *registryView) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for name, feed := range v.feeds {
		feed.halt()
		delete(v.feeds, name)
	}
}

// publishRegistry announces the full membership, with each route's current
// epoch.
func (v *registryView) publishRegistry() {
	refs := []RouteRef{}
	for name, feed := range v.feeds {
		epoch := ""
		feed.rt.always(func() { epoch = feed.rt.epoch })
		refs = append(refs, RouteRef{Route: name, Epoch: epoch})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Route < refs[j].Route })
	v.publishEvent(events.TopicRegistry, &Event{Type: "registry", Routes: refs})
}

func (v *registryView) publishEvent(topic string, ev *Event) {
	ev.V, ev.Time = Protocol, time.Now().Unix()
	publishEncoded(v.hub, topic, ev)
}

func publishEncoded(hub *eventHub, topic string, ev *Event) {
	// An event is plain strings and numbers, which always encode.
	data, _ := encode(ev)
	hub.publish(topic, ev.Type, data)
}

// routeFeed copies one route's events into its topic.
type routeFeed struct {
	hub  *eventHub
	rt   *Route
	stop chan struct{}
	done chan struct{}
	// resume is where the first attach starts; empty means a snapshot.
	resume string

	mu  sync.Mutex
	sub *subscriber
}

func (f *routeFeed) halt() {
	close(f.stop)
	<-f.done
}

// run subscribes to the route and publishes until stopped or the route stops.
// When its queue overflows it attaches again after the last event it saw, so a
// burst costs a replay and no events.
func (f *routeFeed) run() {
	defer close(f.done)
	cursor := f.resume
	for {
		s := newSubscriber(f.rt.name, "")
		var first []frame
		attached := false
		f.rt.always(func() {
			var e *APIError
			first, e = f.rt.subscribe(s, cursor)
			attached = e == nil
		})
		if !attached {
			return
		}
		f.setCurrent(s)
		for _, item := range first {
			cursor = f.publish(item, cursor)
		}
		for s.replay != nil {
			rec, err := s.replay.next()
			if err != nil || rec == nil {
				break
			}
			cursor = f.publish(frame{rec: rec, route: f.rt.name}, cursor)
		}
		stopped := f.pump(s, &cursor)
		s.close()
		f.rt.detach(s)
		s.replay.close()
		if stopped {
			return
		}
	}
}

// pump publishes the live queue until it closes or the feed stops, reporting
// whether the feed stopped.
func (f *routeFeed) pump(s *subscriber, cursor *string) bool {
	for {
		select {
		case <-f.stop:
			return true
		case item, ok := <-s.q:
			if !ok {
				return false
			}
			*cursor = f.publish(item, *cursor)
		}
	}
}

func (f *routeFeed) setCurrent(s *subscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sub = s
}

// publish copies one frame to the route's topic and returns the route cursor
// to resume from: this frame's, when it has one.
func (f *routeFeed) publish(item frame, cursor string) string {
	var ev Event
	if item.rec != nil {
		ev = *item.rec.Event
		ev.Route, ev.Seq, ev.Epoch, ev.Time = f.rt.name, item.rec.Seq, item.rec.Epoch, item.rec.Time
	} else {
		ev = *item.ev
	}
	ev.V = Protocol
	publishEncoded(f.hub, events.RouteTopic(f.rt.name), &ev)
	if item.rec != nil || ev.Type == "snapshot" || ev.Type == "reset" {
		return events.FormatCursor(ev.Epoch, ev.Seq)
	}
	return cursor
}
