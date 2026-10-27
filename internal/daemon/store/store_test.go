package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dir is a private folder (mode 700) for one test's databases.
func dir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "db")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func open(t *testing.T, path string, m Mode) Conn {
	t.Helper()
	c, err := Open(path, m, KindRoute)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readPragma(t *testing.T, c Conn, name string) string {
	t.Helper()
	var v string
	if err := c.Query("PRAGMA "+name, nil, func(r Row) error { v = r.Text(0); return nil }); err != nil {
		t.Fatalf("pragma %s: %v", name, err)
	}
	return v
}

// TestWriterPragmas: every pragma of design section 2 is in effect on a
// writer, read back through the connection itself.
func TestWriterPragmas(t *testing.T) {
	c := open(t, filepath.Join(dir(t), "r.db"), Writer)
	want := map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "0",
		"trusted_schema": "0", "cell_size_check": "1", "cache_size": "-512", "mmap_size": "0", "temp_store": "2",
		"wal_autocheckpoint": "1000", "query_only": "0"}
	if runtime.GOOS == "darwin" {
		want["fullfsync"], want["checkpoint_fullfsync"] = "1", "1"
	}
	for k, v := range want {
		if got := readPragma(t, c, k); got != v {
			t.Errorf("writer %s = %s, want %s", k, got, v)
		}
	}
}

// TestPragmaListIsTheDesign: the settings Open applies and reads back are
// design section 2's, value for value. synchronous=FULL is also the
// current driver's default, so only this list (not a read-back) shows it
// is asked for; a driver whose WAL default is NORMAL would otherwise lose
// durability silently.
func TestPragmaListIsTheDesign(t *testing.T) {
	want := map[Mode]map[string]string{
		Writer: {"journal_mode": "WAL", "synchronous": "FULL", "foreign_keys": "ON", "busy_timeout": "0",
			"trusted_schema": "OFF", "cell_size_check": "ON", "cache_size": "-512", "mmap_size": "0",
			"temp_store": "MEMORY", "wal_autocheckpoint": "1000"},
		Reader: {"busy_timeout": "5000", "foreign_keys": "ON", "trusted_schema": "OFF", "cache_size": "-512",
			"mmap_size": "0", "temp_store": "MEMORY", "query_only": "ON"},
	}
	if runtime.GOOS == "darwin" {
		want[Writer]["fullfsync"], want[Writer]["checkpoint_fullfsync"] = "ON", "ON"
	}
	for m, w := range want {
		got := map[string]string{}
		for _, p := range pragmas(m) {
			got[p.name] = p.set
		}
		for k, v := range w {
			if got[k] != v {
				t.Errorf("%v: pragma %s set to %q, want %q", m, k, got[k], v)
			}
		}
		if len(got) != len(w) {
			t.Errorf("%v: %d pragmas, want %d", m, len(got), len(w))
		}
	}
}

// TestReaderReadOnly: a reader is read-only (mode=ro and query_only), waits
// 5 s for a busy writer, and sees the writer's committed rows.
func TestReaderReadOnly(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	w := open(t, p, Writer)
	if err := w.Tx(func(tx Conn) error {
		if _, err := tx.Exec("CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT, b BLOB) STRICT"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO t VALUES (?, ?, ?)", 1, "one", []byte{0, 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := open(t, p, Reader)
	for k, v := range map[string]string{"query_only": "1", "busy_timeout": "5000", "foreign_keys": "1", "trusted_schema": "0"} {
		if got := readPragma(t, r, k); got != v {
			t.Errorf("reader %s = %s, want %s", k, got, v)
		}
	}
	var k int64
	var s string
	var b []byte
	var null bool
	if err := r.Query("SELECT k, v, b, NULL FROM t", nil, func(row Row) error {
		k, s, b, null = row.Int64(0), row.Text(1), row.Bytes(2), row.Null(3)
		return nil
	}); err != nil || k != 1 || s != "one" || string(b) != "\x00\x01" || !null {
		t.Fatalf("row %d %q %v %v: %v", k, s, b, null, err)
	}
	if _, err := r.Exec("INSERT INTO t VALUES (2, 'two', NULL)"); err == nil {
		t.Fatal("a reader wrote")
	}
	if _, err := Open(filepath.Join(filepath.Dir(p), "missing.db"), Reader, KindRoute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader of a missing file: %v", err)
	}
}

// TestTx: a writer transaction commits or rolls back whole, and nests not.
func TestTx(t *testing.T) {
	c := open(t, filepath.Join(dir(t), "r.db"), Writer)
	if _, err := c.Exec("CREATE TABLE t (k INTEGER PRIMARY KEY) STRICT"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := c.Tx(func(tx Conn) error {
		if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("tx error %v", err)
	}
	if err := c.Tx(func(tx Conn) error {
		if err := tx.Tx(func(Conn) error { return nil }); err == nil {
			return errors.New("nested transaction accepted")
		}
		n, err := tx.Exec("INSERT INTO t VALUES (2)")
		if n != 1 {
			return errors.New("rows affected")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var keys []int64
	_ = c.Query("SELECT k FROM t ORDER BY k", nil, func(r Row) error { keys = append(keys, r.Int64(0)); return nil })
	if len(keys) != 1 || keys[0] != 2 {
		t.Fatalf("rows after rollback and commit: %v", keys)
	}
}

// TestOpenRefusesUnsafeFiles: a folder or file of the wrong mode, a link,
// a FIFO (refused without blocking) and a linked WAL file are refused.
func TestOpenRefusesUnsafeFiles(t *testing.T) {
	// made is a database file a writer created and closed.
	made := func(d string) string {
		p := filepath.Join(d, "r.db")
		c, err := Open(p, Writer, KindRoute)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		return p
	}
	// Each case breaks one file and names the refusal Open must give.
	cases := map[string]func(d string) (path, want string){
		"folder mode": func(d string) (string, string) {
			_ = os.Chmod(d, 0o755)
			return filepath.Join(d, "r.db"), "folder " + d + " has mode 755"
		},
		"file mode": func(d string) (string, string) {
			p := filepath.Join(d, "r.db")
			_ = os.WriteFile(p, nil, 0o644)
			return p, p + " has mode 644"
		},
		"file link": func(d string) (string, string) {
			p := filepath.Join(d, "r.db")
			_ = os.WriteFile(filepath.Join(d, "real.db"), nil, 0o600)
			_ = os.Symlink(filepath.Join(d, "real.db"), p)
			return p, "open " + p
		},
		"folder link": func(d string) (string, string) {
			l := filepath.Join(filepath.Dir(d), "link")
			_ = os.Symlink(d, l)
			return filepath.Join(l, "r.db"), l + " is not a folder"
		},
		"fifo": func(d string) (string, string) {
			p := filepath.Join(d, "r.db")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Skipf("sandbox: mkfifo: %v", err)
			}
			return p, p + " is not a regular file"
		},
		"wal link": func(d string) (string, string) {
			p := made(d)
			_ = os.Remove(p + "-wal")
			_ = os.Symlink(filepath.Join(d, "elsewhere"), p+"-wal")
			return p, p + "-wal is not a regular file"
		},
		"wal mode": func(d string) (string, string) {
			p := made(d)
			_ = os.WriteFile(p+"-wal", nil, 0o644)
			return p, p + "-wal has mode 644"
		},
		"shm link": func(d string) (string, string) {
			p := made(d)
			_ = os.Symlink(filepath.Join(d, "elsewhere"), p+"-shm")
			return p, p + "-shm is not a regular file"
		},
		"shm mode": func(d string) (string, string) {
			p := made(d)
			_ = os.WriteFile(p+"-shm", nil, 0o644)
			return p, p + "-shm has mode 644"
		},
		"relative": func(string) (string, string) { return "r.db", `"r.db" is not absolute` },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p, want := setup(dir(t))
			for _, m := range []Mode{Writer, Reader} {
				done := make(chan error, 1)
				go func() {
					c, err := Open(p, m, KindRoute)
					if err == nil {
						_ = c.Close()
					}
					done <- err
				}()
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), "store: ") || !strings.Contains(err.Error(), want) {
						t.Fatalf("mode %v: got %v, want an error containing %q", m, err, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("mode %v: open blocked", m)
				}
			}
		})
	}
}

// TestNewFileMode: a writer creates its file with mode 600 and refuses a
// kind it does not know.
func TestNewFileMode(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	_ = open(t, p, Writer)
	st, err := os.Lstat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v: %v", st.Mode(), err)
	}
	if _, err := Open(filepath.Join(filepath.Dir(p), "x.db"), Writer, Kind("other")); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}
