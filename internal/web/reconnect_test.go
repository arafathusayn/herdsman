package web

import (
	"context"
	"io"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// A24: 1000 refused herdr subscriptions, each retried after the backoff,
// leave no goroutines once the hub stops and grow the heap by under 1 MiB;
// the backoff doubles up to its cap.
func TestReconnectLoopKeepsResourcesFlat(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fake := herdrtest.Start(t)
	var tries atomic.Int32
	fake.Handle("events.subscribe", func(map[string]any) (any, string) {
		tries.Add(1)
		return nil, "busy"
	})
	h := NewHub(herdr.New(fake.Socket), herdr.Pong{}, nil)
	h.retryBase, h.retryMax = time.Microsecond, 4*time.Microsecond
	var waits []time.Duration
	h.retryWait = func(d time.Duration) <-chan time.Time {
		if len(waits) < 5 {
			waits = append(waits, d)
		}
		return time.After(d)
	}
	base := settle(1 << 20)
	heap := liveHeap()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.subscribeLoop(ctx, func(context.Context) []map[string]any { return nil }, nil)
		close(done)
	}()
	for tries.Load() < 1000 {
		fake.Forget()
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	fake.Forget()
	if g := settle(base); g > base {
		t.Errorf("goroutines %d -> %d after 1000 reconnects", base, g)
	}
	if grown := int64(liveHeap()) - int64(heap); grown > 1<<20 {
		t.Errorf("live heap grew %d bytes over 1000 reconnects", grown)
	}
	want := []time.Duration{time.Microsecond, 2 * time.Microsecond, 4 * time.Microsecond, 4 * time.Microsecond, 4 * time.Microsecond}
	for i, w := range want {
		if i >= len(waits) || waits[i] != w {
			t.Fatalf("backoff %v, want %v", waits, want)
		}
	}
}
