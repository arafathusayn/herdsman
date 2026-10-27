package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// replaced runs Open of a closed database while before and after (the
// driver-open hooks) replace files, and wants a refusal naming want
// within a bounded time.
func replaced(t *testing.T, m Mode, before, after func(p string), want string) {
	t.Helper()
	p := filepath.Join(dir(t), "r.db")
	w, err := Open(p, Writer, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	beforeDriverOpen, afterDriverOpen = before, after
	t.Cleanup(func() { beforeDriverOpen, afterDriverOpen = nil, nil })
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
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want an error containing %q", m, err, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%v: Open blocked", m)
	}
}

// swap moves the file at n away and puts make(n) in its place.
func swap(t *testing.T, n string, make func(n string) error) {
	if err := os.Rename(n, n+".away"); err != nil {
		t.Error(err)
		return
	}
	if err := make(n); err != nil {
		t.Error(err)
	}
}

func fifo(n string) error { return syscall.Mkfifo(n, 0o600) }

// other is a regular mode 600 database file that is not the checked one.
func other(n string) error {
	b, err := os.ReadFile(n + ".away")
	if err != nil {
		return err
	}
	return os.WriteFile(n, b, 0o600)
}

// TestDriverOpenOfReplacedFile (S1-1 F1): the files SQLite opens are the
// checked ones. A database or WAL file replaced by a FIFO between the
// checks and SQLite's own open does not block Open, and a database
// swapped for another file (and back again) is refused.
func TestDriverOpenOfReplacedFile(t *testing.T) {
	for _, m := range []Mode{Reader, Writer} {
		t.Run(m.String()+"/db fifo", func(t *testing.T) {
			replaced(t, m, func(p string) { swap(t, p, fifo) }, nil, "unable to open database file")
		})
		t.Run(m.String()+"/db other file", func(t *testing.T) {
			replaced(t, m, func(p string) { swap(t, p, other) }, nil, "changed while it was opened")
		})
		t.Run(m.String()+"/db swapped and back", func(t *testing.T) {
			replaced(t, m, func(p string) { swap(t, p, other) }, func(p string) {
				if err := os.Rename(p+".away", p); err != nil {
					t.Error(err)
				}
			}, "SQLite does not hold the checked file")
		})
		t.Run(m.String()+"/wal fifo before the open", func(t *testing.T) {
			replaced(t, m, func(p string) { swap(t, p+"-wal", fifo) }, nil, "unable to open database file")
		})
		t.Run(m.String()+"/shm fifo before the open", func(t *testing.T) {
			replaced(t, m, func(p string) { swap(t, p+"-shm", fifo) }, nil, "unable to open database file")
		})
		t.Run(m.String()+"/wal fifo after the open", func(t *testing.T) {
			replaced(t, m, nil, func(p string) { swap(t, p+"-wal", fifo) }, "r.db-wal is not a regular file")
		})
	}
}

// TestCheckedOpenInstalled: SQLite's VFS opens files through the checked
// open, and that open refuses a FIFO, a link, a foreign mode and a
// folder of another mode without blocking.
func TestCheckedOpenInstalled(t *testing.T) {
	if err := installCheckedOpen(); err != nil || !checkedOpenInstalled() {
		t.Fatalf("checked open not installed: %v", err)
	}
	d := dir(t)
	for name, make := range map[string]func(p string) error{
		"fifo":       fifo,
		"link":       func(p string) error { return os.Symlink(filepath.Join(d, "real.db"), p) },
		"mode 644":   func(p string) error { return os.WriteFile(p, nil, 0o644) },
		"folder 755": func(p string) error { return os.Mkdir(p, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(d, strings.ReplaceAll(name, " ", "_"))
			if err := make(p); err != nil {
				t.Fatal(err)
			}
			if tryCheckedOpen(p) {
				t.Fatalf("checked open accepted %s", name)
			}
		})
	}
	ok := filepath.Join(d, "ok.db")
	if err := os.WriteFile(ok, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !tryCheckedOpen(ok) {
		t.Fatal("checked open refused a regular 600 file")
	}
}

// TestNewerSchemaFileUnchanged (S2S3-1): a newer schema in rollback
// journal mode is refused by the writer before any setting changes the
// file: its bytes are the same after the refusal.
func TestNewerSchemaFileUnchanged(t *testing.T) {
	d := dir(t)
	p := filepath.Join(d, "r.db")
	c := writerAt(t, p)
	if err := migrate(c, p, KindRoute, schemaFS); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"PRAGMA journal_mode = DELETE", "PRAGMA user_version = 99"} {
		if _, err := c.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Close()
	for _, side := range []string{"-wal", "-shm"} {
		_ = os.Remove(p + side)
	}
	before, err := os.ReadFile(p)
	if err != nil || before[18] != 1 {
		t.Fatalf("fixture not in rollback mode: %v", err)
	}
	if _, err := Open(p, Writer, KindRoute); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("writer of a newer file: %v", err)
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(before) {
		t.Fatalf("the refused file changed (header bytes 18-19 %v -> %v)", before[18:20], after[18:20])
	}
	if l := listing(t, d); len(l) != 1 {
		t.Fatalf("files after the refusal: %v", l)
	}
}

// TestNegativeSchemaVersionCorrupt (S2S3-1): a negative user_version is a
// corrupt schema for both modes, never a panic.
func TestNegativeSchemaVersionCorrupt(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	c := open(t, p, Writer)
	if _, err := c.Exec("PRAGMA user_version = -1"); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	for _, m := range []Mode{Writer, Reader} {
		if _, err := Open(p, m, KindRoute); !errors.Is(err, ErrSchemaCorrupt) {
			t.Errorf("%v: %v", m, err)
		}
	}
	// Without side files the writer decides from the header alone, and
	// creates nothing.
	for _, side := range []string{"-wal", "-shm"} {
		if err := os.Rename(p+side, p+side+".away"); err != nil {
			t.Fatal(err)
		}
	}
	before := listing(t, filepath.Dir(p))
	if _, err := Open(p, Writer, KindRoute); !errors.Is(err, ErrSchemaCorrupt) {
		t.Fatalf("writer without side files: %v", err)
	}
	if after := listing(t, filepath.Dir(p)); len(after) != len(before) {
		t.Fatalf("files created: before %v, after %v", before, after)
	}
	w := writerAt(t, filepath.Join(dir(t), "x.db"))
	if _, err := w.Exec("PRAGMA user_version = -1"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(w, "", KindRoute, schemaFS); !errors.Is(err, ErrSchemaCorrupt) {
		t.Fatalf("migrate: %v", err)
	}
}
