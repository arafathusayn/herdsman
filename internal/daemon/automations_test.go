package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/automations"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/ops"
)

type recordSink struct {
	got []ops.OutboxEvent
	err error
}

func (s *recordSink) Deliver(_ context.Context, ev ops.OutboxEvent) error {
	s.got = append(s.got, ev)
	return s.err
}

func automationRig(t *testing.T, rule automations.Rule, approve bool, next ops.Sink) (*AutomationSink, *herdrtest.Server) {
	t.Helper()
	server := herdrtest.Start(t)
	fixture := herdrtest.Serve(server)
	fixture.SetAgent("pane-a", herdrtest.Agent("pane-a", "sa"))
	server.Handle("pane.send_input", func(map[string]any) (any, string) { return map[string]any{}, "" })
	dir := t.TempDir()
	store := automations.OpenApprovals(filepath.Join(dir, "approvals.toml"))
	if approve {
		if err := store.Approve("proj", rule); err != nil {
			t.Fatal(err)
		}
	}
	engine := &automations.Engine{
		Config: automations.Config{Enabled: true, Rules: []automations.Rule{rule}}, Approvals: store,
		StateDir: dir, Dir: dir, Stderr: &strings.Builder{}, Timeout: 5 * time.Second,
		Builtins: map[string]automations.Builtin{
			automations.BuiltinRoutePause: RoutePause(herdr.New(server.Socket), func(route string) ([]string, error) {
				if route != "r1" {
					return nil, errors.New("unknown route")
				}
				return []string{"pane-a", "gone"}, nil
			}),
		},
	}
	return &AutomationSink{Engine: engine, Next: next, Log: func(string) {}}, server
}

const p0Payload = `{"project":"proj","values":{"route":"r1"}}`

func sentKeys(server *herdrtest.Server) []map[string]any {
	var sent []map[string]any
	for _, request := range server.Requests() {
		if request.Method == "pane.send_input" {
			sent = append(sent, request.Params)
		}
	}
	return sent
}

func TestReviewP0PausesTheRouteInHerdr(t *testing.T) {
	rule := automations.Rule{Event: "review.p0", Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	next := &recordSink{}
	sink, server := automationRig(t, rule, true, next)
	if err := sink.Deliver(context.Background(), ops.OutboxEvent{Key: "k1", Topic: "review.p0", Payload: p0Payload}); err != nil {
		t.Fatal(err)
	}
	sent := sentKeys(server)
	if len(sent) != 1 || sent[0]["pane_id"] != "pane-a" {
		t.Fatalf("%+v", sent)
	}
	keys, _ := sent[0]["keys"].([]any)
	if len(keys) != 1 || keys[0] != "esc" {
		t.Fatalf("%+v", sent[0])
	}
	if len(next.got) != 1 {
		t.Fatalf("the next consumer did not get the event: %+v", next.got)
	}
}

func TestUnapprovedPauseSendsNothing(t *testing.T) {
	rule := automations.Rule{Event: "review.p0", Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	sink, server := automationRig(t, rule, false, nil)
	if err := sink.Deliver(context.Background(), ops.OutboxEvent{Key: "k1", Topic: "review.p0", Payload: p0Payload}); err != nil {
		t.Fatal(err)
	}
	if sent := sentKeys(server); len(sent) != 0 {
		t.Fatalf("%+v", sent)
	}
}

func TestABadPayloadDoesNotStopTheOutbox(t *testing.T) {
	rule := automations.Rule{Event: "review.p0", Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	var logged []string
	next := &recordSink{}
	sink, server := automationRig(t, rule, true, next)
	sink.Log = func(line string) { logged = append(logged, line) }
	if err := sink.Deliver(context.Background(), ops.OutboxEvent{Key: "k1", Topic: "review.p0", Payload: "{not json"}); err != nil {
		t.Fatal(err)
	}
	if len(sentKeys(server)) != 0 || len(logged) != 1 || len(next.got) != 1 {
		t.Fatalf("%v %v", logged, next.got)
	}
}

func TestAnEmptyPayloadStillFiresWithNoProject(t *testing.T) {
	rule := automations.Rule{Event: "review.p0", Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	sink, server := automationRig(t, rule, true, nil)
	if err := sink.Deliver(context.Background(), ops.OutboxEvent{Key: "k1", Topic: "review.p0"}); err != nil {
		t.Fatal(err)
	}
	if len(sentKeys(server)) != 0 {
		t.Fatal("an event with no project was treated as approved")
	}
}

func TestTheNextConsumersFailureIsReturned(t *testing.T) {
	rule := automations.Rule{Event: "review.p0", Kind: automations.KindBuiltin, Body: automations.BuiltinRoutePause}
	want := errors.New("next failed")
	sink, _ := automationRig(t, rule, false, &recordSink{err: want})
	if err := sink.Deliver(context.Background(), ops.OutboxEvent{Key: "k1", Topic: "review.p0", Payload: p0Payload}); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestRoutePauseNeedsARoute(t *testing.T) {
	pause := RoutePause(herdr.New("/nonexistent"), func(string) ([]string, error) { return nil, errors.New("unknown route") })
	if err := pause(context.Background(), automations.Event{}); err == nil {
		t.Fatal("no error without a route")
	}
	if err := pause(context.Background(), automations.Event{Values: map[string]string{"route": "r"}}); err == nil {
		t.Fatal("the resolver's error was dropped")
	}
}

func TestRoutePauseFailsWhenNoAgentWasReached(t *testing.T) {
	pause := RoutePause(herdr.New("/nonexistent"), func(string) ([]string, error) { return []string{"a"}, nil })
	if err := pause(context.Background(), automations.Event{Values: map[string]string{"route": "r"}}); err == nil {
		t.Fatal("reported a pause that reached nobody")
	}
}
