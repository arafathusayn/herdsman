package config

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// strictSections are the sections where a misspelling could quietly switch
// off a safety setting, so an unknown key stops the command. In [runtime] a
// misspelled backend would otherwise fall through to the installation default.
var strictSections = map[string]bool{"policy": true, "launch": true, "runtime": true}

// Warning is one key the configuration does not define.
type Warning struct {
	Key        string
	Suggestion string
}

// String is the line shown to the user.
func (w Warning) String() string {
	if w.Suggestion != "" {
		return fmt.Sprintf("unknown key %s (did you mean %s?)", w.Key, w.Suggestion)
	}
	return "unknown key " + w.Key
}

// StrictError is an unknown key that stops the command.
type StrictError struct {
	Warning Warning
}

func (e *StrictError) Error() string { return e.Warning.String() }

// report collects what normalization finds. strict turns every unknown key
// into an error, as the strict sections always do.
type report struct {
	strict   bool
	warnings []Warning
}

func (r *report) unknown(path, key string, md protoreflect.MessageDescriptor) error {
	return r.add(Warning{Key: path, Suggestion: suggest(key, md)})
}

// unknownWire reports a binary field this build does not define; it has a
// number and no name, so nothing can be suggested.
func (r *report) unknownWire(path string) error {
	return r.add(Warning{Key: path})
}

func (r *report) add(warning Warning) error {
	section, _, _ := strings.Cut(warning.Key, ".")
	if r.strict || strictSections[section] {
		return &StrictError{Warning: warning}
	}
	r.warnings = append(r.warnings, warning)
	return nil
}

// suggest returns the closest field name, when one is close enough to be a
// likely typo.
func suggest(key string, md protoreflect.MessageDescriptor) string {
	key = canonicalKey(key)
	best, bestDistance := "", len(key)/2+1
	fields := md.Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		if d := editDistance(key, name); d < bestDistance {
			best, bestDistance = name, d
		}
	}
	return best
}

// editDistance is the Damerau-Levenshtein distance limited to adjacent
// transpositions, so that a swapped pair of letters counts as one edit.
func editDistance(a, b string) int {
	rows := make([][]int, len(a)+1)
	for i := range rows {
		rows[i] = make([]int, len(b)+1)
		rows[i][0] = i
	}
	for j := range rows[0] {
		rows[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			rows[i][j] = min(rows[i-1][j]+1, rows[i][j-1]+1, rows[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				rows[i][j] = min(rows[i][j], rows[i-2][j-2]+1)
			}
		}
	}
	return rows[len(a)][len(b)]
}
