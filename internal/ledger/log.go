package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Log is one teardown's record: its plan, then one line per action, as JSON
// lines. A nil Log writes nothing. A write that fails loses a log line and
// never stops the teardown.
type Log struct {
	file *os.File
	now  func() time.Time
}

// OpenLog opens <common git dir>/herdsman/logs/<task>/teardown.log for appending.
func OpenLog(commonDir, task string) (*Log, error) {
	if task == "" || filepath.Base(task) != task || strings.HasPrefix(task, ".") {
		return nil, fmt.Errorf("ledger: task %q cannot name a log folder", task)
	}
	folder := filepath.Join(commonDir, "herdsman", "logs", task)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, fmt.Errorf("ledger: log folder: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(folder, "teardown.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ledger: log: %w", err)
	}
	return &Log{file: file, now: time.Now}, nil
}

// Close releases the file.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Log) write(entry map[string]any) {
	if l == nil {
		return
	}
	entry["time"] = l.now().UTC().Format(time.RFC3339)
	if line, err := json.Marshal(entry); err == nil {
		_, _ = l.file.Write(append(line, '\n'))
	}
}

func (l *Log) plan(plan Plan) {
	l.write(map[string]any{"event": "plan", "plan": plan})
}

// action records one signal or container stop with its evidence and class.
// A zero destination is a container stop, which has no pid.
func (l *Log) action(target Line, destination, signal int) {
	l.write(map[string]any{
		"event": "action", "kind": target.Kind, "id": target.ID, "level": target.Level,
		"class": target.Class, "destination": destination, "signal": signal,
	})
}
