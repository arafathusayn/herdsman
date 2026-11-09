package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Log is one teardown's record: its plan, one line per attempted action or
// refusal, then how the run ended, as JSON lines. A nil Log writes nothing. A write that fails loses a log line and
// never stops the teardown.
type Log struct {
	file *os.File
	now  func() time.Time
}

// LogFolder makes and names the folder that holds one task's teardown files.
func LogFolder(commonDir, task string) (string, error) {
	if task == "" || filepath.Base(task) != task || strings.HasPrefix(task, ".") {
		return "", fmt.Errorf("ledger: task %q cannot name a log folder", task)
	}
	folder := filepath.Join(commonDir, "herdsman", "logs", task)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", fmt.Errorf("ledger: log folder: %w", err)
	}
	return folder, nil
}

// Unfinished reports a log whose newest run wrote its plan and never its end,
// so that it was killed part of the way through. A missing log has no run.
func Unfinished(commonDir, task string) (bool, error) {
	folder, err := LogFolder(commonDir, task)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(folder, "teardown.log"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ledger: log: %w", err)
	}
	unfinished := false
	for _, line := range strings.Split(string(data), "\n") {
		var entry struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		switch entry.Event {
		case "plan":
			unfinished = true
		case "finish":
			unfinished = false
		}
	}
	return unfinished, nil
}

// OpenLog opens <common git dir>/herdsman/logs/<task>/teardown.log for appending.
func OpenLog(commonDir, task string) (*Log, error) {
	folder, err := LogFolder(commonDir, task)
	if err != nil {
		return nil, err
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

func targetEntry(event string, target Line) map[string]any {
	return map[string]any{
		"event": event, "kind": target.Kind, "id": target.ID, "identity": target.Identity,
		"level": target.Level, "class": target.Class,
	}
}

// action records one attempted signal or container stop and how it ended. A
// zero destination is a container stop, which has no pid.
func (l *Log) action(target Line, destination, signal int, failure error) {
	entry := targetEntry("action", target)
	entry["destination"], entry["signal"] = destination, signal
	entry["exit_status"] = exitStatus(failure)
	switch {
	case failure == nil:
		entry["result"] = "delivered"
	case alreadyGone(failure):
		entry["result"] = "gone"
	default:
		entry["result"], entry["error"] = "failed", failure.Error()
	}
	l.write(entry)
}

// refused records a target left alone because proof of it failed.
func (l *Log) refused(target Line, failure error) {
	entry := targetEntry("refused", target)
	entry["error"] = failure.Error()
	l.write(entry)
}

// finish closes one run. A plan entry with no finish after it is a run that
// did not get this far.
func (l *Log) finish(outcomes []Outcome, failure error) {
	entry := map[string]any{"event": "finish", "outcomes": outcomes, "interrupted": errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)}
	if failure != nil {
		entry["error"] = failure.Error()
	}
	l.write(entry)
}
