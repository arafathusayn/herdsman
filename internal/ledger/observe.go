package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Observation is what live detection last saw of one record. It grants nothing:
// authority stays with the record itself.
type Observation struct {
	Execution string
	Kind      string
	ID        string
	Evidence  Level
	Detail    string
	SeenAt    time.Time
}

// observedDetail is the part of a plan line worth keeping with its record.
func observedDetail(line Line) string {
	detail, _ := json.Marshal(struct {
		Class string `json:"class,omitempty"`
		State string `json:"state,omitempty"`
		Role  string `json:"role,omitempty"`
		Name  string `json:"name,omitempty"`
	}{line.Class, line.State, line.Role, line.Name})
	return string(detail)
}

// sameResource reports a record that names what a plan line found, by the
// identity a reused pid or name would not share.
func sameResource(record Record, line Line) bool {
	if record.Kind != line.Kind || record.ID != line.ID {
		return false
	}
	if record.Start.IsZero() {
		return record.Kind != KindProcess && record.Kind != KindVolume && record.Kind != KindNetwork
	}
	return Identity{Kind: record.Kind, ID: record.ID, Start: record.Start}.String() == line.Identity
}

// Observe stores what a plan found for its task's records, and only that: a
// record with no live line keeps what was last seen of it.
func (t *Table) Observe(ctx context.Context, plan Plan) error {
	records, err := t.Records(ctx)
	if err != nil {
		return err
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: observe: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	seen := t.now().Unix()
	for _, record := range records {
		if record.Task != plan.Task {
			continue
		}
		for _, line := range plan.Lines {
			if !sameResource(record, line) {
				continue
			}
			start := ""
			if !record.Start.IsZero() {
				start = record.Start.UTC().Format(time.RFC3339)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE resources SET evidence = ?, detail = ?, seen_at = ?
				WHERE task = ? AND execution = ? AND kind = ? AND id = ? AND start = ?`,
				line.Level, observedDetail(line), seen, record.Task, record.Execution, record.Kind, record.ID, start); err != nil {
				return fmt.Errorf("ledger: observe: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: observe: %w", err)
	}
	return nil
}

// Observations lists what was last seen of a task's records; one never seen
// is left out.
func (t *Table) Observations(ctx context.Context, task string) ([]Observation, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT execution, kind, id, evidence, detail, seen_at
		FROM resources WHERE task = ? AND seen_at > 0 ORDER BY execution, kind, id`, task)
	if err != nil {
		return nil, fmt.Errorf("ledger: observations: %w", err)
	}
	defer rows.Close()
	var observations []Observation
	for rows.Next() {
		var observation Observation
		var seen int64
		if err := rows.Scan(&observation.Execution, &observation.Kind, &observation.ID, &observation.Evidence, &observation.Detail, &seen); err != nil {
			return nil, fmt.Errorf("ledger: observations: %w", err)
		}
		observation.SeenAt = time.Unix(seen, 0)
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}
