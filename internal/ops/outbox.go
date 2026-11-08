package ops

import (
	"context"
	"fmt"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// OutboxEvent is one event waiting for its consumer. Delivery is at least
// once; Key is stable, and a consumer drops a repeat by it.
type OutboxEvent struct {
	Seq     int64
	Key     string
	OpID    string
	Topic   string
	Payload string
}

// Sink receives outbox events in order.
type Sink interface {
	Deliver(ctx context.Context, ev OutboxEvent) error
}

func enqueue(c store.Conn, id string, st Step, now int64) error {
	for i, ev := range st.Events {
		key := fmt.Sprintf("%s/%d/%d", id, st.Ordinal, i)
		if _, err := c.Exec("INSERT INTO outbox(key, op_id, topic, payload, created) VALUES (?,?,?,?,?)", key, id, ev.Topic, ev.Payload, now); err != nil {
			return err
		}
	}
	return nil
}

// DeliverOutbox hands every undelivered event to sink, stopping at the first
// failure so that order holds.
func (e *Engine) DeliverOutbox(ctx context.Context, sink Sink) error {
	e.deliver.Lock()
	defer e.deliver.Unlock()
	var evs []OutboxEvent
	err := e.Read(func(c store.Conn) error {
		return c.Query("SELECT seq, key, op_id, topic, payload FROM outbox WHERE delivered IS NULL ORDER BY seq", nil, func(r store.Row) error {
			evs = append(evs, OutboxEvent{Seq: r.Int64(0), Key: r.Text(1), OpID: r.Text(2), Topic: r.Text(3), Payload: r.Text(4)})
			return nil
		})
	})
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sink.Deliver(ctx, ev); err != nil {
			return fmt.Errorf("ops: deliver %s: %w", ev.Key, err)
		}
		e.fault("outbox:%d:sent", ev.Seq)
		if err := e.tx(func(c store.Conn) error {
			_, err := c.Exec("UPDATE outbox SET delivered = ? WHERE seq = ?", e.now(), ev.Seq)
			return err
		}); err != nil {
			return err
		}
		e.fault("outbox:%d:recorded", ev.Seq)
	}
	return nil
}

// Undelivered counts the events still waiting.
func (e *Engine) Undelivered() (int, error) {
	n := 0
	err := e.Read(func(c store.Conn) error {
		return c.Query("SELECT count(*) FROM outbox WHERE delivered IS NULL", nil, func(r store.Row) error { n = int(r.Int64(0)); return nil })
	})
	return n, err
}
