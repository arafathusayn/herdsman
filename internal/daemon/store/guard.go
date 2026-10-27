package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"syscall"
)

// The checks of Open run on the files before SQLite opens them by name.
// SQLite's own opens are checked by the checked open (vfs_unix.go); after
// the open, every file SQLite holds must also be the checked file at its
// name, so a file swapped in and out again is refused (S1-1 F1).

// header is what Open needs from the database header, read through the
// checked descriptor, so deciding never opens anything else.
type header struct {
	empty       bool  // a new file: nothing written yet
	wal         bool  // read and write versions 2: the file is in WAL mode
	userVersion int32 // PRAGMA user_version (offset 60, big-endian)
}

var sqliteMagic = []byte("SQLite format 3\x00")

func readHeader(f *os.File, p string) (header, error) {
	var b [100]byte
	n, err := f.ReadAt(b[:], 0)
	if n == 0 && (err == nil || errors.Is(err, io.EOF)) {
		return header{empty: true}, nil
	}
	if n < len(b) || string(b[:16]) != string(sqliteMagic) {
		return header{}, fmt.Errorf("store: %s: %w: not a SQLite database", p, ErrSchemaCorrupt)
	}
	return header{
		wal:         b[18] == 2 && b[19] == 2,
		userVersion: int32(binary.BigEndian.Uint32(b[60:64])),
	}, nil
}

// sidesPresent reports whether both -wal and -shm exist (each already
// checked by checkSide when present).
func sidesPresent(p string) bool {
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(p + side); err != nil {
			return false
		}
	}
	return true
}

// missingSide names the first of -wal and -shm that does not exist.
func missingSide(p string) string {
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(p + side); err != nil {
			return p + side
		}
	}
	return ""
}

// Test hooks: called just before the driver opens path, and between the
// driver's open and the first statements (which open -wal and -shm).
var (
	beforeDriverOpen func(path string)
	afterDriverOpen  func(path string)
)

// openMu serializes the opens of this process and guards kept.
var openMu sync.Mutex

// kept holds Open's checked descriptors per inode until the last Conn on
// that inode closes. Closing any descriptor of an inode drops every POSIX
// lock the process holds on it, SQLite's included (a WAL connection holds
// SHARED on the database file for its whole life), so Open never closes
// one while a connection of this process may hold locks there (S1-N1).
// SQLite defers its own closes the same way; this covers ours.
var kept = map[inode]*keptFiles{}

type keptFiles struct {
	files []*os.File
	conns int
}

// keepOpen returns a checked descriptor of path and the release that
// gives it back: a kept descriptor of the inode at path when there is one,
// else a new one from openRegular, kept until its inode's last release.
// The caller holds openMu.
func keepOpen(path string, create bool) (*os.File, func(), error) {
	var f *os.File
	if st, err := os.Lstat(path); err == nil {
		if k := kept[inodeOf(st)]; k != nil {
			for _, kf := range k.files {
				if sameFile(kf, path) == nil {
					f = kf
					break
				}
			}
		}
	}
	if f == nil {
		var err error
		if f, err = openRegular(path, create); err != nil {
			return nil, nil, err
		}
	}
	st, err := f.Stat()
	if err != nil {
		// Never closed here: the inode may be one a connection locks.
		return nil, nil, fmt.Errorf("store: %s: %v", path, err)
	}
	key := inodeOf(st)
	k := kept[key]
	if k == nil {
		k = &keptFiles{}
		kept[key] = k
	}
	if !slices.Contains(k.files, f) {
		k.files = append(k.files, f)
	}
	k.conns++
	var once sync.Once
	return f, func() {
		once.Do(func() {
			openMu.Lock()
			defer openMu.Unlock()
			if k.conns--; k.conns == 0 {
				for _, kf := range k.files {
					_ = kf.Close()
				}
				delete(kept, key)
			}
		})
	}, nil
}

// keptConn releases its kept descriptor after the connection closes.
type keptConn struct {
	Conn
	release func()
}

func (c *keptConn) inTransaction() bool {
	t, ok := c.Conn.(interface{ inTransaction() bool })
	return ok && t.inTransaction()
}

func (c *keptConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}

func inodeOf(st os.FileInfo) inode {
	s := st.Sys().(*syscall.Stat_t)
	return inode{uint64(s.Dev), uint64(s.Ino)}
}

// openConn opens path through the driver for mode m, applies and reads
// back the pragmas, and checks that the files SQLite holds are the
// checked ones: f's inode at path, and the regular -wal and -shm at
// their names. SQLite's own opens go through the checked open
// (vfs_unix.go), so none of them blocks or accepts an unsafe file. The
// caller holds openMu.
func openConn(path string, m Mode, f *os.File) (Conn, error) {
	if beforeDriverOpen != nil {
		beforeDriverOpen(path)
	}
	c, err := openDriver(path, m)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if afterDriverOpen != nil {
		afterDriverOpen(path)
	}
	if err := configure(c, m, path); err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := checkHeld(path, f); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// configure sets and reads back the pragmas of m, then reads once, which
// opens the -wal and -shm of a WAL database.
func configure(c Conn, m Mode, path string) error {
	for _, p := range pragmas(m) {
		if _, err := c.Exec("PRAGMA " + p.name + " = " + p.set); err != nil {
			return fmt.Errorf("store: pragma %s: %w", p.name, err)
		}
	}
	if err := checkPragmas(c, m); err != nil {
		return err
	}
	if err := c.Query("SELECT count(*) FROM sqlite_schema", nil, func(Row) error { return nil }); err != nil {
		return fmt.Errorf("store: open %s: %w", path, err)
	}
	return nil
}

// checkHeld checks the files after the driver opened them: the database
// is still f's inode, the -wal and -shm present are safe, and some
// descriptor of this process other than Open's kept ones holds each of
// these inodes. That proves SQLite did not open something swapped in and
// out again only while no other SQLite connection of the process holds
// them; it suffices because the daemon has no in-process route readers
// (design section 2), and the checked open refuses unsafe files anyway.
func checkHeld(path string, f *os.File) error {
	if err := sameFile(f, path); err != nil {
		return err
	}
	held, err := heldInodes()
	if err != nil {
		return err
	}
	for _, n := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Lstat(n)
		if errors.Is(err, os.ErrNotExist) && n != path {
			continue
		}
		if err != nil {
			return fmt.Errorf("store: %s: %v", n, err)
		}
		if err := checkRegular(st, n); err != nil {
			return err
		}
		s := st.Sys().(*syscall.Stat_t)
		if !held[inode{uint64(s.Dev), uint64(s.Ino)}] {
			return fmt.Errorf("store: %s: SQLite does not hold the checked file (replaced while it was opened)", n)
		}
	}
	return nil
}

type inode struct{ dev, ino uint64 }

// heldInodes lists the inodes this process holds open, except through
// Open's kept descriptors. The caller holds openMu.
func heldInodes() (map[inode]bool, error) {
	skip := map[int]bool{}
	for _, k := range kept {
		for _, f := range k.files {
			skip[int(f.Fd())] = true
		}
	}
	dir := "/dev/fd"
	if runtime.GOOS == "linux" {
		dir = "/proc/self/fd"
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("store: list descriptors: %w", err)
	}
	names, err := d.Readdirnames(-1)
	own := int(d.Fd())
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("store: list descriptors: %w", err)
	}
	held := map[inode]bool{}
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || skip[fd] || fd == own {
			continue
		}
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil {
			held[inode{uint64(st.Dev), uint64(st.Ino)}] = true
		}
	}
	_ = d.Close()
	return held, nil
}
