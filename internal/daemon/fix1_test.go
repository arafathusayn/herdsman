package daemon

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	"google.golang.org/protobuf/proto"
)

// Fix round 1 (review daemon-impl-1): the reviewer's probes as tests.

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestHelloWaitHonorsContext (F1): a caller waiting on another caller's
// hello gives up at its own deadline.
func TestHelloWaitHonorsContext(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c := NewClient("unused", testVersion)
	defer c.Close()
	c.hc.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		body, _ := proto.Marshal(&daemonv1.HelloResponse{DaemonVersion: testVersion})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {rpc.ContentTypeProto}}, Request: r,
			Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	first := make(chan error, 1)
	go func() { first <- c.Check(context.Background()) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- c.Check(ctx) }()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("the second check passed before any hello answered")
		}
	case <-time.After(150 * time.Millisecond):
		close(release)
		<-first
		<-second
		t.Fatal("the second check is still blocked 130 ms after its deadline behind another hello")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("after the shared hello: %v", err)
	}
}

// TestRetryOuterBudget (F2): timed-out attempts and the waits between
// them stay inside one 60 s window, and no attempt starts after it. Each
// attempt here takes its whole deadline on the injected clock.
func TestRetryOuterBudget(t *testing.T) {
	clock := newClock()
	start := clock.Now()
	e := &MessageEnv{Now: clock.Now, Sleep: clock.Add}
	tries := 0
	_ = e.retry(context.Background(), func(ctx context.Context) error {
		if clock.Now().Sub(start) >= 60*time.Second {
			t.Fatal("an attempt started after the window")
		}
		tries++
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatal("an attempt without a deadline")
		}
		// The attempt's real-time budget, spent on the injected clock.
		clock.Add(time.Until(dl).Round(time.Millisecond))
		return &DownError{Err: context.DeadlineExceeded}
	})
	if elapsed := clock.Now().Sub(start); elapsed > 60*time.Second || tries < 2 {
		t.Fatalf("%d attempts consumed %s; the window is 60 s", tries, elapsed)
	}
}

// TestDurationManyDigits (F4): a valid duration of any length is clamped.
func TestDurationManyDigits(t *testing.T) {
	for _, v := range []string{strings.Repeat("9", 19) + "d", strings.Repeat("9", 40), "0000000000000000000000001m"} {
		want := 24 * time.Hour
		if strings.HasSuffix(v, "1m") {
			want = time.Minute
		}
		if d, err := parseDuration(v, time.Second, 24*time.Hour); err != nil || d != want {
			t.Fatalf("valid duration %q: got %s, %v; want %s", v, d, err, want)
		}
	}
	for _, v := range []string{"", "d", "1dd", "+1", "1 s", "١"} {
		if _, err := parseDuration(v, time.Second, time.Hour); err == nil {
			t.Fatalf("%q accepted", v)
		}
	}
}

// TestShutdownBackoff (F3): streams that end at once, also after a
// shutdown frame, are reopened 200 ms, 400 ms, 800 ms apart.
func TestShutdownBackoff(t *testing.T) {
	ph := fakeDaemon(t, func(m *http.ServeMux) {
		m.HandleFunc("GET /v1/routes/r/events", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: shutdown\ndata: {\"type\":\"shutdown\",\"reason\":\"upgrade\"}\n\n")
		})
	})
	clock := newClock()
	var sleeps []time.Duration
	e := &MessageEnv{Now: clock.Now, Sleep: func(d time.Duration) {
		sleeps = append(sleeps, d)
		clock.Add(d)
		if len(sleeps) == 4 {
			clock.Add(time.Minute)
		}
	}}
	_, _, _ = runFake(t, e, partyEnv(ph), 5*time.Second, "wait", "--deadline", "30")
	if len(sleeps) < 3 || sleeps[0] != 200*time.Millisecond || sleeps[1] != 400*time.Millisecond || sleeps[2] != 800*time.Millisecond {
		t.Fatalf("shutdown reconnect sleeps %v; want 200ms, 400ms, 800ms", sleeps)
	}
}
