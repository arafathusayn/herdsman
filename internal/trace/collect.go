package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CommandsFile is the command log's name inside a route's logs folder.
const CommandsFile = "commands.jsonl"

const (
	maxLine     = 1 << 20
	logsDir     = "logs"
	hooksDir    = "hooks"
	hookResult  = "result.json"
	hookStamp   = "20060102T150405.000000000"
	redactedArg = "[redacted]"
)

// Sources says where one route's records are.
type Sources struct {
	Route string // the route folder
	// HookLogs are further folders of hook results. Results do not name a
	// route, so every run in such a folder joins this trace.
	HookLogs []string
	// UsageFile is the measured usage log; "" leaves usage out.
	UsageFile string
}

type measured struct {
	tokens     int64
	costMicros int64
}

// Collect reads every source. A line that cannot be read is counted in
// Skipped; a file that cannot be read fails the call.
func Collect(sources Sources) (Trace, error) {
	route, err := filepath.Abs(sources.Route)
	if err != nil {
		return Trace{}, fmt.Errorf("trace: route: %w", err)
	}
	info, err := os.Stat(route)
	if err != nil {
		return Trace{}, fmt.Errorf("trace: route: %w", err)
	}
	if !info.IsDir() {
		return Trace{}, fmt.Errorf("trace: route %s is not a folder", route)
	}
	name := filepath.Base(route)
	collected := &collection{}
	readers := []func() error{
		func() error { return collected.commands(filepath.Join(route, logsDir, CommandsFile)) },
		func() error { return collected.recorded(filepath.Join(route, traceDir, spansFile)) },
		func() error { return collected.hooks(filepath.Join(route, logsDir, hooksDir), true) },
	}
	for _, folder := range sources.HookLogs {
		readers = append(readers, func() error { return collected.hooks(folder, false) })
	}
	for _, read := range readers {
		if err := read(); err != nil {
			return Trace{}, err
		}
	}
	usage, err := collected.usage(sources.UsageFile, name)
	if err != nil {
		return Trace{}, err
	}
	return finish(name, collected.spans, collected.skipped, usage), nil
}

type collection struct {
	spans   []Span
	skipped int
}

// eachLine calls accept for every non-empty line; a false return counts the
// line as skipped. A missing file has no lines.
func eachLine(path string, accept func([]byte) bool) (int, error) {
	handle, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("trace: %w", err)
	}
	defer handle.Close()
	skipped := 0
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(nil, maxLine)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 && !accept(line) {
			skipped++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("trace: read %s: %w", path, err)
	}
	return skipped, nil
}

// commandEntry is the shape of one line of the command log.
type commandEntry struct {
	Time       time.Time `json:"time"`
	Args       []string  `json:"args"`
	Exit       int       `json:"exit"`
	DurationMS float64   `json:"duration_ms"`
}

func (c *collection) commands(path string) error {
	skipped, err := eachLine(path, func(line []byte) bool {
		var entry commandEntry
		if json.Unmarshal(line, &entry) != nil {
			return false
		}
		exit := entry.Exit
		span := Span{Kind: KindCommand, Name: gitName(entry.Args), Start: entry.Time, DurationMS: entry.DurationMS, Exit: &exit, Args: redactArgs(entry.Args)}
		return c.add(span)
	})
	c.skipped += skipped
	return err
}

func (c *collection) recorded(path string) error {
	skipped, err := eachLine(path, func(line []byte) bool {
		var span Span
		return json.Unmarshal(line, &span) == nil && c.add(span)
	})
	c.skipped += skipped
	return err
}

func (c *collection) add(span Span) bool {
	if !span.valid() {
		return false
	}
	c.spans = append(c.spans, span)
	return true
}

// hookRun is the shape of a hook's result file. The command text is left out
// on purpose: a hook's command can hold a secret, and spans leave the machine.
type hookRun struct {
	Hook     string `json:"hook"`
	Task     string `json:"task"`
	Commands []struct {
		Name       string  `json:"name"`
		Exit       int     `json:"exit"`
		DurationMS float64 `json:"duration_ms"`
	} `json:"commands"`
}

// hooks reads every result under root. The folder holding a result is named
// for the time its run began, the only time the result has, so each command
// starts there and is marked approximate.
func (c *collection) hooks(root string, optional bool) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() != hookResult || !entry.Type().IsRegular() {
			return nil
		}
		if !c.hookResult(path) {
			c.skipped++
		}
		return nil
	})
	if optional && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("trace: hook logs: %w", err)
	}
	return nil
}

func (c *collection) hookResult(path string) bool {
	start, err := time.Parse(hookStamp, filepath.Base(filepath.Dir(path)))
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var run hookRun
	if json.Unmarshal(data, &run) != nil {
		return false
	}
	for _, command := range run.Commands {
		exit := command.Exit
		span := Span{Kind: KindHook, Name: run.Hook + "/" + command.Name, Task: run.Task, Start: start, DurationMS: command.DurationMS, Exit: &exit, StartApproximate: true}
		if !c.add(span) {
			return false
		}
	}
	return true
}

// usage sums the route's lines of the usage log, or returns nil without any.
func (c *collection) usage(path, route string) (*measured, error) {
	if path == "" {
		return nil, nil
	}
	var total measured
	found := false
	skipped, err := eachLine(path, func(line []byte) bool {
		var record struct {
			Route      string `json:"route"`
			Tokens     int64  `json:"tokens"`
			CostMicros int64  `json:"cost_micros"`
		}
		if json.Unmarshal(line, &record) != nil || record.Tokens < 0 || record.CostMicros < 0 {
			return false
		}
		if record.Route == route {
			total.tokens += record.Tokens
			total.costMicros += record.CostMicros
			found = true
		}
		return true
	})
	c.skipped += skipped
	if err != nil || !found {
		return nil, err
	}
	return &total, nil
}

// gitName is the span name of a git call: git and its subcommand, past the
// global options.
func gitName(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c" || args[i] == "-C":
			i++
		case !strings.HasPrefix(args[i], "-"):
			return "git " + args[i]
		}
	}
	return "git"
}

var (
	urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)
	secretOption   = regexp.MustCompile(`(?i)(extraheader|authorization|password|passwd|token|secret)`)
)

// redactArgs joins a git call's arguments without what could be a credential:
// the user part of a URL, and every argument that names a secret. Spans can
// leave the machine, so this runs before anything is stored in one.
func redactArgs(args []string) string {
	cleaned := make([]string, len(args))
	for i, arg := range args {
		if secretOption.MatchString(arg) {
			cleaned[i] = redactedArg
			continue
		}
		cleaned[i] = urlCredentials.ReplaceAllString(arg, "$1")
	}
	return strings.Join(cleaned, " ")
}
