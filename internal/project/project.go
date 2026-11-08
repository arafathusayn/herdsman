// Package project keeps the records that give a task a home: projects, the
// folders bound to them, their repositories, and their tasks.
package project

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The failure classes of this package; every error wraps exactly one.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	ErrRefused  = errors.New("refused")
)

// State is a project's lifecycle state.
type State string

const (
	Active   State = "active"
	Archived State = "archived"
)

// Repo is one repository of a project.
type Repo struct {
	Name          string
	Path          string
	DefaultBranch string
	Remote        string
}

// Project is a stored project with what hangs off it.
type Project struct {
	ID          string
	Name        string
	Description string
	State       State
	Folders     []string
	Repos       []Repo
	OpenTasks   int
	Created     int64
	Updated     int64
}

// ProjectInput describes a new project.
type ProjectInput struct {
	Name        string
	Description string
	Repos       []Repo
}

// ProjectPatch changes a project; a nil pointer leaves the field alone.
type ProjectPatch struct {
	Name        *string
	Description *string
	AddRepos    []Repo
	RemoveRepos []string
}

const idLength = 26

const idAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID: a millisecond time followed by random bits.
func NewID() string {
	var random [idLength - 10]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(err) // crypto/rand does not fail on a supported platform
	}
	var out [idLength]byte
	t := uint64(time.Now().UnixMilli())
	for i := 9; i >= 0; i-- {
		out[i] = idAlphabet[t&31]
		t >>= 5
	}
	for i, b := range random {
		out[10+i] = idAlphabet[b&31]
	}
	return string(out[:])
}

// IsID reports whether s has the shape of an id, which no name or slug may have.
func IsID(s string) bool {
	if len(s) != idLength || s[0] > '7' {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !strings.ContainsRune(idAlphabet, r) })
}

// cleanName trims a project name and refuses one a reference could not reach.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("%w: a project needs a name", ErrInvalid)
	case name == "@":
		return "", fmt.Errorf("%w: @ names the current project and cannot be a name", ErrInvalid)
	case IsID(name):
		return "", fmt.Errorf("%w: %s has the shape of an id", ErrInvalid, name)
	case strings.ContainsFunc(name, unicode.IsControl):
		return "", fmt.Errorf("%w: a project name holds no control characters", ErrInvalid)
	}
	return name, nil
}

// cleanRepos fills in each repository's name from its path and checks the list.
func cleanRepos(repos []Repo) ([]Repo, error) {
	seen := map[string]bool{}
	out := make([]Repo, 0, len(repos))
	for _, r := range repos {
		if !filepath.IsAbs(r.Path) {
			return nil, fmt.Errorf("%w: repository path %q is not absolute", ErrInvalid, r.Path)
		}
		r.Path = filepath.Clean(r.Path)
		if r.Name == "" {
			r.Name = filepath.Base(r.Path)
		}
		if r.Name == string(filepath.Separator) {
			return nil, fmt.Errorf("%w: repository at %s needs a name", ErrInvalid, r.Path)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("%w: repository %s is listed twice", ErrInvalid, r.Name)
		}
		seen[r.Name] = true
		out = append(out, r)
	}
	return out, nil
}

// cleanLabels trims, sorts and removes repeats.
func cleanLabels(labels []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			return nil, fmt.Errorf("%w: a label cannot be empty", ErrInvalid)
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out, nil
}
