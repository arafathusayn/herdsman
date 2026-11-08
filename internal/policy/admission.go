package policy

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// ScopeKind says what a budget is being applied to.
type ScopeKind string

// The scope kinds.
const (
	ScopeTask  ScopeKind = "task"
	ScopeRoute ScopeKind = "route"
)

// Scope is one task or route.
type Scope struct {
	Kind ScopeKind
	Name string
}

func (s Scope) String() string { return string(s.Kind) + " " + s.Name }

// Reason classifies a refusal for callers that branch on it.
type Reason string

// The refusal reasons.
const (
	ReasonBudget    Reason = "budget"
	ReasonAgents    Reason = "agents"
	ReasonDenied    Reason = "denied"
	ReasonProtected Reason = "protected"
	ReasonChecks    Reason = "checks"
	ReasonLanding   Reason = "landing"
	ReasonReview    Reason = "review"
)

// Refusal is a policy saying no. Message says what to change to continue.
type Refusal struct {
	Reason  Reason
	Scope   Scope
	Missing []string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// Operation is a new herdsman operation asking to start.
type Operation struct {
	// Kind is what the operation does: new, merge, up, hook and so on.
	Kind  string
	Task  string
	Route string
	// Agents is how many agents would run if this operation starts.
	Agents int
}

// Usages holds the consumption of the operation's task and route.
type Usages struct {
	Task  Usage
	Route Usage
}

// Admit is the hard promise: at a wall deadline no new operation starts. It
// looks only at wall time, since the other limits have their own enforcement.
// Nothing here remembers a refusal, so raising the budget reopens admission.
func Admit(p Policy, op Operation, usage Usages, now time.Time) error {
	if p.MaxAgents > 0 && op.Agents > p.MaxAgents {
		return &Refusal{
			Reason: ReasonAgents,
			Message: fmt.Sprintf("operation %s would run %d agents; max_agents is %d: raise it in [policy] or stop an agent",
				op.Kind, op.Agents, p.MaxAgents),
		}
	}
	wall := Budget{Wall: p.Budget.Wall}
	for _, candidate := range []struct {
		scope Scope
		usage Usage
	}{
		{Scope{ScopeTask, op.Task}, usage.Task},
		{Scope{ScopeRoute, op.Route}, usage.Route},
	} {
		if candidate.scope.Name == "" {
			continue
		}
		if over := wall.Check(candidate.usage, now); len(over) > 0 {
			return &Refusal{
				Reason: ReasonBudget,
				Scope:  candidate.scope,
				Message: fmt.Sprintf("operation %s refused: the wall budget of %s is spent (limit %s, used %s): raise budget.wall in [policy] to continue",
					op.Kind, candidate.scope, over[0].Limit, over[0].Used),
			}
		}
	}
	return nil
}

// CheckCommand refuses a hook or automation command that runs a denied program.
// It reads every simple command of a shell line, skipping environment prefixes,
// and compares program base names, so a full path does not slip past. It does
// not look inside a nested interpreter such as sh -c.
func (p Policy) CheckCommand(command string) error {
	for _, words := range simpleCommands(command) {
		for _, deny := range p.Deny {
			if denied := strings.Fields(deny); matchesDeny(words, denied) {
				return &Refusal{
					Reason:  ReasonDenied,
					Message: fmt.Sprintf("command %q is denied by [policy] deny entry %q", strings.Join(words, " "), deny),
				}
			}
		}
	}
	return nil
}

func matchesDeny(words, denied []string) bool {
	if len(words) < len(denied) || path.Base(words[0]) != path.Base(denied[0]) {
		return false
	}
	return slices.Equal(words[1:len(denied)], denied[1:])
}

// simpleCommands splits a shell line on its control operators, honoring quotes,
// and returns each command's words without leading assignments or env.
func simpleCommands(line string) [][]string {
	var (
		commands [][]string
		words    []string
		word     strings.Builder
		inWord   bool
		quote    byte
	)
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCommand := func() {
		endWord()
		if program := stripPrefixes(words); len(program) > 0 {
			commands = append(commands, program)
		}
		words = nil
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '\\' && i+1 < len(line):
			i++
			word.WriteByte(line[i])
			inWord = true
		case strings.IndexByte("&|;\n", c) >= 0:
			endCommand()
		case c == ' ' || c == '\t':
			endWord()
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	endCommand()
	return commands
}

func stripPrefixes(words []string) []string {
	for len(words) > 0 && isAssignment(words[0]) {
		words = words[1:]
	}
	if len(words) > 0 && words[0] == "env" {
		words = words[1:]
		for len(words) > 0 && (isAssignment(words[0]) || strings.HasPrefix(words[0], "-")) {
			words = words[1:]
		}
	}
	return words
}

func isAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")
	if !found || name == "" {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		return r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) < 0
}
