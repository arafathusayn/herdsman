package ledger

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxInput bounds every parser's input: command output is hostile input.
const maxInput = 64 << 20

const envExec = "HERDSMAN_EXEC"

// CwdEntry is one process's current folder.
type CwdEntry struct {
	Pid  int
	Path string
}

// Process is one row of ps. Env holds only the HERDSMAN_* variables, so that
// no other environment value, which may be a secret, reaches a plan.
type Process struct {
	Pid     int
	Parent  int
	Group   int
	Start   time.Time
	Command string
	Env     []string
	// Cut marks a Command that may differ from what ran: a value was hidden, or
	// a plain word followed the environment boundary, from a value with a space
	// or a KEY=value argument. Proof never reads Command; see commandProven.
	Cut bool
}

func (p Process) execution() (string, bool) {
	// The last token wins: ps -E prints arguments first, so an argument can
	// spell the variable and only the real environment comes after it.
	for _, entry := range slices.Backward(p.Env) {
		if value, ok := strings.CutPrefix(entry, envExec+"="); ok {
			return value, true
		}
	}
	return "", false
}

func checkSize(raw []byte) error {
	if len(raw) > maxInput {
		return fmt.Errorf("ledger: input of %d bytes is over the %d byte limit", len(raw), maxInput)
	}
	return nil
}

// outputLines returns the non-blank lines of command output.
func outputLines(raw []byte) []string {
	var lines []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if text := strings.TrimRight(string(line), "\r"); strings.TrimSpace(text) != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

// walkLsof reads `lsof -F` output: a p line opens a process, an f line a file
// and an n line names it. Other field letters are tolerated.
func walkLsof(raw []byte, visit func(pid int, fd, name string) error) error {
	if err := checkSize(raw); err != nil {
		return err
	}
	pid, fd := 0, ""
	for _, line := range outputLines(raw) {
		field, value := line[0], line[1:]
		switch field {
		case 'p':
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("ledger: lsof pid %q: %w", value, err)
			}
			pid, fd = parsed, ""
		case 'f':
			fd = value
		case 'n':
			if pid == 0 {
				return fmt.Errorf("ledger: lsof name %q before any pid", value)
			}
			if err := visit(pid, fd, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ParseLsofCwd reads `lsof -d cwd -F pn`. A row with another descriptor is
// skipped, and lsof may leave the descriptor field out.
func ParseLsofCwd(raw []byte) ([]CwdEntry, error) {
	var entries []CwdEntry
	err := walkLsof(raw, func(pid int, fd, name string) error {
		if fd == "" || fd == "cwd" {
			entries = append(entries, CwdEntry{Pid: pid, Path: name})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// nextField splits off one blank-separated token and keeps the rest verbatim.
func nextField(text string) (string, string) {
	text = strings.TrimLeft(text, " \t")
	if end := strings.IndexAny(text, " \t"); end >= 0 {
		return text[:end], text[end:]
	}
	return text, ""
}

// ParsePS reads `ps -E -ax -ww -o pid=,ppid=,pgid=,lstart=,command=`. lstart
// has no zone, so the caller names the machine's location.
func ParsePS(raw []byte, location *time.Location) ([]Process, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	var processes []Process
	for _, line := range outputLines(raw) {
		process, err := parsePSLine(line, location)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, nil
}

func parsePSLine(line string, location *time.Location) (Process, error) {
	const numbers, stamp = 3, 5
	var fields [numbers + stamp]string
	rest := line
	for i := range fields {
		fields[i], rest = nextField(rest)
		if fields[i] == "" {
			return Process{}, fmt.Errorf("ledger: short ps line %q", line)
		}
	}
	var ids [numbers]int
	for i := range ids {
		parsed, err := strconv.Atoi(fields[i])
		if err != nil {
			return Process{}, fmt.Errorf("ledger: ps line %q: %w", line, err)
		}
		ids[i] = parsed
	}
	start, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[numbers:], " "), location)
	if err != nil {
		return Process{}, fmt.Errorf("ledger: ps start in %q: %w", line, err)
	}
	command, env, cut := splitCommandEnv(strings.TrimSpace(rest))
	return Process{Pid: ids[0], Parent: ids[1], Group: ids[2], Start: start, Command: command, Env: env, Cut: cut}, nil
}

// splitCommandEnv separates what `ps -E` joins: the environment starts at the
// first token after argv[0] that holds = and is not a flag. ps cannot mark the
// boundary, so an argument of that shape ends the command early. That loses
// part of a restart line and never keeps a secret, and a plain word after the
// boundary sets cut, as does an environment value with a space. A cut with
// only KEY=value tokens
// after it cannot be told from the environment.
func splitCommandEnv(column string) (string, []string, bool) {
	tokens := strings.Split(column, " ")
	boundary := len(tokens)
	for i := 1; i < len(tokens); i++ {
		if startsEnv(tokens[i]) {
			boundary = i
			break
		}
	}
	var env []string
	cut := false
	for _, token := range tokens[boundary:] {
		cut = cut || !startsEnv(token)
		if strings.HasPrefix(token, "HERDSMAN_") && isEnvToken(token) {
			env = append(env, token)
		}
	}
	command, redacted := redactArgs(tokens[:boundary])
	return strings.Join(command, " "), env, cut || redacted
}

func startsEnv(token string) bool {
	return strings.Contains(token, "=") && !strings.HasPrefix(token, "-")
}

// redactArgs hides the value of a flag whose name says it holds a secret, in
// both the --name=value and the --name value forms.
func redactArgs(tokens []string) ([]string, bool) {
	const hidden = "<redacted>"
	out := slices.Clone(tokens)
	redacted := false
	for i := 1; i < len(out); i++ {
		name, _, inline := strings.Cut(out[i], "=")
		if !strings.HasPrefix(name, "-") || !secretName(name) {
			continue
		}
		redacted = true
		if inline {
			out[i] = name + "=" + hidden
		} else if i+1 < len(out) {
			i++
			out[i] = hidden
		}
	}
	return out, redacted
}

func secretName(flag string) bool {
	lower := strings.ToLower(flag)
	for _, word := range []string{"token", "secret", "password", "passwd", "api-key", "apikey", "auth", "credential"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func isEnvToken(token string) bool {
	key, _, found := strings.Cut(token, "=")
	if !found || key == "" || key[0] >= '0' && key[0] <= '9' {
		return false
	}
	for _, char := range key {
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

// underPath reports path at or below root. Callers pass resolved paths.
func underPath(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// processes is detector 1. A process is a candidate by folder, by environment
// or by record; an inherited variable alone grants nothing.
func (p *planner) processes() map[int]*candidate {
	cwds := make(map[int]string, len(p.s.Cwds))
	for _, entry := range p.s.Cwds {
		cwds[entry.Pid] = entry.Path
	}
	found := make(map[int]*candidate)
	for i := range p.s.Processes {
		process := &p.s.Processes[i]
		if c := p.process(process, cwds[process.Pid]); c != nil {
			found[process.Pid] = c
		}
	}
	for _, entry := range p.s.Cwds {
		_, listed := p.byPid[entry.Pid]
		if !listed && underPath(p.s.Worktree, entry.Path) {
			c := newCandidate(KindProcess, strconv.Itoa(entry.Pid), ProcessIdentity(entry.Pid, time.Time{}))
			c.ev.add(SignalCwd)
			c.ev.Unverified = true
			found[entry.Pid] = c
		}
	}
	return found
}

func (p *planner) process(process *Process, cwd string) *candidate {
	id := ProcessIdentity(process.Pid, process.Start)
	c := newCandidate(KindProcess, id.ID, id)
	if underPath(p.s.Worktree, cwd) {
		c.ev.add(SignalCwd)
	}
	execution, hasExec := process.execution()
	own := hasExec && execution == p.s.Execution
	if own {
		c.ev.add(SignalExecEnv)
	}
	adopted := p.applyRecords(&c.ev, id, true)
	c.line.Role = p.role(id)
	if !c.ev.hit() && c.line.Role == "" {
		return nil
	}
	if hasExec && !own {
		c.ev.share("execution:" + execution)
	}
	for _, other := range p.s.Others {
		if underPath(other.Worktree, cwd) || hasExec && execution == other.Execution {
			c.ev.share("task:" + other.Task)
		}
	}
	c.proven = p.commandProven(process, id)
	if !c.proven && c.line.Role == "" {
		c.ev.share(SharedUnproven)
	}
	p.older(&c.ev, process.Start, adopted)
	c.line.Parent, c.line.Group = process.Parent, process.Group
	c.line.Restart = &Restart{Command: process.Command, Folder: cwd, Env: process.Env, Cut: process.Cut}
	return c
}

// commandProven is the one test for a process being the task's own: a record
// of this execution names this identity and exactly the command it runs. A
// list of what looks human can never be complete, so everything unproven is
// shared. The comparison uses the argv read with no environment, because the
// Command column of ps -E cannot show where argv ends, so a prefix could match.
func (p *planner) commandProven(process *Process, id Identity) bool {
	argv := p.s.Commands[process.Pid]
	if argv == "" {
		return false
	}
	return slices.ContainsFunc(p.s.Records, func(record Record) bool {
		return p.current(record) && record.matches(id) && record.Command == argv
	})
}

func (p *planner) role(id Identity) string {
	for _, excluded := range p.s.Excluded {
		if excluded.Identity.ID == id.ID && excluded.Identity.Start.Equal(id.Start) {
			return excluded.Role
		}
	}
	return ""
}

// ParseCommands reads `ps -ax -ww -o pid=,command=`: each pid's whole argv with
// no environment joined to it. It is the only source of a command proof.
func ParseCommands(raw []byte) (map[int]string, error) {
	if err := checkSize(raw); err != nil {
		return nil, err
	}
	commands := make(map[int]string)
	for _, line := range outputLines(raw) {
		field, rest := nextField(line)
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("ledger: ps command line %q: %w", line, err)
		}
		commands[pid] = strings.TrimSpace(rest)
	}
	return commands, nil
}
