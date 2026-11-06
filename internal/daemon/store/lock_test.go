package store

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// sqliteSharedFirst and sqliteSharedSize are SQLite's SHARED lock range
// on a database file (PENDING_BYTE + 2, 510 bytes); sqliteShmDMS is the
// byte of a -shm file every connection of a WAL database read-locks while
// it uses that file (the DMS slot, 120 + 8).
const (
	sqliteSharedFirst = 0x40000002
	sqliteSharedSize  = 510
	sqliteShmDMS      = 128
)

// TestHelperLockProbe runs in a child process: it asks the kernel, with
// F_GETLK, whether another process holds a lock in SQLite's SHARED range
// of the file named by HERDSMAN_LOCK_PROBE (or at the byte
// HERDSMAN_LOCK_BYTE), and prints "held" or "free".
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
	if b := os.Getenv("HERDSMAN_LOCK_BYTE"); b != "" {
		lk.Len = 1
		if _, err := fmt.Sscan(b, &lk.Start); err != nil {
			fmt.Println("error", err)
			return
		}
	}
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

// lockState asks a child process whether a lock is held on p, in
// SQLite's SHARED range or, with env, as the probe's environment says.
func lockState(t *testing.T, p string, env ...string) string {
	t.Helper()
	cmd := osexec.Command(os.Args[0], "-test.run=^TestHelperLockProbe$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), "HERDSMAN_LOCK_PROBE="+p), env...)
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

// TestRefusedOpenKeepsSQLiteLocks (S27N1-1 F2): an open this process
// refuses never closes a descriptor of a file a connection locks. With a
// writer open on the database, its mode is made 644 and the database is
// opened again: refused by Open's check of its kept descriptor (reader
// and writer), or by the checked open inside SQLite (the mode changes
// after Open's check), or the checked open is asked for the database or
// its -shm directly. Each refusal leaves the writer's locks held, and a
// descriptor the checked open parked is closed once no connection holds
// its inode.
func TestRefusedOpenKeepsSQLiteLocks(t *testing.T) {
	dms := "HERDSMAN_LOCK_BYTE=" + strconv.Itoa(sqliteShmDMS)
	for _, tc := range []struct {
		name, file string
		env        []string
		chmodFirst bool // else refuse makes the mode 644 itself
		refuse     func(p string) error
	}{
		{"reader", "", nil, true, func(p string) error { return openRefused(p, Reader, nil) }},
		{"writer", "", nil, true, func(p string) error { return openRefused(p, Writer, nil) }},
		{"reader refused by the checked open", "", nil, false, func(p string) error {
			return openRefused(p, Reader, func(n string) { _ = os.Chmod(n, 0o644) })
		}},
		{"checked open of the database", "", nil, true, checkedOpenRefused},
		{"checked open of the -shm", "-shm", []string{dms}, true, checkedOpenRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir(t), "r.db")
			w, err := Open(p, Writer, KindRoute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Exec("CREATE TABLE t (k INTEGER) STRICT"); err != nil {
				t.Fatal(err)
			}
			locked := p + tc.file
			if got := lockState(t, locked, tc.env...); got != "held" {
				t.Fatalf("before the refused open: lock %s", got)
			}
			if tc.chmodFirst {
				if err := os.Chmod(locked, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.refuse(locked); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(locked, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := lockState(t, locked, tc.env...); got != "held" {
				t.Errorf("after the refused open: lock %s", got)
			}
			if _, err := w.Exec("INSERT INTO t VALUES (1)"); err != nil {
				t.Errorf("writer after the refused open: %v", err)
			}
			_ = w.Close()
			if n := closeParked(); n != 0 {
				t.Errorf("%d refused descriptors still parked after the last close", n)
			}
			if got := lockState(t, locked, tc.env...); got != "free" {
				t.Errorf("after the last close: lock %s, want free", got)
			}
		})
	}
}

// openRefused opens p in mode m, with before as the hook just before the
// driver opens it, and wants a refusal.
func openRefused(p string, m Mode, before func(string)) error {
	beforeDriverOpen = before
	defer func() { beforeDriverOpen = nil }()
	c, err := Open(p, m, KindRoute)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("%v open of a mode 644 database accepted", m)
	}
	return nil
}

func checkedOpenRefused(p string) error {
	if tryCheckedOpen(p) {
		return fmt.Errorf("checked open accepted mode 644 %s", p)
	}
	return nil
}
