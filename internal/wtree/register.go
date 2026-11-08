package wtree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// The kinds of resource a task can own.
const (
	KindProcess   = "process"
	KindPort      = "port"
	KindContainer = "container"
	KindCompose   = "compose"
	KindVolume    = "volume"
	KindNetwork   = "network"
	KindScratch   = "scratch"
	KindDatabase  = "database"
)

var resourceKinds = []string{KindProcess, KindPort, KindContainer, KindCompose, KindVolume, KindNetwork, KindScratch, KindDatabase}

// Resource records that an execution of a task owns something. Start is kept
// as given: a process's start time, or a volume's or network's creation time.
type Resource struct {
	Task         string
	Execution    string
	Kind         string
	ID           string
	Start        string
	Command      string
	Adopted      bool
	Source       string
	RegisteredAt time.Time
}

// validate refuses a record that could later match the wrong thing: process
// ids, volume names and network names all get reused, so those kinds need the
// time that tells one holder from the next.
func (r Resource) validate() error {
	if r.Task == "" || r.Execution == "" || r.ID == "" {
		return errors.New("wtree: a resource needs a task, an execution and an id")
	}
	if !slices.Contains(resourceKinds, r.Kind) {
		return fmt.Errorf("wtree: unknown resource kind %q", r.Kind)
	}
	switch r.Kind {
	case KindProcess:
		if pid, err := strconv.Atoi(r.ID); err != nil || pid <= 0 {
			return fmt.Errorf("wtree: %q is not a process id", r.ID)
		}
	case KindPort:
		if port, err := strconv.Atoi(r.ID); err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("wtree: %q is not a port", r.ID)
		}
	case KindScratch:
		if !filepath.IsAbs(r.ID) {
			return fmt.Errorf("wtree: scratch path %q is not absolute", r.ID)
		}
	}
	if r.Start == "" && slices.Contains([]string{KindProcess, KindVolume, KindNetwork}, r.Kind) {
		return fmt.Errorf("wtree: a %s needs its start or creation time", r.Kind)
	}
	return nil
}

// Register records a resource. Registering the same one again changes nothing.
func (s *State) Register(ctx context.Context, r Resource) error {
	if err := r.validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO resources
		(task, execution, kind, id, start, command, adopted, source, registered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task, execution, kind, id, start) DO NOTHING`,
		r.Task, r.Execution, r.Kind, r.ID, r.Start, r.Command, r.Adopted, r.Source, time.Now().Unix())
	return err
}

// Resources lists what is registered for a task.
func (s *State) Resources(ctx context.Context, task string) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task, execution, kind, id, start, command, adopted, source, registered_at
		FROM resources WHERE task = ? ORDER BY kind, id, start`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []Resource
	for rows.Next() {
		var r Resource
		var registered int64
		if err := rows.Scan(&r.Task, &r.Execution, &r.Kind, &r.ID, &r.Start, &r.Command, &r.Adopted, &r.Source, &registered); err != nil {
			return nil, err
		}
		r.RegisteredAt = time.Unix(registered, 0)
		found = append(found, r)
	}
	return found, rows.Err()
}
