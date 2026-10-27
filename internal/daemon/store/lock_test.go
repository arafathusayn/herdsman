package store

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// sqliteSharedFirst and sqliteSharedSize are SQLite's SHARED lock range
// on a database file (PENDING_BYTE + 2, 510 bytes).
const (
	sqliteSharedFirst = 0x40000002
	sqliteSharedSize  = 510
)

// TestHelperLockProbe runs in a child process: it asks the kernel, with
// F_GETLK, whether another process holds a lock in SQLite's SHARED range
// of the file named by HERDSMAN_LOCK_PROBE, and prints "held" or "free".
func TestHelperLockProbe(t *testing.T) {
	p := os.Getenv("HERDSMAN_LOCK_PROBE")
	if p == "" {
		t.Skip("lock probe helper")
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		fmt.Println("error", err)
		return
	}
	defer f.Close()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: sqliteSharedFirst, Len: sqliteSharedSize}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		fmt.Println("error", err)
		return
	}
	if lk.Type == syscall.F_UNLCK {
		fmt.Println("probe: free")
	} else {
		fmt.Println("probe: held")
	}
}

// lockState asks a child process whether a lock is held on p.
func lockState(t *testing.T, p string) string {
	t.Helper()
	cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperLockProbe$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HERDSMAN_LOCK_PROBE="+p)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lock probe: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "probe: ") {
			return strings.TrimPrefix(line, "probe: ")
		}
	}
	t.Fatalf("lock probe said nothing:\n%s", out)
	return ""
}

// TestOpenKeepsSQLiteLocks (S1-1 N1): Open never closes a descriptor of
// the database's inode while SQLite holds locks on it, since closing any
// descriptor of an inode drops every POSIX lock the process holds there.
// A connection inside a read transaction keeps its SHARED lock after
// Open returns, alone and after a reader of the same file is opened and
// closed in this process.
func TestOpenKeepsSQLiteLocks(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	w := open(t, p, Writer)
	if _, err := w.Exec("CREATE TABLE t (k INTEGER) STRICT"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	// A read transaction holds SHARED on the database file until it ends.
	held := func(c Conn, what string, during func()) {
		err := c.Tx(func(tx Conn) error {
			if err := tx.Query("SELECT count(*) FROM t", nil, func(Row) error { return nil }); err != nil {
				return err
			}
			if during != nil {
				during()
			}
			if got := lockState(t, p); got != "held" {
				t.Errorf("%s: SHARED lock %s", what, got)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	held(w, "writer opened through Open", nil)
	r := open(t, p, Reader)
	held(r, "reader opened through Open beside the writer", nil)
	held(w, "writer while a reader is opened and closed", func() {
		r2, err := Open(p, Reader, KindRoute)
		if err != nil {
			t.Fatal(err)
		}
		_ = r2.Close()
	})
}

// TestOpenReleasesKeptDescriptors: the descriptors Open keeps live only
// while a connection on their inode is open, also after a failed open.
func TestOpenReleasesKeptDescriptors(t *testing.T) {
	p := filepath.Join(dir(t), "r.db")
	w, err := Open(p, Writer, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p, Reader, KindAccount); err == nil {
		t.Fatal("a route file opened as an account database")
	}
	r, err := Open(p, Reader, KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	keptCount := func() (n int) {
		openMu.Lock()
		defer openMu.Unlock()
		for _, k := range kept {
			n += len(k.files)
		}
		return n
	}
	if n := keptCount(); n != 1 {
		t.Fatalf("%d descriptors kept for one inode, want 1", n)
	}
	_ = w.Close()
	if n := keptCount(); n != 1 {
		t.Fatalf("%d descriptors kept while a reader is open, want 1", n)
	}
	_ = r.Close()
	if n := keptCount(); n != 0 {
		t.Fatalf("%d descriptors kept after every connection closed", n)
	}
}
