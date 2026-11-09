package trace

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// HarnessSession names where a harness keeps its own log of a session.
type HarnessSession struct {
	Agent string // claude or codex
	Kind  string // id, or path when Value is the log file
	Value string
	Home  string
}

type usageLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	CostUSD   *float64  `json:"costUSD"`
	Message   struct {
		ID    string `json:"id"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Payload struct {
		Type string `json:"type"`
		Info *struct {
			Last *struct {
				Total int64 `json:"total_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// HarnessUsage adds up what a harness logged between start and end. A measure
// the log does not hold stays nil: neither harness writes a cost into every
// session log, and none is estimated here.
func HarnessUsage(session HarnessSession, start, end time.Time) (tokens, costMicros *int64) {
	path := session.logPath()
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	// A streamed reply repeats its id with growing counts; the last one counts.
	byMessage := map[string]int64{}
	reader := bufio.NewReader(file)
	for {
		raw, err := reader.ReadBytes('\n')
		var line usageLine
		if json.Unmarshal(raw, &line) == nil && !line.Timestamp.Before(start) && !line.Timestamp.After(end) {
			switch {
			case line.Message.Usage != nil && line.Message.ID != "":
				u := line.Message.Usage
				byMessage[line.Message.ID] = u.Input + u.Output + u.CacheCreation + u.CacheRead
			case line.Payload.Type == "token_count" && line.Payload.Info != nil && line.Payload.Info.Last != nil:
				total := line.Payload.Info.Last.Total
				addMeasure(&tokens, &total)
			}
			if line.CostUSD != nil && *line.CostUSD >= 0 {
				micros := int64(*line.CostUSD * 1e6)
				addMeasure(&costMicros, &micros)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, nil
			}
			break
		}
	}
	for _, total := range byMessage {
		addMeasure(&tokens, &total)
	}
	return tokens, costMicros
}

// logPath finds the session's log file, or "" when there is none. The session
// id comes from herdr's report, so it is kept to one path component.
func (s HarnessSession) logPath() string {
	if s.Kind == "path" {
		return s.Value
	}
	if s.Value != filepath.Base(s.Value) || s.Home == "" {
		return ""
	}
	var pattern string
	switch s.Agent {
	case "claude":
		pattern = filepath.Join(s.Home, ".claude", "projects", "*", s.Value+".jsonl")
	case "codex":
		pattern = filepath.Join(s.Home, ".codex", "sessions", "*", "*", "*", "rollout-*"+s.Value+".jsonl")
	default:
		return ""
	}
	found, err := filepath.Glob(pattern)
	if err != nil || len(found) == 0 {
		return ""
	}
	return found[0]
}
