package events

import (
	"context"
	"time"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// Batch is one long poll's result.
type Batch struct {
	Events []*daemonv1.Event
	Lagged []*daemonv1.Lagged
}

// Waiter reads topics by long poll and keeps their positions between calls.
type Waiter struct {
	client daemonv1.EventService
	topics []*daemonv1.TopicCursor
}

// NewWaiter polls the named topics from the oldest event each retains.
func NewWaiter(client daemonv1.EventService, topics ...string) *Waiter {
	w := &Waiter{client: client}
	for _, topic := range topics {
		w.topics = append(w.topics, &daemonv1.TopicCursor{Topic: topic})
	}
	return w
}

// Next returns the events after the current positions and moves past them. It
// waits up to wait for one. A Lagged entry says what was lost; the position has
// already moved to where reading continues.
func (w *Waiter) Next(ctx context.Context, wait time.Duration) (*Batch, error) {
	res, err := w.client.Wait(ctx, &daemonv1.EventServiceWaitRequest{Topics: w.topics, WaitMs: int32(wait / time.Millisecond)})
	if err != nil {
		return nil, err
	}
	w.topics = res.GetCursors()
	return &Batch{Events: res.GetEvents(), Lagged: res.GetLagged()}, nil
}

// Cursor is a polled topic's position.
func (w *Waiter) Cursor(topic string) string {
	for _, position := range w.topics {
		if position.GetTopic() == topic {
			return position.GetCursor()
		}
	}
	return ""
}
