// Package store is the daemon's SQLite storage layer (design delta
// sqlite-storage.md, sections 2 and 9). Code outside it never imports a
// driver: it uses Conn and Row, and Open, which checks the files, opens the
// database through the one driver adapter (driver_*.go) and applies and
// reads back the pragmas.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// Conn is one SQLite connection; the writer is used by one goroutine.
type Conn interface {
	// Exec runs one statement from the prepared-statement cache.
	Exec(query string, args ...any) (rowsAffected int64, err error)
	// Query runs one statement and calls fn per row; Row reads typed columns.
	Query(query string, args []any, fn func(Row) error) error
	// Tx runs fn in BEGIN IMMEDIATE … COMMIT (writer) or BEGIN … COMMIT
	// (reader); an error from fn rolls back. Transactions do not nest.
	Tx(fn func(Conn) error) error
	Close() error
}

// Row is the current row of a Query.
type Row interface {
	Int64(i int) int64
	Text(i int) string
	Bytes(i int) []byte // copied; valid after the row
	Null(i int) bool
}

// Mode is a connection's role: the one writer, or a read-only reader.
type Mode int

const (
	Writer Mode = iota
	Reader
)

func (m Mode) String() string {
	if m == Writer {
		return "writer"
	}
	return "reader"
}

// Kind is a database's schema kind (design section 1.1).
type Kind string

const (
	KindAccount Kind = "account"
	KindRoute   Kind = "route"
)

// pragma is one setting and the value PRAGMA <name> reads back.
type pragma struct{ name, set, want string }

// pragmas are the settings of design section 2, per mode, in order.
func pragmas(m Mode) []pragma {
	if m == Reader {
		return []pragma{
			{"busy_timeout", "5000", "5000"},
			{"foreign_keys", "ON", "1"},
			{"trusted_schema", "OFF", "0"},
			{"cache_size", "-512", "-512"},
			{"mmap_size", "0", "0"},
			{"temp_store", "MEMORY", "2"},
			{"query_only", "ON", "1"},
		}
	}
	p := []pragma{
		{"journal_mode", "WAL", "wal"},
		{"synchronous", "FULL", "2"},
		{"foreign_keys", "ON", "1"},
		{"busy_timeout", "0", "0"},
		{"trusted_schema", "OFF", "0"},
		{"cell_size_check", "ON", "1"},
		{"cache_size", "-512", "-512"},
		{"mmap_size", "0", "0"},
		{"temp_store", "MEMORY", "2"},
		{"wal_autocheckpoint", "1000", "1000"},
	}
	if runtime.GOOS == "darwin" {
		// Plain fsync on darwin reaches only the drive cache.
		p = append(p, pragma{"fullfsync", "ON", "1"}, pragma{"checkpoint_fullfsync", "ON", "1"})
	}
	return p
}

// Open opens path as the writer or a reader of a database of kind. The
// folder must be mode 700 and the file (and its -wal and -shm, when
// present) a regular file of mode 600, owned by this user, never a link;
// a writer creates a missing file with mode 600, a reader of a missing
// file gets an error wrapping os.ErrNotExist. Every pragma is read back,
// then the writer migrates the schema and a reader checks it (section 1).
func Open(path string, m Mode, kind Kind) (Conn, error) {
	if kind != KindAccount && kind != KindRoute {
		return nil, fmt.Errorf("store: unknown kind %q", kind)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("store: %q is not absolute", path)
	}
	if err := checkFolder(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := openRegular(path, m == Writer)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for _, side := range []string{"-wal", "-shm"} {
		if err := checkSide(path + side); err != nil {
			return nil, err
		}
	}
	c, err := openDriver(path, m)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// The file SQLite opened is the one checked: same inode at the path.
	if err := sameFile(f, path); err != nil {
		_ = c.Close()
		return nil, err
	}
	for _, p := range pragmas(m) {
		if _, err := c.Exec("PRAGMA " + p.name + " = " + p.set); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("store: pragma %s: %w", p.name, err)
		}
	}
	for _, p := range pragmas(m) {
		got := ""
		if err := c.Query("PRAGMA "+p.name, nil, func(r Row) error { got = r.Text(0); return nil }); err != nil || got != p.want {
			_ = c.Close()
			return nil, fmt.Errorf("store: pragma %s reads %q, want %q (%v)", p.name, got, p.want, err)
		}
	}
	// The schema is checked before any other statement: the writer
	// migrates to this binary's version, a reader refuses any other.
	if m == Writer {
		err = migrate(c, path, kind, schemaFS)
	} else {
		_, err = checkSchema(c, Reader, kind, Newest(kind))
	}
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// checkFolder requires a real folder of mode 700 owned by this user.
func checkFolder(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("store: folder %s: %w", dir, err)
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("store: %s is not a folder", dir)
	}
	if st.Mode().Perm() != 0o700 {
		return fmt.Errorf("store: folder %s has mode %o, want 700", dir, st.Mode().Perm())
	}
	return checkOwner(st, dir)
}

func checkOwner(st os.FileInfo, p string) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok && int(s.Uid) != os.Getuid() {
		return fmt.Errorf("store: %s is not owned by this user", p)
	}
	return nil
}

// checkRegular requires a regular file of mode 600 owned by this user.
func checkRegular(st os.FileInfo, p string) error {
	if !st.Mode().IsRegular() {
		return fmt.Errorf("store: %s is not a regular file", p)
	}
	if st.Mode().Perm() != 0o600 {
		return fmt.Errorf("store: %s has mode %o, want 600", p, st.Mode().Perm())
	}
	return checkOwner(st, p)
}

// openRegular opens the database file without following a link and
// without blocking on a FIFO, and checks it; create makes a missing file.
func openRegular(p string, create bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(p, flags, 0)
	if errors.Is(err, os.ErrNotExist) && create {
		f, err = os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("store: %s: %w", p, err)
		}
		return nil, fmt.Errorf("store: open %s: %v", p, err)
	}
	st, err := f.Stat()
	if err == nil {
		err = checkRegular(st, p)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// checkSide checks a -wal or -shm file when it exists.
func checkSide(p string) error {
	st, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: %s: %v", p, err)
	}
	return checkRegular(st, p)
}

// sameFile reports a path that no longer names the checked file.
func sameFile(f *os.File, p string) error {
	a, err1 := f.Stat()
	b, err2 := os.Lstat(p)
	if err1 != nil || err2 != nil || !os.SameFile(a, b) {
		return fmt.Errorf("store: %s changed while it was opened", p)
	}
	return nil
}
