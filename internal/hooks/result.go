package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CommandResult is how one command ended. Exit is -1 when it was killed.
type CommandResult struct {
	Name       string  `json:"name"`
	Command    string  `json:"command"`
	Exit       int     `json:"exit"`
	TimedOut   bool    `json:"timed_out,omitempty"`
	DurationMS float64 `json:"duration_ms"`
	Log        string  `json:"log"`
}

// Result is one run of a hook, kept beside its logs.
type Result struct {
	Hook     string          `json:"hook"`
	Task     string          `json:"task"`
	OK       bool            `json:"ok"`
	Commands []CommandResult `json:"commands"`
	File     string          `json:"file"`
}

// FailedError names the command that stopped a hook.
type FailedError struct {
	Hook string
	Name string
	Exit int
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("hook %s: command %q failed with exit %d", e.Hook, e.Name, e.Exit)
}

func writeResult(result *Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(result.File, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("hooks: write result: %w", err)
	}
	return nil
}

// safeName makes one path component out of a name a configuration chose.
func safeName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, name)
	if cleaned == "" || cleaned[0] == '.' {
		return "_" + cleaned
	}
	return cleaned
}

func commandLog(runDir string, step int, name string) string {
	return filepath.Join(runDir, fmt.Sprintf("%02d-%s.log", step, safeName(name)))
}
