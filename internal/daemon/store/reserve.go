package store

import (
	"fmt"
	"strconv"
)

// Id and seq reservation and restore detection (design sections 1.4, 3
// and 4, items S27 and S27b). The account database's routes row holds,
// per route, the highest message id and event seq the route may issue
// (raised in blocks of 1000, before an id or seq above it is issued) and
// the route database's store_gen at its last open. A restored or copied
// route file can then never issue an id or seq a client has seen.

// ReserveBlock is how far one account commit raises a reservation.
const ReserveBlock = 1000

// ErrUnknownRoute is a route with no row in the account database.
var ErrUnknownRoute = fmt.Errorf("store: route has no account row")

// Reservation is a routes row's reservations and open generation.
type Reservation struct {
	MessageID, Seq int64
	OpenGen        string
}

// ReadReservation reads route's row of the account database.
func ReadReservation(acct Conn, route string) (Reservation, error) {
	var r Reservation
	found := false
	err := acct.Query("SELECT reserved_message_id, reserved_seq, open_gen FROM routes WHERE route = ?", []any{route}, func(row Row) error {
		r, found = Reservation{row.Int64(0), row.Int64(1), row.Text(2)}, true
		return nil
	})
	if err == nil && !found {
		err = ErrUnknownRoute
	}
	return r, err
}

// Reserve makes the reservations cover message id id and event seq seq,
// raising each that falls short by whole blocks above what it needs, in
// one account commit; a reservation that already covers them costs no
// write. The route's transaction that issues them runs after it.
func Reserve(acct Conn, route string, id, seq int64) (Reservation, error) {
	var r Reservation
	err := acct.Tx(func(tx Conn) error {
		var err error
		if r, err = ReadReservation(tx, route); err != nil {
			return err
		}
		raise := func(have, need int64) int64 {
			if need <= have {
				return have
			}
			return need + ReserveBlock - 1 - (need-1)%ReserveBlock
		}
		nid, nseq := raise(r.MessageID, id), raise(r.Seq, seq)
		if nid == r.MessageID && nseq == r.Seq {
			return nil
		}
		r.MessageID, r.Seq = nid, nseq
		_, err = tx.Exec("UPDATE routes SET reserved_message_id = ?, reserved_seq = ? WHERE route = ?", nid, nseq, route)
		return err
	})
	return r, err
}

// OpenResult is what OpenGeneration found.
type OpenResult struct {
	Restored bool   // store_gen differed from open_gen: a new epoch began
	Epoch    string // the route's epoch after the open
}

// OpenGeneration runs at every open of route's writer rc, before any
// operation: the route's last_message_id and next_seq are raised to the
// account's reservations (so no detection gap can reuse an id or seq);
// when the route's store_gen is not the account's open_gen (a restored
// backup, a copied file, or a crash between the two writes below) the
// route starts newEpoch with no events. Then a new store_gen is written
// to the route, and after it to the account row.
func OpenGeneration(acct, rc Conn, route, newGen, newEpoch string) (OpenResult, error) {
	var out OpenResult
	res, err := ReadReservation(acct, route)
	if err != nil {
		return out, err
	}
	err = rc.Tx(func(tx Conn) error {
		gen, err := metaGet(tx, "store_gen")
		if err != nil {
			return err
		}
		lastID, err := metaInt(tx, "last_message_id")
		if err != nil {
			return err
		}
		next, err := metaInt(tx, "next_seq")
		if err != nil {
			return err
		}
		if res.MessageID > lastID {
			lastID = res.MessageID
		}
		if res.Seq+1 > next {
			next = res.Seq + 1
		}
		for k, v := range map[string]int64{"last_message_id": lastID, "next_seq": next} {
			if err := metaSet(tx, k, strconv.FormatInt(v, 10)); err != nil {
				return err
			}
		}
		if gen != res.OpenGen {
			out.Restored = true
			if _, err := tx.Exec("DELETE FROM events"); err != nil {
				return err
			}
			if err := metaSet(tx, "epoch", newEpoch); err != nil {
				return err
			}
			if err := metaSet(tx, "oldest_seq", strconv.FormatInt(next, 10)); err != nil {
				return err
			}
		}
		if out.Epoch, err = metaGet(tx, "epoch"); err != nil {
			return err
		}
		return metaSet(tx, "store_gen", newGen)
	})
	if err != nil {
		return out, err
	}
	_, err = acct.Exec("UPDATE routes SET open_gen = ? WHERE route = ?", newGen, route)
	return out, err
}

// PublishRoute inserts an adopted route's account row from its staging
// file sc: reservations at or above the staged last_message_id and last
// seq, and open_gen equal to the staged store_gen, so the first open
// keeps the imported ids and epoch (section 5 step 5, N3-P1-1).
func PublishRoute(acct, sc Conn, route, inbox, repo, socket, dbPath string, now int64) error {
	gen, err := metaGet(sc, "store_gen")
	if err != nil {
		return err
	}
	lastID, err := metaInt(sc, "last_message_id")
	if err != nil {
		return err
	}
	next, err := metaInt(sc, "next_seq")
	if err != nil {
		return err
	}
	_, err = acct.Exec("INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) "+
		"VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)", route, inbox, repo, socket, dbPath, now, gen, lastID, next-1)
	return err
}
