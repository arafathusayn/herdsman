package message

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// scripted is the shell suite's fake event stream (test/fakebin/nc): a
// list of steps per connection, on the case's fake clock. "@sleep n"
// passes n seconds, "@stage" moves the held envelope to new, "@ready" to
// ready, "@flood n" sends n unrelated events; any other step is a line.
type scripted struct {
	c         *tc
	steps     []string
	opens     int
	floodLeft int
	subs      []string
}

func (s *scripted) Open(_ context.Context, subs []map[string]any) (Stream, error) {
	s.opens++
	b, _ := json.Marshal(subs)
	s.subs = append(s.subs, string(b))
	return &scriptedStream{s: s}, nil
}

type scriptedStream struct{ s *scripted }

func (st *scriptedStream) Close() {}

func (st *scriptedStream) Next(d time.Duration) (string, error) {
	s := st.s
	for len(s.steps) > 0 {
		step := s.steps[0]
		switch {
		case strings.HasPrefix(step, "@sleep "):
			n, _ := strconv.Atoi(strings.TrimPrefix(step, "@sleep "))
			left := time.Duration(n) * time.Second
			if left > d {
				s.c.now = s.c.now.Add(d)
				s.steps[0] = "@sleep " + strconv.Itoa(int((left-d)/time.Second))
				return "", errTimeout
			}
			s.c.now = s.c.now.Add(left)
			d -= left
			s.steps = s.steps[1:]
		case strings.HasPrefix(step, "@blocked "):
			f := strings.Fields(step)
			setBlocked(s.c, f[1], f[2])
			s.steps = s.steps[1:]
		case step == "@stage", step == "@ready":
			dir := "new"
			if step == "@ready" {
				dir = "ready"
			}
			os.Rename(s.c.base("staged", ID+".md"), s.c.ob(dir, ID+".md"))
			s.steps = s.steps[1:]
		case strings.HasPrefix(step, "@flood-for "):
			// Other panes events every 100 ms for n seconds of the fake clock.
			n, _ := strconv.Atoi(strings.TrimPrefix(step, "@flood-for "))
			if n <= 0 {
				s.steps = s.steps[1:]
				continue
			}
			if s.floodLeft == 0 {
				s.floodLeft = n * 10
			}
			s.floodLeft--
			if s.floodLeft == 0 {
				s.steps = s.steps[1:]
			}
			s.c.now = s.c.now.Add(100 * time.Millisecond)
			return `{"event":"pane.updated","data":{"pane":{"pane_id":"other-pane","title":"busy"}}}`, nil
		case strings.HasPrefix(step, "@flood "):
			n, _ := strconv.Atoi(strings.TrimPrefix(step, "@flood "))
			if n <= 1 {
				s.steps = s.steps[1:]
			} else {
				s.steps[0] = "@flood " + strconv.Itoa(n-1)
			}
			return `{"event":"pane.updated","data":{"pane":{"pane_id":"other-pane","title":"busy"}}}`, nil
		default:
			s.steps = s.steps[1:]
			return step, nil
		}
	}
	s.c.now = s.c.now.Add(d)
	return "", errTimeout
}

const ring = `{"event":"pane.updated","data":{"pane":{"pane_id":"sender-pane","tokens":{"message_out":"m000000000001"}}}}`

// evcase is the suite's evcase: a sent envelope held back until a step
// moves it, and the scripted event stream.
func evcase(t *testing.T, steps ...string) (*tc, *scripted) {
	c := newCase(t, true)
	c.send()
	os.MkdirAll(c.base("staged"), 0o700)
	os.Rename(c.ob("new", ID+".md"), c.base("staged", ID+".md"))
	s := &scripted{c: c, steps: steps}
	c.events = s
	return c, s
}

// Parts 11, 12 and 16: EV, the event hints.
func TestPart11EventHints(t *testing.T) {
	c, s := evcase(t, "@sleep 1", "@stage")
	c.courier("RUN_FOR=4", "SWEEP=60")
	check(t, "EV0 without a ring the staged send waits for the next sweep", "^yes no$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "EV0b the courier subscribed to pane updates, closes and exits", "pane.updated.*pane.closed.*pane.exited", strings.Join(s.subs, " "))

	c, _ = evcase(t, "@sleep 1", "@stage", ring)
	c.courier("RUN_FOR=4", "SWEEP=60")
	once := c.count("^agent get receiver-pane")
	c, _ = evcase(t, "@sleep 1", "@stage", ring, ring)
	c.courier("RUN_FOR=4", "SWEEP=60")
	check(t, "EV1 a ring wakes a sweep that delivers the send", "^no yes$", yn(c.ob("new", ID+".md"))+" "+yn(c.ob("ready", ID+".md")))
	check(t, "EV1b a repeated ring for the same message wakes no second sweep", "^"+strconv.Itoa(once)+" 1$", strconv.Itoa(c.count("^agent get receiver-pane"))+" "+strconv.Itoa(nfiles(c.in("state"), "ring-*")))

	c, s = evcase(t, "@sleep 1", "@stage", "this is not a frame")
	c.courier("RUN_FOR=4", "SWEEP=60")
	check(t, "EV3 a malformed frame reconnects and the rescan delivers", "^[2-9] yes 0$", strconv.Itoa(s.opens)+" "+yn(c.ob("ready", ID+".md"))+" "+strconv.Itoa(nfiles(c.in("state"), "ring-*")))

	c, s = evcase(t, "@sleep 1", "@stage", "@flood 300", ring)
	c.courier("RUN_FOR=20", "SWEEP=60")
	check(t, "EV4 a flood of unrelated events is read without reconnecting and the ring still delivers", "^1 yes$", strconv.Itoa(s.opens)+" "+yn(c.ob("ready", ID+".md")))

	c, _ = evcase(t)
	os.Mkdir(c.in("courier.lock"), 0o700)
	out, _ := c.courier()
	check(t, "EV6c and a new courier refuses the kept lock", "courier lock exists", out)

	own := `{"event":"pane.updated","data":{"pane":{"pane_id":"receiver-pane","tokens":{"messages":"1"}}}}`
	other := strings.Replace(own, "receiver-pane", "other-pane", 1)
	c, _ = evcase(t, "@sleep 1", "@ready", own)
	out, _ = c.wait("receiver-pane", O, "DEADLINE=6", "RECHECK=60")
	check(t, "EV7 the waiter wakes on its own pane's event", "^MESSAGE "+O, out)
	c, _ = evcase(t, "@sleep 1", "@ready", other)
	out, _ = c.wait("receiver-pane", O, "DEADLINE=4", "RECHECK=60")
	check(t, "EV7b another pane's event does not wake it before the deadline", "^TICK", out)
	c, _ = evcase(t, "@sleep 1", "@ready", "@flood 300", own)
	out, _ = c.wait("receiver-pane", O, "DEADLINE=20", "RECHECK=60")
	check(t, "EV8 the waiter wakes on its own event after a flood", "^MESSAGE "+O, out)
}

// The herdr transport itself: a subscription through the socket delivers
// an event line, times out when quiet, and ends when closed.
func TestHerdrEvents(t *testing.T) {
	c := newCase(t, false)
	ev := herdrEvents{c: herdr.New(c.srv.Socket)}
	st, err := ev.Open(context.Background(), []map[string]any{{"type": "pane.updated"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; c.srv.Subscribers() == 0 && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	c.srv.Emit("pane.updated", map[string]any{"pane": map[string]any{"pane_id": "p", "tokens": map[string]any{"message_out": ID}}})
	l, err := st.Next(2 * time.Second)
	if err != nil || hint(l) != 0 {
		t.Fatalf("event: %q %v", l, err)
	}
	if p, r := eventPane(l); p != "p" || r != ID {
		t.Errorf("pane %q ring %q", p, r)
	}
	if _, err := st.Next(20 * time.Millisecond); err != errTimeout {
		t.Errorf("quiet stream: %v", err)
	}
	st.Close()
	if _, err := st.Next(time.Second); err == nil || err == errTimeout {
		t.Errorf("closed stream: %v", err)
	}
	if _, err := (herdrEvents{}).Open(context.Background(), nil); err == nil {
		t.Error("no socket opened")
	}
	if hint("not a frame") != 2 || hint(`{"event":"tab.focused"}`) != 3 || hint(`{"messages" broken`) != 2 {
		t.Error("hint classes")
	}
}
