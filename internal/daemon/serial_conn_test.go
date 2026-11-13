package daemon

import (
	"errors"
	"sync"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// oneAtATime fails when two calls overlap, which is what a shared connection
// must never see.
type oneAtATime struct {
	mu       sync.Mutex
	busy     bool
	overlaps int
	closed   bool
}

func (c *oneAtATime) enter() func() {
	c.mu.Lock()
	if c.busy {
		c.overlaps++
	}
	c.busy = true
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
	}
}

func (c *oneAtATime) Exec(string, ...any) (int64, error) { defer c.enter()(); return 7, nil }
func (c *oneAtATime) Query(string, []any, func(store.Row) error) error {
	defer c.enter()()
	return nil
}
func (c *oneAtATime) Tx(fn func(store.Conn) error) error { defer c.enter()(); return fn(c) }
func (c *oneAtATime) Close() error {
	defer c.enter()()
	c.closed = true
	return errors.New("closed")
}

func TestSerialConnKeepsCallsApart(t *testing.T) {
	inner := &oneAtATime{}
	shared := newSerialConn(inner)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = shared.Exec("x")
				_ = shared.Query("x", nil, nil)
				_ = shared.Tx(func(store.Conn) error { return nil })
			}
		}()
	}
	wg.Wait()
	if inner.overlaps != 0 {
		t.Fatalf("%d calls overlapped", inner.overlaps)
	}
	if n, err := shared.Exec("x"); n != 7 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	if err := shared.Close(); err == nil || !inner.closed {
		t.Fatalf("%v %v", err, inner.closed)
	}
}

// The transaction hands back the connection underneath, because a statement
// inside it already holds the lock.
func TestSerialConnTransactionUsesTheInnerConnection(t *testing.T) {
	inner := &oneAtATime{}
	shared := newSerialConn(inner)
	err := shared.Tx(func(c store.Conn) error {
		if c != store.Conn(inner) {
			t.Errorf("transaction got %T", c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
