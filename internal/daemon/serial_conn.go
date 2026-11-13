package daemon

import (
	"sync"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// serialConn shares one connection between goroutines. The operation engine
// serializes its own calls but runs effects outside that lock, and an effect
// may write to this same account database.
type serialConn struct {
	mu sync.Mutex
	c  store.Conn
}

func newSerialConn(c store.Conn) *serialConn { return &serialConn{c: c} }

func (s *serialConn) Exec(query string, args ...any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Exec(query, args...)
}

func (s *serialConn) Query(query string, args []any, fn func(store.Row) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Query(query, args, fn)
}

// Tx holds the lock for the whole transaction and hands fn the connection
// underneath, so statements inside it do not wait on themselves.
func (s *serialConn) Tx(fn func(store.Conn) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Tx(fn)
}

func (s *serialConn) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.Close()
}
