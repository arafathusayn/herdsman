package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/events"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func daemonTopic(t *testing.T, d *Daemon, topic string) []*daemonv1.Event {
	t.Helper()
	res, err := newEventService(d.hub, nil, nil).Wait(context.Background(), &daemonv1.EventServiceWaitRequest{
		Topics: []*daemonv1.TopicCursor{{Topic: topic}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.GetEvents()
}

func TestEventCallersShareTheirOwnLargerLimit(t *testing.T) {
	d := New(Config{})
	for i := range maxStreamsEvents {
		if _, err := d.admitEvents(eventSlotKey); err != nil {
			t.Fatalf("event slot %d refused: %v", i, err)
		}
	}
	release, err := d.admitEvents(eventSlotKey)
	if release != nil || rpc.FromError(err).Code != rpc.Unavailable {
		t.Fatalf("over the limit: %v", err)
	}
	if !strings.Contains(err.Error(), "too many open streams") {
		t.Fatalf("refusal %v", err)
	}
	// A party's own calls keep the smaller limit beside them.
	for i := range maxStreamsParty {
		if _, e := d.streamSlot("r/party"); e != nil {
			t.Fatalf("party slot %d refused: %v", i, e)
		}
	}
	if _, e := d.streamSlot("r/party"); e == nil {
		t.Fatal("party limit gone")
	}
}

func TestAStoppingDaemonAdmitsNoEventCaller(t *testing.T) {
	d := New(Config{})
	d.upgrading = true
	if release, err := d.admitEvents(eventSlotKey); release != nil || rpc.FromError(err).Code != rpc.Unavailable {
		t.Fatalf("admitted while stopping: %v", err)
	}
}

func TestAReleasedEventSlotIsFreeAgain(t *testing.T) {
	d := New(Config{})
	release, err := d.admitEvents(eventSlotKey)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if d.streams[eventSlotKey] != 0 || d.streams["all"] != 0 {
		t.Fatalf("streams %v", d.streams)
	}
}

func TestReleaseContextIgnoresARequestWithoutASlot(t *testing.T) {
	releaseContext(context.Background())
	calls := 0
	releaseContext(context.WithValue(context.Background(), releaseKey{}, func() { calls++ }))
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}

func TestDaemonPublishesItsRoutesAndTheirRemoval(t *testing.T) {
	h := newHarness(t)
	h.route("r")
	eventually(t, func() bool {
		found := daemonTopic(t, h.d, events.TopicRegistry)
		return len(found) > 0 && strings.Contains(string(found[len(found)-1].GetPayload()), `"route":"r"`)
	})
	eventually(t, func() bool { return len(daemonTopic(t, h.d, events.RouteTopic("r"))) > 0 })

	if e := h.d.removeRoute("r"); e != nil {
		t.Fatal(e)
	}
	found := daemonTopic(t, h.d, events.TopicRegistry)
	if last := found[len(found)-1]; strings.Contains(string(last.GetPayload()), `"route":"r"`) {
		t.Fatalf("still listed: %s", last.GetPayload())
	}
	route := daemonTopic(t, h.d, events.RouteTopic("r"))
	if last := route[len(route)-1]; last.GetType() != "route_removed" {
		t.Fatalf("last event %s", last.GetType())
	}
}

func TestStoppingTheDaemonClosesTheHub(t *testing.T) {
	h := newHarness(t)
	h.d.Stop("test")
	if !h.d.hub.isClosed() {
		t.Fatal("hub still open")
	}
}

func TestInspectDaemonServesTheStream(t *testing.T) {
	d := New(Config{Inspect: true})
	for _, path := range []string{events.Path, rpc.BasePath + daemonv1.EventServiceName + "/Wait"} {
		if e := d.refuseInInspect(httptest.NewRequest(http.MethodPost, path, nil)); e != nil {
			t.Fatalf("%s refused: %v", path, e)
		}
	}
}
