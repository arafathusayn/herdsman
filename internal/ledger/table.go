package ledger

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// EnvExecution names the variable that carries the execution id into a pane.
const EnvExecution = envExec

// crockford is the alphabet of an execution id: a ULID, 48 bits of
// milliseconds and 80 random bits, which sorts by time.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const executionLength = 26

// NewExecution mints an execution id.
func NewExecution(now time.Time) string {
	var random [10]byte
	_, _ = rand.Read(random[:]) // never fails since Go 1.24; it crashes instead
	return encodeExecution(uint64(now.UnixMilli()), random)
}

func encodeExecution(millis uint64, random [10]byte) string {
	id := make([]byte, executionLength)
	for i := 9; i >= 0; i-- {
		id[i] = crockford[millis&31]
		millis >>= 5
	}
	high := binary.BigEndian.Uint16(random[:2])
	low := binary.BigEndian.Uint64(random[2:])
	for i := executionLength - 1; i >= 10; i-- {
		id[i] = crockford[low&31]
		low = low>>5 | uint64(high&31)<<59
		high >>= 5
	}
	return string(id)
}

// ExecutionTime is when an execution id was minted, zero for any other text,
// so that the age rule proves nothing from a value it cannot read.
func ExecutionTime(execution string) time.Time {
	if len(execution) != executionLength {
		return time.Time{}
	}
	var millis uint64
	for _, char := range []byte(execution[:10]) {
		digit := indexCrockford(char)
		if digit < 0 {
			return time.Time{}
		}
		millis = millis<<5 | uint64(digit)
	}
	return time.UnixMilli(int64(millis))
}

func indexCrockford(char byte) int {
	for i := range len(crockford) {
		if crockford[i] == char {
			return i
		}
	}
	return -1
}

// Table reads and writes the records in the repository's resources table.
type Table struct {
	db  *sql.DB
	now func() time.Time
}

// NewTable uses a state database that has the resources table.
func NewTable(db *sql.DB) *Table {
	return &Table{db: db, now: time.Now}
}

// Add registers authority over a resource. Writing the same record twice keeps
// one row, so a hook may run again.
func (t *Table) Add(ctx context.Context, record Record, source string) error {
	switch {
	case record.Task == "":
		return errors.New("ledger: a record needs a task")
	case record.Execution == "":
		return errors.New("ledger: a record needs an execution")
	case record.Kind == "":
		return errors.New("ledger: a record needs a kind")
	case record.ID == "":
		return errors.New("ledger: a record needs an id")
	case record.Kind == KindProcess && (record.Start.IsZero() || record.Command == ""):
		return errors.New("ledger: a process record needs its start time and command")
	}
	start := ""
	if !record.Start.IsZero() {
		start = record.Start.UTC().Format(time.RFC3339)
	}
	adopted := 0
	if record.Adopted {
		adopted = 1
	}
	_, err := t.db.ExecContext(ctx, `INSERT INTO resources (task, execution, kind, id, start, command, adopted, source, registered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task, execution, kind, id, start) DO UPDATE SET command = excluded.command, adopted = excluded.adopted, source = excluded.source`,
		record.Task, record.Execution, record.Kind, record.ID, start, record.Command, adopted, source, t.now().Unix())
	if err != nil {
		return fmt.Errorf("ledger: add record: %w", err)
	}
	return nil
}

// Records lists every task's records, so that a claim by another task on a
// resource is visible to this one's plan.
func (t *Table) Records(ctx context.Context) ([]Record, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT task, execution, kind, id, start, command, adopted FROM resources ORDER BY task, execution, kind, id, start`)
	if err != nil {
		return nil, fmt.Errorf("ledger: records: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var record Record
		var start string
		var adopted int
		if err := rows.Scan(&record.Task, &record.Execution, &record.Kind, &record.ID, &start, &record.Command, &adopted); err != nil {
			return nil, fmt.Errorf("ledger: records: %w", err)
		}
		if start != "" {
			if record.Start, err = time.Parse(time.RFC3339, start); err != nil {
				return nil, fmt.Errorf("ledger: record start %q: %w", start, err)
			}
		}
		record.Adopted = adopted != 0
		records = append(records, record)
	}
	return records, rows.Err()
}

// Executions lists a task's executions, newest first.
func (t *Table) Executions(ctx context.Context, task string) ([]string, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT execution FROM resources WHERE task = ? GROUP BY execution ORDER BY execution DESC`, task)
	if err != nil {
		return nil, fmt.Errorf("ledger: executions: %w", err)
	}
	defer rows.Close()
	var executions []string
	for rows.Next() {
		var execution string
		if err := rows.Scan(&execution); err != nil {
			return nil, fmt.Errorf("ledger: executions: %w", err)
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}
