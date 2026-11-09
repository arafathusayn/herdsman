package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/events"
)

const eventWait = 15 * time.Second

// eventDaemon is one real daemon with the clients that reach it.
type eventDaemon struct {
	socket string
	hc     *http.Client
}

func (d *eventDaemon) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", d.socket)
}

func (d *eventDaemon) open(t *testing.T, json bool) *events.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventWait)
	defer cancel()
	stream, err := events.Dial(ctx, events.DialOptions{URL: "ws://herdsman" + events.Path, DialContext: d.dial, JSON: json})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func (d *eventDaemon) createRoute(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), eventWait)
	defer cancel()
	code, err := unary[daemonv1.CreateRouteRequest, daemonv1.CreateRouteResponse]("CreateRoute",
		&daemonv1.CreateRouteRequest{Repo: "repo", Route: route(name)}, nil)(ctx, d.hc, nil)
	if err != nil || code != okay {
		t.Fatalf("create route %s: code %v, error %v", name, code, err)
	}
}

// wait is one long poll with a stock Connect client.
func (d *eventDaemon) wait(ctx context.Context, options []connect.ClientOption, req *daemonv1.EventServiceWaitRequest) (*daemonv1.EventServiceWaitResponse, error) {
	url := "http://herdsman/rpc/" + daemonv1.EventServiceName + "/Wait"
	res, err := connect.NewClient[daemonv1.EventServiceWaitRequest, daemonv1.EventServiceWaitResponse](d.hc, url, options...).CallUnary(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func positions(topics ...string) []*daemonv1.TopicCursor {
	out := make([]*daemonv1.TopicCursor, 0, len(topics))
	for _, topic := range topics {
		out = append(out, &daemonv1.TopicCursor{Topic: topic})
	}
	return out
}

// seenEvent is everything about an event a reader can compare.
type seenEvent struct {
	topic, cursor, kind, payload string
}

func seen(event *daemonv1.Event) seenEvent {
	return seenEvent{event.GetTopic(), event.GetCursor(), event.GetType(), string(event.GetPayload())}
}

// drain long-polls a topic until it has nothing more, and returns what it read
// and where it stopped.
func (d *eventDaemon) drain(t *testing.T, options []connect.ClientOption, topic string) ([]seenEvent, []*daemonv1.TopicCursor) {
	t.Helper()
	var out []seenEvent
	at := positions(topic)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), eventWait)
		res, err := d.wait(ctx, options, &daemonv1.EventServiceWaitRequest{Topics: at})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(res.GetLagged()) != 0 {
			t.Fatalf("lagged while draining: %v", res.GetLagged())
		}
		at = res.GetCursors()
		if len(res.GetEvents()) == 0 {
			return out, at
		}
		for _, event := range res.GetEvents() {
			out = append(out, seen(event))
		}
	}
}

// recvFrame is the next frame, within the time limit.
func recvFrame(t *testing.T, stream *events.Stream) *daemonv1.StreamFrame {
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
	case <-time.After(eventWait):
		t.Fatal("no frame in time")
		return nil
	}
}

// recvEvents reads n events, passing over the daemon's idle pings.
func recvEvents(t *testing.T, stream *events.Stream, n int) []seenEvent {
	t.Helper()
	var out []seenEvent
	for len(out) < n {
		frame := recvFrame(t, stream)
		if frame.GetPing() != nil {
			continue
		}
		if frame.GetEvent() == nil {
			t.Fatalf("after %d events: %v", len(out), frame)
		}
		out = append(out, seen(frame.GetEvent()))
	}
	return out
}

// lists reports whether a registry event's payload names a route.
func lists(event seenEvent, name string) bool {
	return event.kind == "registry" && strings.Contains(event.payload, fmt.Sprintf(`"route":%q`, name))
}

// TestEventScenariosCoverEveryMethod fails when the service grows a unary
// method these scenarios do not call.
func TestEventScenariosCoverEveryMethod(t *testing.T) {
	methods := (&daemonv1.EventServiceWaitRequest{}).ProtoReflect().Descriptor().ParentFile().Services().ByName("EventService").Methods()
	var names []string
	for i := range methods.Len() {
		names = append(names, string(methods.Get(i).Name()))
	}
	if !slices.Equal(names, []string{"Wait"}) {
		t.Fatalf("EventService has %v; add scenarios for what is new", names)
	}
}

func TestEventServiceAgainstTheDaemon(t *testing.T) {
	socket := startDaemon(t)
	d := &eventDaemon{socket: socket, hc: httpClient(t, socket)}
	encodings := map[string][]connect.ClientOption{"proto": nil, "json": {connect.WithProtoJSON()}}

	for encoding, options := range encodings {
		t.Run("wait/"+encoding, func(t *testing.T) {
			name := "wait-" + encoding
			d.createRoute(t, name)
			registry, _ := d.drain(t, options, events.TopicRegistry)
			if !slices.ContainsFunc(registry, func(e seenEvent) bool { return lists(e, name) }) {
				t.Fatalf("no registry event lists %s: %v", name, registry)
			}
			var routeEvents []seenEvent
			deadline := time.Now().Add(eventWait)
			at := positions(events.RouteTopic(name))
			for len(routeEvents) == 0 && time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), eventWait)
				res, err := d.wait(ctx, options, &daemonv1.EventServiceWaitRequest{Topics: at, WaitMs: 500})
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				at = res.GetCursors()
				for _, event := range res.GetEvents() {
					routeEvents = append(routeEvents, seen(event))
				}
			}
			if len(routeEvents) == 0 || routeEvents[0].kind != "snapshot" {
				t.Fatalf("route topic began with %v", routeEvents)
			}
		})

		t.Run("wait-holds-then-answers/"+encoding, func(t *testing.T) {
			_, at := d.drain(t, options, events.TopicRegistry)
			name := "held-" + encoding
			type result struct {
				res *daemonv1.EventServiceWaitResponse
				err error
			}
			done := make(chan result, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), eventWait)
				defer cancel()
				res, err := d.wait(ctx, options, &daemonv1.EventServiceWaitRequest{Topics: at, WaitMs: 10_000})
				done <- result{res, err}
			}()
			time.Sleep(100 * time.Millisecond)
			d.createRoute(t, name)
			select {
			case r := <-done:
				if r.err != nil {
					t.Fatal(r.err)
				}
				if len(r.res.GetEvents()) == 0 || !lists(seen(r.res.GetEvents()[0]), name) {
					t.Fatalf("held wait answered %v", r.res)
				}
				if r.res.GetCursors()[0].GetCursor() == at[0].GetCursor() {
					t.Fatal("the position did not move")
				}
			case <-time.After(eventWait):
				t.Fatal("the held wait did not answer")
			}
		})

		t.Run("wait-times-out-in-place/"+encoding, func(t *testing.T) {
			_, at := d.drain(t, options, events.TopicRegistry)
			ctx, cancel := context.WithTimeout(context.Background(), eventWait)
			defer cancel()
			res, err := d.wait(ctx, options, &daemonv1.EventServiceWaitRequest{Topics: at, WaitMs: 20})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.GetEvents()) != 0 || res.GetCursors()[0].GetCursor() != at[0].GetCursor() {
				t.Fatalf("got %v from %v", res, at)
			}
		})

		t.Run("wait-refusals/"+encoding, func(t *testing.T) {
			for label, topics := range map[string][]*daemonv1.TopicCursor{
				"no topic":   nil,
				"bad topic":  {{Topic: "../x"}},
				"bad cursor": {{Topic: "registry", Cursor: "nope"}},
				"twice":      {{Topic: "registry"}, {Topic: "registry"}},
			} {
				ctx, cancel := context.WithTimeout(context.Background(), eventWait)
				_, err := d.wait(ctx, options, &daemonv1.EventServiceWaitRequest{Topics: topics})
				cancel()
				var failure *connect.Error
				if !errors.As(err, &failure) || failure.Code() != connect.CodeInvalidArgument {
					t.Errorf("%s: %v", label, err)
				}
			}
		})
	}

	for _, json := range []bool{false, true} {
		suffix := fmt.Sprintf("json=%v", json)

		t.Run("stream-and-wait-agree/"+suffix, func(t *testing.T) {
			d.createRoute(t, "agree-before-"+suffix[5:])
			waited, at := d.drain(t, nil, events.TopicRegistry)
			stream := d.open(t, json)
			if err := stream.Subscribe(events.TopicRegistry, ""); err != nil {
				t.Fatal(err)
			}
			streamed := recvEvents(t, stream, len(waited))
			if !slices.Equal(streamed, waited) {
				t.Fatalf("history differs\nstream %v\nwait   %v", streamed, waited)
			}

			d.createRoute(t, "agree-after-"+suffix[5:])
			live := recvEvents(t, stream, 1)
			ctx, cancel := context.WithTimeout(context.Background(), eventWait)
			defer cancel()
			res, err := d.wait(ctx, nil, &daemonv1.EventServiceWaitRequest{Topics: at, WaitMs: 5000})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.GetEvents()) != 1 || seen(res.GetEvents()[0]) != live[0] {
				t.Fatalf("live differs\nstream %v\nwait   %v", live, res.GetEvents())
			}
		})

		t.Run("stream-resumes-after-reconnect/"+suffix, func(t *testing.T) {
			history, _ := d.drain(t, nil, events.TopicRegistry)
			first := d.open(t, json)
			if err := first.Subscribe(events.TopicRegistry, ""); err != nil {
				t.Fatal(err)
			}
			recvEvents(t, first, len(history))
			cursor := first.Cursor(events.TopicRegistry)
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}

			name := "resume-" + suffix[5:]
			d.createRoute(t, name)
			second := d.open(t, json)
			if err := second.Subscribe(events.TopicRegistry, cursor); err != nil {
				t.Fatal(err)
			}
			if got := recvEvents(t, second, 1)[0]; !lists(got, name) {
				t.Fatalf("resumed at %v", got)
			}
		})

		t.Run("stream-reports-a-foreign-cursor/"+suffix, func(t *testing.T) {
			stream := d.open(t, json)
			if err := stream.Subscribe(events.TopicRegistry, events.FormatCursor(strings.Repeat("0", 32), 1)); err != nil {
				t.Fatal(err)
			}
			frame := recvFrame(t, stream)
			if frame.GetPing() != nil {
				frame = recvFrame(t, stream)
			}
			if frame.GetLagged().GetReason() != "epoch_changed" {
				t.Fatalf("got %v", frame)
			}
		})

		t.Run("stream-answers-a-ping/"+suffix, func(t *testing.T) {
			stream := d.open(t, json)
			if err := stream.Ping("conformance"); err != nil {
				t.Fatal(err)
			}
			if got := recvFrame(t, stream).GetPing().GetNonce(); got != "conformance" {
				t.Fatalf("nonce %q", got)
			}
		})

		t.Run("stream-unsubscribe-stops-a-topic/"+suffix, func(t *testing.T) {
			history, _ := d.drain(t, nil, events.TopicRegistry)
			stream := d.open(t, json)
			if err := stream.Subscribe(events.TopicRegistry, ""); err != nil {
				t.Fatal(err)
			}
			recvEvents(t, stream, len(history))
			if err := stream.Unsubscribe(events.TopicRegistry); err != nil {
				t.Fatal(err)
			}
			// The answer to a ping sent after it proves the unsubscribe was read.
			if err := stream.Ping("after"); err != nil {
				t.Fatal(err)
			}
			if got := recvFrame(t, stream).GetPing().GetNonce(); got != "after" {
				t.Fatalf("nonce %q", got)
			}
			d.createRoute(t, "quiet-"+suffix[5:])
			if err := stream.Ping("later"); err != nil {
				t.Fatal(err)
			}
			if frame := recvFrame(t, stream); frame.GetPing().GetNonce() != "later" {
				t.Fatalf("delivered after unsubscribe: %v", frame)
			}
		})

		t.Run("stream-closes-on-a-bad-topic/"+suffix, func(t *testing.T) {
			stream := d.open(t, json)
			if err := stream.Subscribe("../x", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err == nil {
				t.Fatal("the connection stayed open")
			}
		})
	}

	t.Run("a-plain-request-to-the-stream-is-refused", func(t *testing.T) {
		res, err := d.hc.Post("http://herdsman"+events.Path, "application/json", bytes.NewReader([]byte("{}")))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", res.StatusCode)
		}
	})
}
