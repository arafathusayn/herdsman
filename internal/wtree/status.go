package wtree

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Status is one worktree's state as git reports it. It is read fresh on every
// collection and never cached: an edit in a nested folder changes neither the
// index nor the worktree folder's own timestamp, so no timestamp can say that
// an earlier answer still holds.
type Status struct {
	Staged      bool
	Modified    bool
	Untracked   bool
	Conflicted  bool
	HasUpstream bool
	Ahead       int // Commits the upstream lacks.
	Behind      int // Commits the upstream has.
}

// statusArgs asks for machine-readable output. An inspection must not take
// the index lock from an agent working in the same tree.
var statusArgs = []string{"--no-optional-locks", "status", "--porcelain=v2", "--branch", "-z"}

// Symbols renders the working-tree flags for a table cell.
func (s Status) Symbols() string {
	var out strings.Builder
	for _, flag := range []struct {
		set    bool
		symbol string
	}{{s.Staged, "+"}, {s.Modified, "!"}, {s.Untracked, "?"}, {s.Conflicted, "✘"}} {
		if flag.set {
			out.WriteString(flag.symbol)
		}
	}
	return out.String()
}

// ParseStatus reads git status --porcelain=v2 --branch -z. An entry it does
// not recognize is an error, so a format change cannot pass as a clean tree.
func ParseStatus(data []byte) (Status, error) {
	var status Status
	if len(data) == 0 {
		return status, nil
	}
	if data[len(data)-1] != 0 {
		return status, fmt.Errorf("wtree: status output is not NUL-terminated: %q", data)
	}
	entries := bytes.Split(data[:len(data)-1], []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := string(entries[i])
		kind, rest, _ := strings.Cut(entry, " ")
		switch kind {
		case "#":
			if err := status.readHeader(rest); err != nil {
				return status, err
			}
		case "1", "2":
			if err := status.readChange(rest, entry); err != nil {
				return status, err
			}
			if kind == "2" {
				i++ // A rename's original path is the next field.
				if i >= len(entries) {
					return status, fmt.Errorf("wtree: rename without its original path: %q", entry)
				}
			}
		case "u":
			status.Conflicted = true
		case "?":
			status.Untracked = true
		case "!":
		default:
			return status, fmt.Errorf("wtree: unknown status entry: %q", entry)
		}
	}
	return status, nil
}

func (s *Status) readChange(rest, entry string) error {
	code, _, _ := strings.Cut(rest, " ")
	if len(code) != 2 {
		return fmt.Errorf("wtree: malformed status entry: %q", entry)
	}
	s.Staged = s.Staged || code[0] != '.'
	s.Modified = s.Modified || code[1] != '.'
	return nil
}

func (s *Status) readHeader(header string) error {
	name, value, _ := strings.Cut(header, " ")
	switch name {
	case "branch.upstream":
		s.HasUpstream = true
	case "branch.ab":
		ahead, behind, ok := strings.Cut(value, " ")
		a, aheadErr := strconv.Atoi(strings.TrimPrefix(ahead, "+"))
		b, behindErr := strconv.Atoi(strings.TrimPrefix(behind, "-"))
		if !ok || aheadErr != nil || behindErr != nil {
			return fmt.Errorf("wtree: malformed status header: %q", header)
		}
		s.Ahead, s.Behind = a, b
	}
	return nil
}
