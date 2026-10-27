package store

// The provisional driver adapter: modernc.org/sqlite (pure Go, no cgo)
// until the sqlite-bench result picks the driver (design section 9, BENCH
// SLOT). Swapping the driver replaces this file only.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

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
	q := url.Values{"mode": {"rw"}}
	if m == Reader {
		q.Set("mode", "ro")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return nil, err
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
	st, err := s.stmt(q)
	if err != nil {
		return 0, err
	}
	res, err := st.Exec(args...)
	if err != nil {
		return 0, err
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
