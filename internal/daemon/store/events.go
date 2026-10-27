package store

import (
	"errors"
	"fmt"
	"strconv"
)

// The route's stored event stream (design section 3, 5.3 of the note):
// seq comes from meta.next_seq and is never reused in an epoch, not after
// retention and not after reset-events.

// Event is one row of events.
type Event struct {
	Seq       int64
	Epoch     string
	Time      int64
	Type      string
	Recipient string // "" for an event both parties see
	Data      string
}

// Replay refusals; the stream answers each with a reset.
var (
	ErrCursorExpired = errors.New("cursor_expired")
	ErrEpochChanged  = errors.New("epoch_changed")
)

// Retention bounds (design section 3).
const (
	KeepEvents     = 20000
	retentionBatch = 1000
	ReplayPage     = 64
)

// metaGet reads one meta value ("" when absent).
func metaGet(c Conn, k string) (string, error) {
	v := ""
	err := c.Query("SELECT v FROM meta WHERE k = ?", []any{k}, func(r Row) error { v = r.Text(0); return nil })
	return v, err
}

func metaInt(c Conn, k string) (int64, error) {
	v, err := metaGet(c, k)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("store: meta %s = %q: %w", k, v, ErrSchemaCorrupt)
	}
	return n, nil
}

func metaSet(c Conn, k, v string) error {
	_, err := c.Exec("INSERT INTO meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v", k, v)
	return err
}

// InitEvents gives a new route its first epoch, next_seq 1 and
// oldest_seq 1; a route that has them keeps them.
func InitEvents(c Conn, epoch string) error {
	for k, v := range map[string]string{"epoch": epoch, "next_seq": "1", "oldest_seq": "1"} {
		if _, err := c.Exec("INSERT OR IGNORE INTO meta(k, v) VALUES (?, ?)", k, v); err != nil {
			return err
		}
	}
	return nil
}

// AppendEvent inserts e at meta.next_seq in the current epoch and raises
// next_seq; it runs inside the operation's transaction, and refuses to
// run outside one (its two statements must commit together).
func AppendEvent(tx Conn, e Event) (Event, error) {
	if t, ok := tx.(interface{ inTransaction() bool }); !ok || !t.inTransaction() {
		return e, errors.New("store: AppendEvent runs only inside the operation's transaction")
	}
	seq, err := metaInt(tx, "next_seq")
	if err != nil {
		return e, err
	}
	epoch, err := metaGet(tx, "epoch")
	if err != nil {
		return e, err
	}
	var recipient any
	if e.Recipient != "" {
		recipient = e.Recipient
	}
	if _, err := tx.Exec("INSERT INTO events(seq, epoch, time, type, recipient, data) VALUES (?, ?, ?, ?, ?, ?)",
		seq, epoch, e.Time, e.Type, recipient, e.Data); err != nil {
		return e, err
	}
	if err := metaSet(tx, "next_seq", strconv.FormatInt(seq+1, 10)); err != nil {
		return e, err
	}
	e.Seq, e.Epoch = seq, epoch
	return e, nil
}

// VisibleSeq is the last committed seq (0 before the first event).
func VisibleSeq(c Conn) (int64, error) {
	n, err := metaInt(c, "next_seq")
	return n - 1, err
}

// ReadEvents returns at most limit events of epoch with cursor < seq <=
// barrier, oldest first. A cursor of another epoch gets ErrEpochChanged;
// one below the kept window gets ErrCursorExpired.
func ReadEvents(c Conn, epoch string, cursor, barrier int64, limit int) ([]Event, error) {
	cur, err := metaGet(c, "epoch")
	if err != nil {
		return nil, err
	}
	if epoch != cur {
		return nil, ErrEpochChanged
	}
	oldest, err := metaInt(c, "oldest_seq")
	if err != nil {
		return nil, err
	}
	if cursor+1 < oldest {
		return nil, ErrCursorExpired
	}
	var out []Event
	err = c.Query("SELECT seq, epoch, time, type, recipient, data FROM events WHERE seq > ? AND seq <= ? AND epoch = ? ORDER BY seq LIMIT ?",
		[]any{cursor, barrier, epoch, limit}, func(r Row) error {
			out = append(out, Event{Seq: r.Int64(0), Epoch: r.Text(1), Time: r.Int64(2), Type: r.Text(3), Recipient: r.Text(4), Data: r.Text(5)})
			return nil
		})
	return out, err
}

// PruneEvents deletes one batch of at most 1000 events below the newest
// keep and raises meta.oldest_seq; it reports how many it deleted, so the
// scheduler runs one transaction per batch until it gets 0.
func PruneEvents(c Conn, keep int64) (int64, error) {
	var n int64
	err := c.Tx(func(tx Conn) error {
		next, err := metaInt(tx, "next_seq")
		if err != nil {
			return err
		}
		below := next - keep
		if n, err = tx.Exec("DELETE FROM events WHERE seq IN (SELECT seq FROM events WHERE seq < ? ORDER BY seq LIMIT ?)", below, retentionBatch); err != nil {
			return err
		}
		oldest := below
		err = tx.Query("SELECT min(seq) FROM events", nil, func(r Row) error {
			if !r.Null(0) && r.Int64(0) < oldest {
				oldest = r.Int64(0)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if cur, err := metaInt(tx, "oldest_seq"); err != nil || oldest <= cur {
			return err
		}
		return metaSet(tx, "oldest_seq", strconv.FormatInt(oldest, 10))
	})
	return n, err
}

// ResetEvents starts a new epoch with no events; next_seq stays, so no
// seq is issued twice (reset-events, design section 3).
func ResetEvents(c Conn, epoch string) error {
	return c.Tx(func(tx Conn) error {
		next, err := metaInt(tx, "next_seq")
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM events"); err != nil {
			return err
		}
		if err := metaSet(tx, "epoch", epoch); err != nil {
			return err
		}
		return metaSet(tx, "oldest_seq", strconv.FormatInt(next, 10))
	})
}
