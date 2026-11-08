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
	ReasonUnclear   Reason = "unclear"
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
// It reads each simple command of a shell line, past redirections, assignments,
// reserved words and wrappers such as env, and compares program base names. A
// line whose program it cannot name for certain, because the name is computed
// or another program would run it, is refused too: a deny list that can be
// stepped around by spelling is not one. With no deny list nothing is parsed.
func (p Policy) CheckCommand(command string) error {
	if len(p.Deny) == 0 {
		return nil
	}
	commands, unclear := readShell(command)
	if unclear != "" {
		return unclearCommand(command, unclear)
	}
	for _, words := range commands {
		for _, deny := range p.Deny {
			denied := strings.Fields(deny)
			switch matchDeny(words, denied) {
			case denyMatched:
				return &Refusal{
					Reason:  ReasonDenied,
					Message: fmt.Sprintf("command %q is denied by [policy] deny entry %q", command, deny),
				}
			case denyUnknown:
				return unclearCommand(command, "an argument of "+words.names[0]+" is computed")
			}
		}
	}
	return nil
}

func unclearCommand(command, why string) error {
	return &Refusal{
		Reason:  ReasonUnclear,
		Message: fmt.Sprintf("command %q refused: [policy] deny is set and %s, so what it runs cannot be checked", command, why),
	}
}

type denyResult int

const (
	denyNone denyResult = iota
	denyMatched
	denyUnknown
)

// matchDeny compares one entry with a command. Later words of an entry may have
// options between them, so git -c a=b push still reads as git push.
func matchDeny(c shellCommand, denied []string) denyResult {
	if len(denied) == 1 && slices.Contains(c.wrappers, path.Base(denied[0])) {
		return denyMatched
	}
	if len(c.names) == 0 || path.Base(c.names[0]) != path.Base(denied[0]) {
		return denyNone
	}
	rest := denied[1:]
	for _, word := range c.names[1:] {
		if len(rest) > 0 && word == rest[0] {
			rest = rest[1:]
		}
	}
	switch {
	case len(rest) == 0:
		return denyMatched
	case c.computedArg:
		return denyUnknown
	default:
		return denyNone
	}
}

// shellWord is one word; computed means expansion decides its final text.
type shellWord struct {
	text     string
	computed bool
}

// shellCommand is one simple command once its prefixes are gone.
type shellCommand struct {
	names       []string
	wrappers    []string
	computedArg bool
}

type shellReader struct {
	commands []shellCommand
	words    []shellWord
	word     strings.Builder
	computed bool
	inWord   bool
	quoted   bool
	// dropNext says the next word is a redirection's target.
	dropNext bool
	unclear  string
}

func (r *shellReader) fail(why string) {
	if r.unclear == "" {
		r.unclear = why
	}
}

func (r *shellReader) endWord() {
	if !r.inWord {
		return
	}
	if r.dropNext {
		r.dropNext = false
	} else {
		r.words = append(r.words, shellWord{r.word.String(), r.computed})
	}
	r.word.Reset()
	r.inWord, r.computed, r.quoted = false, false, false
}

func (r *shellReader) endCommand() {
	r.endWord()
	r.dropNext = false
	command, unclear := parseProgram(r.words)
	if unclear != "" {
		r.fail(unclear)
	}
	if len(command.names)+len(command.wrappers) > 0 {
		r.commands = append(r.commands, command)
	}
	r.words = nil
}

func (r *shellReader) write(c byte) {
	r.word.WriteByte(c)
	r.inWord = true
}

// redirect handles the operator at line[i], returning the index of its last
// byte. A word of digits right before it is a descriptor, not an argument.
func (r *shellReader) redirect(line string, i int) int {
	if r.inWord && !r.quoted && strings.Trim(r.word.String(), "0123456789") == "" {
		r.word.Reset()
		r.inWord, r.computed = false, false
	}
	r.endWord()
	for i+1 < len(line) && strings.IndexByte("<>&|", line[i+1]) >= 0 {
		i++
	}
	r.dropNext = true
	return i
}

// doubleQuoted reads from just after an opening double quote and returns the
// index of the closing one.
func (r *shellReader) doubleQuoted(line string, i int) int {
	for i++; i < len(line) && line[i] != '"'; i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i++
			if strings.IndexByte("$`\"\\", line[i]) < 0 {
				r.word.WriteByte('\\')
			}
			r.word.WriteByte(line[i])
		case c == '`' || c == '$' && i+1 < len(line) && line[i+1] == '(':
			r.fail("it uses command substitution")
		case c == '$':
			r.computed = true
			r.word.WriteByte(c)
		default:
			r.word.WriteByte(c)
		}
	}
	return i
}

// readShell splits a line into its simple commands. The second result names why
// the line cannot be read for certain, or is empty.
func readShell(line string) ([]shellCommand, string) {
	r := &shellReader{}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'':
			r.inWord, r.quoted = true, true
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				end = len(line) - i - 1
			}
			r.word.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			r.inWord, r.quoted = true, true
			i = r.doubleQuoted(line, i)
		case c == '\\' && i+1 < len(line):
			i++
			if line[i] != '\n' {
				r.write(line[i])
				r.quoted = true
			}
		case c == '`' || c == '(' || c == ')':
			r.fail("it uses a subshell or command substitution")
		case c == '<' || c == '>' || c == '&' && i+1 < len(line) && line[i+1] == '>':
			i = r.redirect(line, i)
		case strings.IndexByte("&|;\n", c) >= 0:
			r.endCommand()
		case c == ' ' || c == '\t' || c == '\r':
			r.endWord()
		default:
			if strings.IndexByte("$*?[{", c) >= 0 {
				r.computed = true
			}
			r.write(c)
		}
	}
	r.endCommand()
	return r.commands, r.unclear
}

// parseProgram removes what stands before the program and notes anything that
// would run a program this reader cannot see.
func parseProgram(words []shellWord) (shellCommand, string) {
	var command shellCommand
	for len(words) > 0 {
		word := words[0]
		if isAssignment(word.text) {
			words = words[1:]
			continue
		}
		switch word.text {
		case "if", "then", "elif", "else", "while", "until", "do", "!", "{":
			words = words[1:]
			continue
		case "for":
			return command, ""
		case "case", "select", "function":
			return command, "it uses a " + word.text + " construct"
		}
		if word.computed {
			return command, "its program name is computed"
		}
		base := path.Base(word.text)
		switch base {
		case "env":
			command.wrappers = append(command.wrappers, base)
			rest, unclear := skipEnvOptions(words[1:])
			if unclear != "" {
				return command, unclear
			}
			words = rest
			continue
		case "exec", "command", "builtin", "nohup", "time":
			command.wrappers = append(command.wrappers, base)
			words = words[1:]
			if base == "time" && len(words) > 0 && words[0].text == "-p" {
				words = words[1:]
			}
			if len(words) > 0 && strings.HasPrefix(words[0].text, "-") {
				return command, base + " has an option this check does not read"
			}
			continue
		case "eval", "source", ".", "xargs", "sudo", "doas", "nice", "ionice", "timeout", "stdbuf", "setsid", "watch", "parallel":
			return command, base + " runs another program"
		case "sh", "bash", "dash", "zsh", "ksh", "fish":
			if slices.ContainsFunc(words[1:], isShellCommandOption) {
				return command, base + " is given a command to run"
			}
		case "find":
			if slices.ContainsFunc(words[1:], isFindExec) {
				return command, "find runs another program"
			}
		}
		for _, w := range words {
			command.names = append(command.names, w.text)
			command.computedArg = command.computedArg || w.computed
		}
		return command, ""
	}
	return command, ""
}

func isShellCommandOption(word shellWord) bool {
	if word.text == "--command" {
		return true
	}
	return strings.HasPrefix(word.text, "-") && !strings.HasPrefix(word.text, "--") && strings.Contains(word.text, "c")
}

func isFindExec(word shellWord) bool {
	return slices.Contains([]string{"-exec", "-execdir", "-ok", "-okdir"}, word.text)
}

// skipEnvOptions removes env's options and assignments, including the options
// that take a separate operand.
func skipEnvOptions(words []shellWord) ([]shellWord, string) {
	for len(words) > 0 {
		text := words[0].text
		switch {
		case isAssignment(text),
			slices.Contains([]string{"-", "--", "-i", "-0", "-v", "--ignore-environment", "--null", "--debug"}, text),
			strings.HasPrefix(text, "--unset="), strings.HasPrefix(text, "--chdir="),
			len(text) > 2 && (strings.HasPrefix(text, "-u") || strings.HasPrefix(text, "-C")):
			words = words[1:]
		case slices.Contains([]string{"-u", "-C", "--unset", "--chdir"}, text):
			words = words[min(2, len(words)):]
		case strings.HasPrefix(text, "-"):
			return nil, "env has an option this check does not read"
		default:
			return words, ""
		}
	}
	return nil, ""
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
