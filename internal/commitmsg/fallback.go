package commitmsg

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// fallbackHeader is the longest first line the fallback writes.
const fallbackHeader = 72

// Pin holds the header parts the caller already knows.
type Pin struct {
	Type  string
	Scope string
}

// Validate refuses a pin no message could carry.
func (p Pin) Validate() error {
	if p.Type != "" && !slices.Contains(Types, p.Type) {
		return fmt.Errorf("type %q is not one of %s", p.Type, strings.Join(Types, ", "))
	}
	if p.Scope != "" && (!ValidScope(p.Scope) || len(p.Scope) > MaxScope) {
		return fmt.Errorf("scope %q must be at most %d characters of a-z 0-9 - _ . /", p.Scope, MaxScope)
	}
	return nil
}

func (p Pin) apply(message Message) Message {
	if p.Type != "" {
		message.Type = p.Type
	}
	if p.Scope != "" {
		message.Scope = p.Scope
	}
	return message
}

// BranchParts reads a type and a scope from a branch name such as feat/login.
// A branch that names no type gives chore and no scope.
func BranchParts(branch string) (kind, scope string) {
	head, tail, found := strings.Cut(branch, "/")
	if !found || !slices.Contains(Types, head) {
		return "chore", ""
	}
	scope = strings.Trim(strings.Map(func(r rune) rune {
		r = unicode.ToLower(r)
		if r < utf8.RuneSelf && ValidScope(string(r)) && r != '/' {
			return r
		}
		return '-'
	}, tail), "-")
	if !ValidScope(scope) || len(scope) > MaxScope {
		scope = ""
	}
	return head, scope
}

// Fallback writes a valid message from the branch and the changed files alone,
// so a commit never depends on a model being available.
func Fallback(branch string, files []string, pin Pin) Message {
	kind, scope := BranchParts(branch)
	base := pin.apply(Message{Type: kind, Scope: scope})
	for _, subject := range subjects(files) {
		message := base
		message.Subject = subject
		if _, err := Parse(message.Header()); err == nil && utf8.RuneCountInString(message.Header()) <= fallbackHeader {
			return message
		}
	}
	base.Subject = fmt.Sprintf("update %d files", len(files))
	return base
}

// subjects lists the wordings to try, most specific first.
func subjects(files []string) []string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, path.Base(file))
	}
	switch len(names) {
	case 0:
		return nil
	case 1:
		return []string{"update " + names[0]}
	case 2:
		return []string{"update " + names[0] + " and " + names[1]}
	}
	return []string{fmt.Sprintf("update %s, %s and %d more files", names[0], names[1], len(names)-2)}
}
