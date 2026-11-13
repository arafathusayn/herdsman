// Package wtree is the worktree layer's library: naming, selectors, the
// per-repository state, and the collection behind the worktree table.
package wtree

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"
	"unicode/utf8"
)

// Vars is everything a template can read. Values holds the named variables,
// Custom the vars.<key> table, Port the first port of the task's block and
// Ports the named ports.
type Vars struct {
	Values map[string]string
	Custom map[string]string
	Port   int
	Ports  map[string]int
}

// UnknownVariableError names a template variable that cannot be read.
type UnknownVariableError struct {
	Name       string
	Suggestion string // Closest known name; empty when nothing is close.
	NoValue    bool   // The name is known but this context does not set it.
}

func (e *UnknownVariableError) Error() string {
	if e.NoValue {
		return fmt.Sprintf("template variable %q has no value here", e.Name)
	}
	message := fmt.Sprintf("unknown template variable %q", e.Name)
	if e.Suggestion != "" {
		message += fmt.Sprintf("; did you mean %q?", e.Suggestion)
	}
	return message
}

// variables are the names a template reads as plain functions, which is what
// lets {{ branch }} and {{ branch | sanitize }} both parse.
var variables = []string{
	"args", "base", "branch", "commit", "compose", "default_branch", "hook_name", "hook_type",
	"owner", "pr_number", "pr_url", "remote", "repo", "repo_path", "route", "short_commit",
	"target", "task", "type", "worktree_name", "worktree_path",
}

var (
	undefinedFunction = regexp.MustCompile(`function "([^"]+)" not defined`)
	missingMapKey     = regexp.MustCompile(`map has no entry for key "([^"]+)"`)
)

// Render expands text with v. A name the template cannot read is an
// *UnknownVariableError.
func Render(name, text string, v Vars) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Funcs(funcMap(v)).Parse(text)
	if err != nil {
		if match := undefinedFunction.FindStringSubmatch(err.Error()); match != nil {
			return "", &UnknownVariableError{Name: match[1], Suggestion: nearest(match[1], variables)}
		}
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, nil); err != nil {
		if match := missingMapKey.FindStringSubmatch(err.Error()); match != nil {
			return "", missingCustom(match[1], v.Custom)
		}
		var unknown *UnknownVariableError
		if errors.As(err, &unknown) {
			return "", unknown
		}
		return "", fmt.Errorf("template %s: %w", name, err)
	}
	return out.String(), nil
}

func missingCustom(key string, custom map[string]string) error {
	keys := make([]string, 0, len(custom))
	for known := range custom {
		keys = append(keys, known)
	}
	sort.Strings(keys)
	unknown := &UnknownVariableError{Name: "vars." + key}
	if close := nearest(key, keys); close != "" {
		unknown.Suggestion = "vars." + close
	}
	return unknown
}

func funcMap(v Vars) template.FuncMap {
	funcs := template.FuncMap{
		"sanitize":    Sanitize,
		"sanitize_db": SanitizeDB,
		"hash":        hashText,
		"hash_port":   HashPort,
		"codename":    Codename,
		"basename":    filepath.Base,
		"dirname":     filepath.Dir,
		"tojson":      toJSON,
		"vars":        func() map[string]string { return v.Custom },
		"port":        func(names ...string) (int, error) { return v.port(names) },
	}
	for _, name := range variables {
		funcs[name] = func() (string, error) { return v.value(name) }
	}
	return funcs
}

func (v Vars) value(name string) (string, error) {
	if value, ok := v.Values[name]; ok {
		return value, nil
	}
	if name == "compose" && v.Values["repo"] != "" && v.Values["task"] != "" {
		return composeName(v.Values["repo"] + "-" + v.Values["task"]), nil
	}
	return "", &UnknownVariableError{Name: name, NoValue: true}
}

func (v Vars) port(names []string) (int, error) {
	switch len(names) {
	case 0:
		if v.Port == 0 {
			return 0, &UnknownVariableError{Name: "port", NoValue: true}
		}
		return v.Port, nil
	case 1:
		if port, ok := v.Ports[names[0]]; ok {
			return port, nil
		}
		return 0, fmt.Errorf("no port named %q in this task's block", names[0])
	default:
		return 0, errors.New("port takes at most one name")
	}
}

// composeName keeps what a compose project name allows.
func composeName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
}

// Sanitize makes a value safe as one path component.
func Sanitize(value string) string {
	return strings.NewReplacer("/", "-", `\`, "-").Replace(value)
}

const databaseNameLimit = 63

// SanitizeDB makes a value a database identifier. The short hash keeps two
// values that clean up to the same text apart.
func SanitizeDB(value string) string {
	var clean strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			clean.WriteRune(r)
		} else {
			clean.WriteByte('_')
		}
	}
	name := clean.String()
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	suffix := "_" + hashText(value)[:3]
	if limit := databaseNameLimit - len(suffix); len(name) > limit {
		name = name[:limit]
	}
	return name + suffix
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

const (
	portFloor     = 10000
	portBlock     = 10
	portBlockSpan = 5000 // Blocks available above the floor.
)

// HashPort maps a value to the first port of a block. Blocks are aligned, so
// two tasks either get the same block or blocks that do not overlap.
// PortBlock is how many ports one task's block holds.
const PortBlock = portBlock

func HashPort(value string) int {
	sum := sha256.Sum256([]byte(value))
	return portFloor + int(binary.BigEndian.Uint64(sum[:8])%portBlockSpan)*portBlock
}

var (
	codenameLeft  = []string{"amber", "brisk", "calm", "deft", "eager", "fleet", "gentle", "hardy", "ivory", "jolly", "keen", "lucid", "mellow", "noble", "plucky", "quiet"}
	codenameRight = []string{"badger", "crane", "dingo", "egret", "ferret", "gecko", "heron", "ibis", "jackal", "kestrel", "lynx", "marten", "newt", "otter", "puffin", "raven"}
)

// Codename turns a value into a stable, readable two-word name.
func Codename(value string) string {
	sum := sha256.Sum256([]byte(value))
	return codenameLeft[int(sum[0])%len(codenameLeft)] + "-" + codenameRight[int(sum[1])%len(codenameRight)]
}

func toJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// nearest returns the candidate closest to name, or "" when none is close
// enough to be what was meant. A tie goes to the first in byte order.
func nearest(name string, candidates []string) string {
	sorted := slices.Sorted(slices.Values(candidates))
	limit := max(2, utf8.RuneCountInString(name)/3)
	best, bestDistance := "", limit+1
	for _, candidate := range sorted {
		if distance := editDistance(name, candidate); distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

func editDistance(a, b string) int {
	left, right := []rune(a), []rune(b)
	row := make([]int, len(right)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(left); i++ {
		diagonal := row[0]
		row[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			row[j], diagonal = min(row[j]+1, row[j-1]+1, diagonal+cost), row[j]
		}
	}
	return row[len(right)]
}
