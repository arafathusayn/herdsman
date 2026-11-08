package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/events"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const testWait = 10 * time.Second

// eventServer serves one hub the way the daemon does, on its own socket.
type eventServer struct {
	hub     *eventHub
	service *eventService
	url     string
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	client  *http.Client
}

func newEventServer(t *testing.T, configure func(*eventHub)) *eventServer {
	t.Helper()
	hub := newEventHub()
	if configure != nil {
		configure(hub)
	}
	service := newEventService(hub, nil, nil)
	socket := filepath.Join(shortDir(t), "e.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(rpc.BasePath, rpc.NewMux(service.procedures()...))
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
	t.Cleanup(func() {
		hub.close()
		server.Close()
	})
	return &eventServer{hub: hub, service: service, url: "ws://herdsman" + events.Path, dial: dial,
		client: &http.Client{Transport: &http.Transport{DialContext: dial}}}
}

func (s *eventServer) open(t *testing.T, json bool) *events.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	stream, err := events.Dial(ctx, events.DialOptions{URL: s.url, DialContext: s.dial, JSON: json})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Close() })
	return stream
}

func (s *eventServer) waiter() daemonv1.EventService {
	return daemonv1.NewEventServiceClient(s.client, "http://herdsman")
}

// recv reads frames until one of the wanted kind arrives, failing on timeout.
func recv(t *testing.T, stream *events.Stream) *daemonv1.StreamFrame {
	t.Helper()
	type result struct {
		frame *daemonv1.StreamFrame
		err   error
	}
	got := make(chan result, 1)
	go func() {
		frame, err := stream.Recv()
		got <- result{frame, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("recv: %v", r.err)
		}
		return r.frame
	case <-time.After(testWait):
		t.Fatal("no frame in time")
	}
	return nil
}

func recvEvents(t *testing.T, stream *events.Stream, n int) []string {
	t.Helper()
	var out []string
	for len(out) < n {
		frame := recv(t, stream)
		if frame.GetEvent() == nil {
			t.Fatalf("frame %d is not an event: %v", len(out), frame)
		}
		out = append(out, string(frame.GetEvent().GetPayload()))
	}
	return out
}

func publishN(hub *eventHub, topic string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		payload := fmt.Sprintf("e%d", i)
		hub.publish(topic, "tick", []byte(payload))
		out = append(out, payload)
	}
	return out
}

func TestSubscriberReceivesEventsInOrderWithCursors(t *testing.T) {
	for _, json := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", json), func(t *testing.T) {
			s := newEventServer(t, nil)
			stream := s.open(t, json)
			if err := stream.Subscribe("registry", ""); err != nil {
				t.Fatal(err)
			}
			want := publishN(s.hub, "registry", 0, 5)
			if got := recvEvents(t, stream, 5); !slices.Equal(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			last := stream.Cursor("registry")
			if _, _, ok := events.ParseCursor(last); !ok {
				t.Fatalf("cursor %q does not parse", last)
			}
		})
	}
}

func TestSlowSubscriberIsToldItLaggedAndResumesFromItsCursor(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	s := newEventServer(t, func(h *eventHub) {
		h.lag = 8
		h.beforeWrite = func() { <-gate }
	})
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	stream := s.open(t, false)
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	// Let the subscription register while the writer is held.
	eventually(t, func() bool { return s.service.subscriptions() == 1 })

	want := publishN(s.hub, "t", 0, 30)
	open()

	frame := recv(t, stream)
	lagged := frame.GetLagged()
	if lagged == nil {
		t.Fatalf("first frame is %v, want lagged", frame)
	}
	if lagged.GetReason() != "queue_full" {
		t.Fatalf("reason %q", lagged.GetReason())
	}
	if err := stream.Subscribe("t", lagged.GetCursor()); err != nil {
		t.Fatal(err)
	}
	if got := recvEvents(t, stream, len(want)); !slices.Equal(got, want) {
		t.Fatalf("after resume got %v", got)
	}
}

func TestPublisherNeverWaitsForAStalledSubscriber(t *testing.T) {
	gate := make(chan struct{})
	s := newEventServer(t, func(h *eventHub) { h.beforeWrite = func() { <-gate } })
	defer close(gate)
	stream := s.open(t, false)
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.service.subscriptions() == 1 })
	done := make(chan struct{})
	go func() {
		publishN(s.hub, "t", 0, 10_000)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(testWait):
		t.Fatal("publish blocked behind a stalled subscriber")
	}
	if n := s.hub.retained("t"); n > s.hub.retain {
		t.Fatalf("topic retains %d events, limit %d", n, s.hub.retain)
	}
}

func TestWaitReturnsTheSameEventsAsTheStream(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.service.subscriptions() == 1 })
	publishN(s.hub, "t", 0, 3)
	s.hub.publish("t", "other", []byte("x"))

	type seen struct{ cursor, kind, payload string }
	var streamed []seen
	for len(streamed) < 4 {
		e := recv(t, stream).GetEvent()
		streamed = append(streamed, seen{e.GetCursor(), e.GetType(), string(e.GetPayload())})
	}

	waiter := events.NewWaiter(s.waiter(), "t")
	var waited []seen
	for len(waited) < 4 {
		ctx, cancel := context.WithTimeout(context.Background(), testWait)
		batch, err := waiter.Next(ctx, time.Second)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range batch.Events {
			waited = append(waited, seen{e.GetCursor(), e.GetType(), string(e.GetPayload())})
		}
	}
	if !slices.Equal(streamed, waited) {
		t.Fatalf("stream %v\nwait   %v", streamed, waited)
	}
}

func TestWaitBlocksUntilAnEventAndReportsEveryCursor(t *testing.T) {
	s := newEventServer(t, nil)
	client := s.waiter()
	first, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{
		Topics: []*daemonv1.TopicCursor{{Topic: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GetEvents()) != 0 || len(first.GetCursors()) != 1 {
		t.Fatalf("empty topic answered %v", first)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish("a", "tick", []byte("late"))
	}()
	res, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{
		Topics: first.GetCursors(), WaitMs: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetEvents()) != 1 || string(res.GetEvents()[0].GetPayload()) != "late" {
		t.Fatalf("got %v", res)
	}
	if res.GetCursors()[0].GetCursor() != res.GetEvents()[0].GetCursor() {
		t.Fatalf("cursor %v after event %v", res.GetCursors(), res.GetEvents())
	}
}

func TestWaitTimesOutWithTheSamePosition(t *testing.T) {
	s := newEventServer(t, nil)
	s.hub.publish("a", "tick", []byte("1"))
	client := s.waiter()
	res, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: res.GetCursors(), WaitMs: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.GetEvents()) != 0 || again.GetCursors()[0].GetCursor() != res.GetCursors()[0].GetCursor() {
		t.Fatalf("got %v after %v", again, res)
	}
}

func TestExpiredAndForeignCursorsReportTheLoss(t *testing.T) {
	s := newEventServer(t, func(h *eventHub) { h.retain = 4 })
	publishN(s.hub, "t", 0, 3)
	client := s.waiter()
	res, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}})
	if err != nil {
		t.Fatal(err)
	}
	old := res.GetEvents()[0].GetCursor()
	epoch, seq, _ := events.ParseCursor(old)
	publishN(s.hub, "t", 3, 20)

	cases := []struct{ name, cursor, reason string }{
		{"behind the ring", old, "cursor_expired"},
		{"past the head", events.FormatCursor(epoch, seq+1000), "cursor_expired"},
		{"another epoch", events.FormatCursor(strings.Repeat("0", 32), 1), "epoch_changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t", Cursor: c.cursor}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.GetLagged()) != 1 || res.GetLagged()[0].GetReason() != c.reason {
				t.Fatalf("got %v, want reason %s", res, c.reason)
			}
			resumed, err := client.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: res.GetCursors()})
			if err != nil {
				t.Fatal(err)
			}
			if len(resumed.GetEvents()) != s.hub.retain || len(resumed.GetLagged()) != 0 {
				t.Fatalf("resume got %v", resumed)
			}
		})
	}
	stream := s.open(t, false)
	if err := stream.Subscribe("t", old); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, stream).GetLagged().GetReason(); got != "cursor_expired" {
		t.Fatalf("stream reason %q", got)
	}
}

func TestACursorOnATopicNobodyPublishedIsRefusedAsChanged(t *testing.T) {
	s := newEventServer(t, nil)
	res, err := s.waiter().Wait(context.Background(), &daemonv1.EventServiceWaitRequest{
		Topics: []*daemonv1.TopicCursor{{Topic: "gone", Cursor: events.FormatCursor(strings.Repeat("a", 32), 7)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetLagged()) != 1 || res.GetLagged()[0].GetReason() != "epoch_changed" || res.GetCursors()[0].GetCursor() != "" {
		t.Fatalf("got %v", res)
	}
}

func TestWaitRefusesBadRequests(t *testing.T) {
	s := newEventServer(t, nil)
	many := make([]*daemonv1.TopicCursor, maxEventTopics+1)
	for i := range many {
		many[i] = &daemonv1.TopicCursor{Topic: fmt.Sprintf("t%d", i)}
	}
	cases := map[string][]*daemonv1.TopicCursor{
		"no topic":   nil,
		"bad topic":  {{Topic: "../x"}},
		"bad cursor": {{Topic: "t", Cursor: "nope"}},
		"too many":   many,
		"twice":      {{Topic: "t"}, {Topic: "t"}},
	}
	for name, topics := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.waiter().Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: topics})
			if err == nil || !strings.Contains(err.Error(), "invalid_argument") {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestWaitBoundsOneReply(t *testing.T) {
	s := newEventServer(t, func(h *eventHub) { h.retain = 500 })
	publishN(s.hub, "t", 0, 200)
	res, err := s.waiter().Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetEvents()) != maxWaitEvents {
		t.Fatalf("reply carried %d", len(res.GetEvents()))
	}
	if res.GetCursors()[0].GetCursor() != res.GetEvents()[maxWaitEvents-1].GetCursor() {
		t.Fatal("next cursor is not after the last event returned")
	}
}

func TestWaitEndsWhenTheCallerGoesAway(t *testing.T) {
	s := newEventServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.service.Wait(ctx, &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}, WaitMs: 20_000})
	if err == nil {
		t.Fatal("wait outlived its caller")
	}
}

func TestWaitEndsWhenTheDaemonStops(t *testing.T) {
	s := newEventServer(t, nil)
	done := make(chan error, 1)
	go func() {
		_, err := s.service.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}, WaitMs: 20_000})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	s.hub.close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a stopping daemon answers with what it has: %v", err)
		}
	case <-time.After(testWait):
		t.Fatal("wait kept the daemon from stopping")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	for _, topic := range []string{"a", "b"} {
		if err := stream.Subscribe(topic, ""); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return s.service.subscriptions() == 2 })
	if err := stream.Unsubscribe("a"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.service.subscriptions() == 1 })
	s.hub.publish("a", "tick", []byte("no"))
	s.hub.publish("b", "tick", []byte("yes"))
	if got := recvEvents(t, stream, 1); got[0] != "yes" {
		t.Fatalf("got %v", got)
	}
}

func TestStreamAnswersAClientPingWithItsNonce(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	if err := stream.Ping("n-1"); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, stream).GetPing().GetNonce(); got != "n-1" {
		t.Fatalf("nonce %q", got)
	}
}

func TestIdleStreamIsPinged(t *testing.T) {
	s := newEventServer(t, func(h *eventHub) { h.ping = 10 * time.Millisecond })
	stream := s.open(t, false)
	if recv(t, stream).GetPing() == nil {
		t.Fatal("first idle frame is not a ping")
	}
}

func TestStreamClosesOnAFrameItCannotServe(t *testing.T) {
	cases := map[string]func(*events.Stream) error{
		"bad topic":  func(c *events.Stream) error { return c.Subscribe("../x", "") },
		"bad cursor": func(c *events.Stream) error { return c.Subscribe("t", "nope") },
		"server frame": func(c *events.Stream) error {
			return c.Send(&daemonv1.StreamFrame{Frame: &daemonv1.StreamFrame_Event{Event: &daemonv1.Event{}}})
		},
	}
	for name, send := range cases {
		t.Run(name, func(t *testing.T) {
			s := newEventServer(t, nil)
			stream := s.open(t, false)
			if err := send(stream); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err == nil {
				t.Fatal("connection stayed open")
			}
			eventually(t, func() bool { return s.service.subscriptions() == 0 && s.service.connections() == 0 })
		})
	}
}

func TestStreamLimitsSubscriptionsPerConnection(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	for i := range maxEventTopics + 1 {
		if err := stream.Subscribe(fmt.Sprintf("t%d", i), ""); err != nil {
			break
		}
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("connection stayed open over the subscription limit")
	}
}

func TestDaemonStopClosesStreams(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.service.subscriptions() == 1 })
	s.hub.close()
	if _, err := stream.Recv(); err == nil {
		t.Fatal("stream survived the daemon")
	}
	eventually(t, func() bool { return s.service.connections() == 0 })
}

func TestAStreamIsAdmittedAndReleased(t *testing.T) {
	hub := newEventHub()
	defer hub.close()
	admitted, released, attached := 0, 0, 0
	var mu sync.Mutex
	service := newEventService(hub, func(key string) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		if key != eventSlotKey {
			t.Errorf("slot key %q", key)
		}
		admitted++
		return func() { mu.Lock(); released++; mu.Unlock() }, nil
	}, func(context.Context) { mu.Lock(); attached++; mu.Unlock() })
	server := httptest.NewServer(rpc.NewMux(service.procedures()...))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	stream, err := events.Dial(ctx, events.DialOptions{URL: "ws" + strings.TrimPrefix(server.URL, "http") + events.Path})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return attached == 1 })
	stream.Close()
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return admitted == 1 && released == 1 })

	res, err := daemonv1.NewEventServiceClient(server.Client(), server.URL).Wait(ctx, &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}})
	if err != nil || res == nil {
		t.Fatalf("wait: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if admitted != 2 || released != 2 || attached != 2 {
		t.Fatalf("admitted %d released %d attached %d", admitted, released, attached)
	}
}

func TestAStreamOverTheLimitIsRefusedBeforeUpgrade(t *testing.T) {
	hub := newEventHub()
	defer hub.close()
	service := newEventService(hub, func(string) (func(), error) { return nil, rpc.Errorf(rpc.ResourceExhausted, "too many open streams") }, nil)
	server := httptest.NewServer(rpc.NewMux(service.procedures()...))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	if _, err := events.Dial(ctx, events.DialOptions{URL: "ws" + strings.TrimPrefix(server.URL, "http") + events.Path}); err == nil {
		t.Fatal("stream admitted over the limit")
	}
	if _, err := daemonv1.NewEventServiceClient(server.Client(), server.URL).Wait(ctx, &daemonv1.EventServiceWaitRequest{Topics: []*daemonv1.TopicCursor{{Topic: "t"}}}); err == nil {
		t.Fatal("wait admitted over the limit")
	}
}

func TestAPlainRequestToTheStreamPathIsRefused(t *testing.T) {
	s := newEventServer(t, nil)
	res, err := s.client.Post("http://herdsman"+events.Path, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestByteLimitTrimsATopic(t *testing.T) {
	hub := newEventHub()
	defer hub.close()
	hub.retainBytes = 10
	hub.publish("t", "x", []byte("0123456"))
	hub.publish("t", "x", []byte("0123456"))
	if n := hub.retained("t"); n != 1 {
		t.Fatalf("retained %d", n)
	}
}

func TestOldestIdleTopicIsForgottenAtTheLimit(t *testing.T) {
	hub := newEventHub()
	defer hub.close()
	hub.maxTopics = 2
	hub.publish("a", "x", nil)
	hub.publish("b", "x", nil)
	hub.publish("a", "x", nil)
	hub.publish("c", "x", nil)
	if hub.retained("b") != 0 || hub.retained("a") != 2 || hub.retained("c") != 1 {
		t.Fatalf("a=%d b=%d c=%d", hub.retained("a"), hub.retained("b"), hub.retained("c"))
	}
}

func TestPublishAfterCloseIsDropped(t *testing.T) {
	hub := newEventHub()
	hub.close()
	hub.close()
	if cursor := hub.publish("t", "x", nil); cursor != "" || hub.retained("t") != 0 {
		t.Fatalf("published %q after close", cursor)
	}
}

func (s *eventService) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *eventService) subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		c.mu.Lock()
		n += len(c.subs)
		c.mu.Unlock()
	}
	return n
}

func (h *eventHub) retained(topic string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.topics[topic]; t != nil {
		return len(t.ring)
	}
	return 0
}

// dropQueue closes a feed's queue, as an overflow does.
func (v *registryView) dropQueue(name string) {
	v.mu.Lock()
	feed := v.feeds[name]
	v.mu.Unlock()
	feed.mu.Lock()
	defer feed.mu.Unlock()
	feed.sub.close()
}

// feedsRunning counts the feeds whose goroutine has not ended.
func (v *registryView) feedsRunning() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, feed := range v.feeds {
		select {
		case <-feed.done:
		default:
			n++
		}
	}
	return n
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The view: every route's events and the membership, as topics.

func viewEvents(t *testing.T, s *eventServer, topic string, want int) []*daemonv1.Event {
	t.Helper()
	var out []*daemonv1.Event
	deadline := time.Now().Add(testWait)
	cursor := ""
	for len(out) < want {
		if time.Now().After(deadline) {
			t.Fatalf("topic %s: %d of %d events: %v", topic, len(out), want, out)
		}
		res, err := s.service.Wait(context.Background(), &daemonv1.EventServiceWaitRequest{
			Topics: []*daemonv1.TopicCursor{{Topic: topic, Cursor: cursor}}, WaitMs: 200})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, res.GetEvents()...)
		cursor = res.GetCursors()[0].GetCursor()
	}
	return out
}

func TestRegistryViewListsRoutesAndCarriesTheirEvents(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := newEventServer(t, nil)
	view := newRegistryView(s.hub)
	defer view.close()
	view.attach(h.d.routes["r"])

	first := viewEvents(t, s, events.TopicRegistry, 1)[0]
	if first.GetType() != "registry" || !strings.Contains(string(first.GetPayload()), `"route":"r"`) {
		t.Fatalf("registry event %s %s", first.GetType(), first.GetPayload())
	}
	topic := events.RouteTopic("r")
	if got := viewEvents(t, s, topic, 1)[0]; got.GetType() != "snapshot" {
		t.Fatalf("first route event is %s", got.GetType())
	}

	adj.send(t, "r", orch.name, "the body stays private")
	var message *daemonv1.Event
	eventually(t, func() bool {
		for _, e := range viewEvents(t, s, topic, 1) {
			if e.GetType() == "message" {
				message = e
				return true
			}
		}
		return false
	})
	if strings.Contains(string(message.GetPayload()), "the body stays private") {
		t.Fatal("the view carried a message body")
	}
	if !strings.Contains(string(message.GetPayload()), `"route":"r"`) {
		t.Fatalf("payload %s", message.GetPayload())
	}

	view.remove("r")
	all := viewEvents(t, s, events.TopicRegistry, 2)
	if got := string(all[1].GetPayload()); strings.Contains(got, `"route":"r"`) {
		t.Fatalf("removed route still listed: %s", got)
	}
	eventually(t, func() bool {
		for _, e := range viewEvents(t, s, topic, 1) {
			if e.GetType() == "route_removed" {
				return true
			}
		}
		return false
	})
}

func TestRegistryViewResumesAfterItsQueueOverflows(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	s := newEventServer(t, nil)
	view := newRegistryView(s.hub)
	defer view.close()
	rt := h.d.routes["r"]
	view.attach(rt)
	topic := events.RouteTopic("r")
	viewEvents(t, s, topic, 1)

	// Overflow the feed's own queue, as a burst the feed cannot drain would.
	view.dropQueue("r")
	adj.send(t, "r", orch.name, "after the overflow")
	eventually(t, func() bool {
		for _, e := range viewEvents(t, s, topic, 1) {
			if e.GetType() == "message" {
				return true
			}
		}
		return false
	})
}

func TestRegistryViewForgetsAStoppedRoute(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	s := newEventServer(t, nil)
	view := newRegistryView(s.hub)
	defer view.close()
	rt := h.d.routes["r"]
	view.attach(rt)
	viewEvents(t, s, events.RouteTopic("r"), 1)
	h.stop()
	eventually(t, func() bool { return view.feedsRunning() == 0 })
	view.remove("r")
	view.remove("never attached")
}

func TestRegistryViewListsEveryRouteInOrder(t *testing.T) {
	h := newHarness(t)
	h.route("b")
	h.route("a")
	s := newEventServer(t, nil)
	view := newRegistryView(s.hub)
	defer view.close()
	view.attach(h.d.routes["b"])
	view.attach(h.d.routes["a"])
	last := string(viewEvents(t, s, events.TopicRegistry, 2)[1].GetPayload())
	if a, b := strings.Index(last, `"route":"a"`), strings.Index(last, `"route":"b"`); a < 0 || b < a {
		t.Fatalf("membership %s", last)
	}
}

func TestRouteFeedReplaysFromTheJournalWhenTheRingIsPast(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "older than the ring")
	rt := h.d.routes["r"]
	var epoch string
	rt.always(func() { epoch, rt.ring = rt.epoch, nil })

	s := newEventServer(t, nil)
	feed := &routeFeed{hub: s.hub, rt: rt, stop: make(chan struct{}), done: make(chan struct{}), resume: events.FormatCursor(epoch, 0)}
	go feed.run()
	defer feed.halt()
	eventually(t, func() bool {
		for _, e := range viewEvents(t, s, events.RouteTopic("r"), 1) {
			if e.GetType() == "message" {
				return true
			}
		}
		return false
	})
}

// A write that fails ends the connection, whichever frame it was.
func TestAFailedWriteEndsTheConnection(t *testing.T) {
	cases := map[string]func(*events.Stream, *eventHub){
		"event": func(_ *events.Stream, hub *eventHub) { hub.publish("t", "x", []byte("e")) },
		"lagged": func(_ *events.Stream, hub *eventHub) {
			for range 10 {
				hub.publish("t", "x", []byte("e"))
			}
		},
		"pong": func(stream *events.Stream, _ *eventHub) { stream.Ping("n") },
	}
	for name, provoke := range cases {
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			var once sync.Once
			s := newEventServer(t, func(h *eventHub) {
				h.lag = 2
				h.beforeWrite = func() { <-gate }
			})
			t.Cleanup(func() { once.Do(func() { close(gate) }) })
			stream := s.open(t, false)
			if err := stream.Subscribe("t", ""); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { return s.service.subscriptions() == 1 })
			provoke(stream, s.hub)
			if name == "pong" {
				eventually(t, func() bool { return s.service.pendingPongs() == 1 })
			}
			s.service.closeAll()
			once.Do(func() { close(gate) })
			eventually(t, func() bool { return s.service.connections() == 0 })
		})
	}
}

func TestPingsPastTheLimitAreDropped(t *testing.T) {
	s := newEventServer(t, nil)
	c := &eventConn{service: s.service, subs: map[string]*eventSubscription{}}
	for range maxPendingPongs + 3 {
		c.queuePing("n")
	}
	if got := len(c.takePongs()); got != maxPendingPongs {
		t.Fatalf("queued %d", got)
	}
}

func TestAFrameOfAnUnknownKindIsIgnored(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	if err := stream.Send(&daemonv1.StreamFrame{}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Ping("still"); err != nil {
		t.Fatal(err)
	}
	if got := recv(t, stream).GetPing().GetNonce(); got != "still" {
		t.Fatalf("nonce %q", got)
	}
}

func TestResubscribingReplacesThePositionAndSurvivesTheLag(t *testing.T) {
	s := newEventServer(t, nil)
	stream := s.open(t, false)
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	want := publishN(s.hub, "t", 0, 3)
	if got := recvEvents(t, stream, 3); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if err := stream.Subscribe("t", ""); err != nil {
		t.Fatal(err)
	}
	if got := recvEvents(t, stream, 3); !slices.Equal(got, want) {
		t.Fatalf("replay got %v", got)
	}
}

func (s *eventService) pendingPongs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		c.mu.Lock()
		n += len(c.pongs)
		c.mu.Unlock()
	}
	return n
}

// closeAll closes the server side of every connection, so the next write fails.
func (s *eventService) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.conn.Close(events.CloseNormal, "")
	}
}
