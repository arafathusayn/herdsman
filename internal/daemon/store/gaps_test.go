package store

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func setGaps(t *testing.T, c Conn, v string) {
	t.Helper()
	if err := metaSet(c, "seq_gaps", v); err != nil {
		t.Fatal(err)
	}
}

// TestAddGap: a new range merges with the ranges it touches or overlaps,
// keeps the ones apart from it, and drops those below the kept window.
func TestAddGap(t *testing.T) {
	for _, tc := range []struct {
		name, gaps string
		oldest     string
		add        seqRange
		want       string
	}{
		{"first", "", "1", seqRange{3, 5}, "3-5"},
		{"apart", "3-5", "1", seqRange{8, 9}, "3-5,8-9"},
		{"touching", "3-5", "1", seqRange{6, 9}, "3-9"},
		{"overlapping", "3-5,7-8", "1", seqRange{8, 12}, "3-5,7-12"},
		{"covering two", "3-5,7-8", "1", seqRange{4, 9}, "3-9"},
		{"below the kept window", "3-5,10-12", "8", seqRange{20, 21}, "10-12,20-21"},
		{"just below the window stays", "3-7", "8", seqRange{20, 21}, "3-7,20-21"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := eventsRoute(t)
			if tc.gaps != "" {
				setGaps(t, c, tc.gaps)
			}
			if err := metaSet(c, "oldest_seq", tc.oldest); err != nil {
				t.Fatal(err)
			}
			if err := c.Tx(func(tx Conn) error { return addGap(tx, tc.add) }); err != nil {
				t.Fatal(err)
			}
			if got, _ := metaGet(c, "seq_gaps"); got != tc.want {
				t.Errorf("seq_gaps %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAddGapCorruptOldest: addGap refuses a corrupt meta.oldest_seq.
func TestAddGapCorruptOldest(t *testing.T) {
	c := eventsRoute(t)
	if err := metaSet(c, "oldest_seq", "x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Tx(func(tx Conn) error { return addGap(tx, seqRange{3, 4}) }); !errors.Is(err, ErrSchemaCorrupt) {
		t.Fatalf("addGap with oldest_seq x: %v, want ErrSchemaCorrupt", err)
	}
}

// TestCorruptSeqGapsRefused: a malformed meta.seq_gaps is refused as a
// corrupt schema by ReadEvents (a cursor at or below the head reads the
// gaps) and by OpenGeneration (a same-generation raise records one).
func TestCorruptSeqGapsRefused(t *testing.T) {
	for _, v := range []string{"x", "3", "3-x", "x-3", "5-3", "3-4,", "3-4,y-9"} {
		t.Run("ReadEvents "+v, func(t *testing.T) {
			c := eventsRoute(t)
			appendN(t, c, 2)
			setGaps(t, c, v)
			if _, err := ReadEvents(c, epochA, 1, 2, ReplayPage); !errors.Is(err, ErrSchemaCorrupt) {
				t.Fatalf("seq_gaps %q: %v, want ErrSchemaCorrupt", v, err)
			}
		})
		t.Run("OpenGeneration "+v, func(t *testing.T) {
			acct := accountWith(t, "g1")
			if _, err := acct.Exec("UPDATE routes SET reserved_seq = 10 WHERE route = 'r'"); err != nil {
				t.Fatal(err)
			}
			rc := routeFile(t, filepath.Join(dir(t), "r.db"), "g1")
			t.Cleanup(func() { _ = rc.Close() })
			setGaps(t, rc, v)
			if _, err := OpenGeneration(acct, rc, "r", "g2", epochB); !errors.Is(err, ErrSchemaCorrupt) {
				t.Fatalf("seq_gaps %q: %v, want ErrSchemaCorrupt", v, err)
			}
		})
	}
}

// TestOpenRegularRefusals: openRegular refuses a missing file without
// create, and a FIFO without blocking on it.
func TestOpenRegularRefusals(t *testing.T) {
	d := dir(t)
	if _, err := openRegular(filepath.Join(d, "missing.db"), false); err == nil {
		t.Error("openRegular of a missing file without create succeeded")
	}
	fifo := filepath.Join(d, "f.db")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openRegular(fifo, false); err == nil {
		t.Error("openRegular of a FIFO succeeded")
	}
}
