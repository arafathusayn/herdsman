package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
)

// faultConn fails the at-th Exec (or, with query, the at-th Query) of an
// import, as a full disk or an I/O error would.
type faultConn struct {
	store.Conn
	n     *int
	at    int
	query bool
}

var errFault = errors.New("injected storage failure")

func (f faultConn) hit(query bool) bool {
	if f.query != query {
		return false
	}
	*f.n++
	return *f.n == f.at
}

func (f faultConn) Exec(q string, args ...any) (int64, error) {
	if f.hit(false) {
		return 0, errFault
	}
	return f.Conn.Exec(q, args...)
}

func (f faultConn) Query(q string, args []any, fn func(store.Row) error) error {
	if f.hit(true) {
		return errFault
	}
	return f.Conn.Query(q, args, fn)
}

// fullInbox is an inbox with every kind the importer reads: the core
// with an active claim and an earlier reader's claim, the state files,
// every census kind, a sender notice, resume intents and a delivery hold.
func fullInbox(t *testing.T) *importInbox {
	t.Helper()
	x := newImportInbox(t)
	x.withEveryKind(t)
	x.write(t, "orch-1/claim/token", []byte(x.token+"\n"))
	x.write(t, "orch-1/claim/owner", []byte("p2:4242\n"))
	x.write(t, "ids/m000000000003/claim", []byte(x.token+"\n"))
	x.write(t, "ids/m000000000001/claim", []byte(".claim.9\n"))
	x.write(t, "orch-1/notices/m000000000004.md.json", prettyJSON(obj("id", "m000000000004", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
	x.write(t, "orch-1/notices/.tmp-1", []byte("half"))
	x.write(t, "state/resume-k1.sent", []byte("1700000500 intent\n"))
	x.write(t, "state/resume-k2", []byte("intent\n"))
	x.write(t, "state/route-paused", []byte("by the human\n"))
	x.write(t, "ids/m000000000004/notice-published", nil)
	x.write(t, "state/resume-k1", []byte("intent\n"))
	x.write(t, "state/resume-k1.readback", []byte("screen\n"))
	// A quarantined entry that is no envelope stays legacy evidence, and
	// so does its notice.
	x.write(t, "adj-1/quarantine/m000000000005.md", []byte("not an envelope"))
	x.write(t, "ids/m000000000005/envelope", []byte("not an envelope either"))
	x.write(t, "orch-1/notices/m000000000005.md.json", prettyJSON(obj("id", "m000000000005", "to", "adj-1", "state", "quarantined", "authority", "notice-only")))
	return x
}

// TestImportStorageFailures: every statement of an import can fail, and
// each failure refuses the import (never a partial import that reports
// success); with no failure the same inbox imports.
func TestImportStorageFailures(t *testing.T) {
	x := fullInbox(t)
	dbDir := filepath.Join(x.ph, "daemon")
	_ = os.Mkdir(dbDir, 0o700)
	c, err := store.Open(filepath.Join(dbDir, "fault.db"), store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	errRollback := errors.New("rollback")
	for _, query := range []bool{false, true} {
		for at := 1; ; at++ {
			n := 0
			var ae *APIError
			_ = c.Tx(func(tx store.Conn) error {
				ae = importCore(faultConn{Conn: tx, n: &n, at: at, query: query}, x.dir, 1800000000)
				return errRollback
			})
			if n < at {
				if ae != nil {
					t.Fatalf("query=%v: no failure injected, import refused: %v", query, ae)
				}
				if (!query && at < 10) || at < 2 {
					t.Fatalf("query=%v: only %d statements", query, at-1)
				}
				break
			}
			if ae == nil {
				t.Fatalf("query=%v: statement %d failed and the import reported success", query, at)
			}
		}
	}
}
