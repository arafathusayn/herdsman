package context

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The kinds herdsman writes. A reader accepts any kind that fits the grammar,
// so that a newer writer's events stay readable.
const (
	KindTaskCreated = "task.created"
	KindTaskStarted = "task.started"
	KindTaskDone    = "task.done"
	KindTaskLinked  = "task.linked"
	KindRouteUp     = "route.up"
	KindRouteClosed = "route.closed"
	KindMergeLanded = "merge.landed"
	KindNote        = "note"
)

// Event is one file of the chronology. The file is a header between two "---"
// lines, one "key: value" per line, followed by a Markdown body. A value is a
// plain or double-quoted scalar, a [flow, list] or a { flow: map }.
type Event struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	At      time.Time         `json:"at"`
	Actor   string            `json:"actor"`
	Task    string            `json:"task,omitempty"`
	Repos   []string          `json:"repos,omitempty"`
	Parents []string          `json:"parents"`
	Commits map[string]string `json:"commits,omitempty"`
	// Extra holds the keys this version does not know, values as written.
	Extra map[string]string `json:"extra,omitempty"`
	Body  string            `json:"body"`
}

const (
	fence       = "---\n"
	keyID       = "id"
	keyKind     = "kind"
	keyAt       = "at"
	keyActor    = "actor"
	keyTask     = "task"
	keyRepos    = "repos"
	keyParents  = "parents"
	keyCommits  = "commits"
	commentMark = " #"
)

var (
	keyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	kindPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	plainPattern = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
	knownKeys    = []string{keyID, keyKind, keyAt, keyActor, keyTask, keyRepos, keyParents, keyCommits}
	requiredKeys = []string{keyID, keyKind, keyAt, keyActor, keyParents}
)

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// parseEvent reads the text of one event file.
func parseEvent(data []byte) (Event, error) {
	rest, ok := strings.CutPrefix(string(data), fence)
	if !ok {
		return Event{}, malformed("no opening fence")
	}
	end := strings.Index("\n"+rest, "\n"+fence)
	if end < 0 {
		return Event{}, malformed("no closing fence")
	}
	fields, err := parseHeader(rest[:end])
	if err != nil {
		return Event{}, err
	}
	e, err := eventFrom(fields)
	if err != nil {
		return Event{}, err
	}
	e.Body = rest[end+len(fence):]
	return e, nil
}

func parseHeader(header string) (map[string]string, error) {
	fields := map[string]string{}
	for raw := range strings.SplitSeq(header, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || !keyPattern.MatchString(key) {
			return nil, malformed("bad header line %q", line)
		}
		if _, dup := fields[key]; dup {
			return nil, malformed("key %q is repeated", key)
		}
		fields[key] = cleanValue(value)
	}
	for _, key := range requiredKeys {
		if _, ok := fields[key]; !ok {
			return nil, malformed("key %q is missing", key)
		}
	}
	return fields, nil
}

func eventFrom(fields map[string]string) (Event, error) {
	e := Event{ID: fields[keyID], Kind: fields[keyKind]}
	if !validULID(e.ID) {
		return e, malformed("bad id %q", e.ID)
	}
	if !kindPattern.MatchString(e.Kind) {
		return e, malformed("bad kind %q", e.Kind)
	}
	at, err := time.Parse(time.RFC3339, fields[keyAt])
	if err != nil {
		return e, malformed("bad time %q", fields[keyAt])
	}
	e.At = at.UTC()
	if e.Actor, err = scalar(fields[keyActor]); err != nil {
		return e, err
	}
	if strings.TrimSpace(e.Actor) == "" {
		return e, malformed("empty actor")
	}
	if e.Task, err = scalar(fields[keyTask]); err != nil {
		return e, err
	}
	if e.Parents, err = list(fields[keyParents]); err != nil {
		return e, err
	}
	if err := checkParents(e.Parents); err != nil {
		return e, malformed("%s", err)
	}
	if value, ok := fields[keyRepos]; ok {
		if e.Repos, err = list(value); err != nil {
			return e, err
		}
	}
	if value, ok := fields[keyCommits]; ok {
		if e.Commits, err = flowMap(value); err != nil {
			return e, err
		}
	}
	for key, value := range fields {
		if !slices.Contains(knownKeys, key) {
			if e.Extra == nil {
				e.Extra = map[string]string{}
			}
			e.Extra[key] = value
		}
	}
	return e, nil
}

// checkParents refuses a parent that is no id and one named twice.
func checkParents(parents []string) error {
	for i, parent := range parents {
		if !validULID(parent) {
			return fmt.Errorf("bad parent %q", parent)
		}
		if slices.Contains(parents[:i], parent) {
			return fmt.Errorf("parent %s is named twice", parent)
		}
	}
	return nil
}

// cleanValue drops the spaces around a raw value and a trailing comment.
func cleanValue(value string) string {
	value = strings.TrimSpace(value)
	if i := topIndex(value, commentMark); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return value
}

// topIndex finds sep outside double quotes, or returns -1.
func topIndex(s, sep string) int {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

func scalar(value string) (string, error) {
	if !strings.HasPrefix(value, `"`) {
		return value, nil
	}
	text, err := strconv.Unquote(value)
	if err != nil {
		return "", malformed("bad quoted value %s", value)
	}
	return text, nil
}

// flowItems splits the inside of a flow collection into trimmed, non-empty
// items.
func flowItems(value, open, closer string) ([]string, error) {
	if !strings.HasPrefix(value, open) || !strings.HasSuffix(value, closer) {
		return nil, malformed("expected %s...%s, got %s", open, closer, value)
	}
	rest := strings.TrimSpace(value[len(open) : len(value)-len(closer)])
	items := []string{}
	for rest != "" {
		item := rest
		next := topIndex(rest, ",")
		if next >= 0 {
			item, rest = rest[:next], rest[next+1:]
		} else {
			rest = ""
		}
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, malformed("empty item in %s", value)
		}
		items = append(items, item)
	}
	return items, nil
}

func list(value string) ([]string, error) {
	items, err := flowItems(value, "[", "]")
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		if items[i], err = scalar(item); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func flowMap(value string) (map[string]string, error) {
	items, err := flowItems(value, "{", "}")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		colon := topIndex(item, ":")
		if colon < 0 {
			return nil, malformed("no colon in %s", item)
		}
		key, err := scalar(strings.TrimSpace(item[:colon]))
		if err != nil {
			return nil, err
		}
		text, err := scalar(strings.TrimSpace(item[colon+1:]))
		if err != nil {
			return nil, err
		}
		if _, dup := out[key]; dup || text == "" {
			return nil, malformed("bad entry %s", item)
		}
		out[key] = text
	}
	return out, nil
}

// formatEvent writes the file text of an event, keys in their fixed order.
func formatEvent(e Event) []byte {
	var b strings.Builder
	line := func(key, value string) { b.WriteString(key + ": " + value + "\n") }
	b.WriteString(fence)
	line(keyID, e.ID)
	line(keyKind, e.Kind)
	line(keyAt, e.At.UTC().Format(time.RFC3339))
	line(keyActor, quote(e.Actor))
	if e.Task != "" {
		line(keyTask, quote(e.Task))
	}
	if len(e.Repos) > 0 {
		line(keyRepos, flowList(e.Repos))
	}
	line(keyParents, flowList(e.Parents))
	if len(e.Commits) > 0 {
		entries := make([]string, 0, len(e.Commits))
		for _, key := range slices.Sorted(maps.Keys(e.Commits)) {
			entries = append(entries, quote(key)+": "+quote(e.Commits[key]))
		}
		line(keyCommits, "{ "+strings.Join(entries, ", ")+" }")
	}
	for _, key := range slices.Sorted(maps.Keys(e.Extra)) {
		line(key, e.Extra[key])
	}
	b.WriteString(fence)
	b.WriteString(e.Body)
	return []byte(b.String())
}

func flowList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = quote(item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func quote(s string) string {
	if plainPattern.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}
