package store

// The driver adapter: github.com/mattn/go-sqlite3 (cgo), the driver the
// sqlite-bench result picked (design section 9). Swapping the driver
// replaces this file and vfs_unix.go only.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	// The checked open (vfs_unix.go) replaces open in SQLite's unix VFS
	// for the whole process, and Open's kept descriptors assume no other
	// SQLite connection: the store must stay the only SQLite user in the
	// binary.
	sqlite3 "github.com/mattn/go-sqlite3"
)

// fcntlPersistWAL is SQLITE_FCNTL_PERSIST_WAL.
const fcntlPersistWAL = 10

// sqlConn is a Conn on one database/sql connection.
type sqlConn struct {
	db     *sql.DB
	c      *sql.Conn
	mode   Mode
	stmts  map[string]*sql.Stmt
	inTx   bool
	broken error // set when the connection was retired; every call returns it
}

func openDriver(path string, m Mode) (Conn, error) {
	if err := installCheckedOpen(); err != nil {
		return nil, err
	}
	q := url.Values{"mode": {"rw"}}
	if m == Reader {
		q.Set("mode", "ro")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if m == Writer {
		// The writer keeps its -wal and -shm after a clean close, so a
		// reader of a closed database never has to create them (S1-1 F4).
		err = c.Raw(func(dc any) error {
			sc, ok := dc.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("not a go-sqlite3 connection")
			}
			return sc.SetFileControlInt("main", fcntlPersistWAL, 1)
		})
		if err != nil {
			_ = c.Close()
			_ = db.Close()
			return nil, fmt.Errorf("persist WAL: %w", err)
		}
	}
	return &sqlConn{db: db, c: c, mode: m, stmts: map[string]*sql.Stmt{}}, nil
}

func (s *sqlConn) stmt(q string) (*sql.Stmt, error) {
	if s.broken != nil {
		return nil, s.broken
	}
	if st, ok := s.stmts[q]; ok {
		return st, nil
	}
	st, err := s.c.PrepareContext(context.Background(), q)
	if err != nil {
		return nil, err
	}
	s.stmts[q] = st
	return st, nil
}

func (s *sqlConn) Exec(q string, args ...any) (int64, error) {
	var res sql.Result
	if len(q) >= 6 && strings.EqualFold(q[:6], "PRAGMA") {
		// A PRAGMA that answers a row would leave a cached statement
		// active (go-sqlite3 resets it only at its next use), which makes
		// the next COMMIT fail: run it uncached, finalized at once.
		if s.broken != nil {
			return 0, s.broken
		}
		r, err := s.c.ExecContext(context.Background(), q, args...)
		if err != nil {
			return 0, err
		}
		res = r
	} else {
		st, err := s.stmt(q)
		if err != nil {
			return 0, err
		}
		if res, err = st.Exec(args...); err != nil {
			return 0, err
		}
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *sqlConn) Query(q string, args []any, fn func(Row) error) error {
	st, err := s.stmt(q)
	if err != nil {
		return err
	}
	rows, err := st.Query(args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := fn(row(vals)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// script runs several statements outside the statement cache (migration
// files only).
func (s *sqlConn) script(q string) error {
	if s.broken != nil {
		return s.broken
	}
	_, err := s.c.ExecContext(context.Background(), q)
	return err
}

// inTransaction reports a Tx in progress on this connection.
func (s *sqlConn) inTransaction() bool { return s.inTx }

func (s *sqlConn) Tx(fn func(Conn) error) error {
	if s.broken != nil {
		return s.broken
	}
	if s.inTx {
		return errors.New("store: transactions do not nest")
	}
	begin := "BEGIN IMMEDIATE"
	if s.mode == Reader {
		begin = "BEGIN"
	}
	if _, err := s.c.ExecContext(context.Background(), begin); err != nil {
		return err
	}
	s.inTx = true
	err := fn(s)
	s.inTx = false
	if err != nil {
		if _, rerr := s.c.ExecContext(context.Background(), "ROLLBACK"); rerr != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rerr)
		}
		return err
	}
	if _, err = s.c.ExecContext(context.Background(), "COMMIT"); err != nil {
		// A refused COMMIT (a deferred constraint, a busy or I/O error) can
		// leave the transaction open: roll it back, or retire the
		// connection when even that fails.
		if _, rerr := s.c.ExecContext(context.Background(), "ROLLBACK"); rerr != nil && !strings.Contains(rerr.Error(), "no transaction is active") {
			s.broken = fmt.Errorf("store: connection retired after a failed commit: %v (rollback: %v)", err, rerr)
			_ = s.Close()
		}
	}
	return err
}

func (s *sqlConn) Close() error {
	if s.stmts == nil {
		return nil
	}
	for _, st := range s.stmts {
		_ = st.Close()
	}
	s.stmts = nil
	err := s.c.Close()
	if e := s.db.Close(); err == nil {
		err = e
	}
	return err
}

// row reads the scanned values of one row.
type row []any

func (r row) Int64(i int) int64 {
	switch v := r[i].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func (r row) Text(i int) string {
	switch v := r[i].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return fmt.Sprint(v)
	case float64:
		return fmt.Sprint(v)
	}
	return ""
}

func (r row) Bytes(i int) []byte {
	switch v := r[i].(type) {
	case []byte:
		return append([]byte(nil), v...)
	case string:
		return []byte(v)
	}
	return nil
}

func (r row) Null(i int) bool { return r[i] == nil }
