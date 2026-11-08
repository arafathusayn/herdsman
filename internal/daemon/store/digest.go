package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
)

// Record is one thing a digest covers: its stable id and the fields that
// must survive a migration. Timestamps of last change and progress states
// are left out, because a healthy daemon changes them.
type Record struct {
	Kind   string
	ID     string
	Fields []string
}

// The record kinds.
const (
	RecordRoute     = "route"
	RecordMessage   = "message"
	RecordOperation = "operation"
)

// AccountRecords reads the routes and operations of an account database.
func AccountRecords(c Conn) ([]Record, error) {
	var recs []Record
	err := c.Query("SELECT route, inbox, repo, herdr_socket, created FROM routes ORDER BY route", nil, func(r Row) error {
		recs = append(recs, RouteRecord(r.Text(0), r.Text(1), r.Text(2), r.Text(3), r.Int64(4)))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: digest routes: %w", err)
	}
	var hasOps bool
	if err := c.Query("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'operations'", nil, func(Row) error { hasOps = true; return nil }); err != nil {
		return nil, fmt.Errorf("store: digest: %w", err)
	}
	if !hasOps {
		return recs, nil // an account file from before the journal existed
	}
	steps := map[string][]string{}
	err = c.Query("SELECT op_id, ordinal, name, effect, key, spec, retry, events FROM operation_steps ORDER BY op_id, ordinal", nil, func(r Row) error {
		id := r.Text(0)
		steps[id] = append(steps[id], strconv.FormatInt(r.Int64(1), 10), r.Text(2), r.Text(3), r.Text(4), r.Text(5), r.Text(6), r.Text(7))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: digest steps: %w", err)
	}
	err = c.Query("SELECT id, COALESCE(key,''), kind, plan, spec_sha256, preconditions FROM operations ORDER BY id", nil, func(r Row) error {
		id := r.Text(0)
		recs = append(recs, Record{Kind: RecordOperation, ID: id, Fields: append([]string{r.Text(1), r.Text(2), r.Text(3), r.Text(4), r.Text(5)}, steps[id]...)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: digest operations: %w", err)
	}
	return recs, nil
}

// RouteRecord is the record of one route, wherever it is registered.
func RouteRecord(name, inbox, repo, herdrSocket string, created int64) Record {
	return Record{Kind: RecordRoute, ID: name, Fields: []string{inbox, repo, herdrSocket, strconv.FormatInt(created, 10)}}
}

// RouteRecords reads the messages of a route database.
func RouteRecords(route string, c Conn) ([]Record, error) {
	var recs []Record
	err := c.Query("SELECT id, sender, recipient, kind, COALESCE(reply_to, 0), envelope, body_sha256, origin FROM messages ORDER BY id", nil, func(r Row) error {
		recs = append(recs, Record{Kind: RecordMessage, ID: fmt.Sprintf("%s/m%012d", route, r.Int64(0)),
			Fields: []string{r.Text(1), r.Text(2), r.Text(3), strconv.FormatInt(r.Int64(4), 10), r.Text(5), r.Text(6), r.Text(7)}})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: digest messages of %s: %w", route, err)
	}
	return recs, nil
}

// Canonical sorts records by kind and id and keeps the first of each; a
// caller lists its most authoritative source first.
func Canonical(recs []Record) []Record {
	out := append([]Record(nil), recs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	kept := out[:0]
	for i, r := range out {
		if i == 0 || r.Kind != out[i-1].Kind || r.ID != out[i-1].ID {
			kept = append(kept, r)
		}
	}
	return kept
}

// Digest is a hash of the canonical records. Every value is length-prefixed,
// so moving a byte between two fields changes the result.
func Digest(recs []Record) string {
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s", len(s), s) }
	for _, r := range Canonical(recs) {
		write(r.Kind)
		write(r.ID)
		fmt.Fprintf(h, "#%d", len(r.Fields))
		for _, f := range r.Fields {
			write(f)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Count is how many canonical records of kind there are.
func Count(recs []Record, kind string) int {
	n := 0
	for _, r := range Canonical(recs) {
		if r.Kind == kind {
			n++
		}
	}
	return n
}
