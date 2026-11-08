// Package commitmsg parses, generates and falls back on Conventional Commits
// messages for the commits the worktree layer makes.
package commitmsg

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxHeader is the longest first line Parse accepts, in characters.
const MaxHeader = 100

// MaxScope is the longest scope a pin or a branch may supply.
const MaxScope = 24

// Types are the commit types the convention allows.
var Types = []string{"build", "chore", "ci", "docs", "feat", "fix", "perf", "refactor", "test"}

// Footer is one trailer of the last paragraph.
type Footer struct {
	Token string `json:"token"`
	Value string `json:"value"`
}

// Message is a commit message that follows the convention.
type Message struct {
	Type     string   `json:"type"`
	Scope    string   `json:"scope,omitempty"`
	Bang     bool     `json:"bang,omitempty"` // The header carries "!".
	Breaking bool     `json:"breaking,omitempty"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body,omitempty"`
	Footers  []Footer `json:"footers,omitempty"`
}

// ParseError says why a text is not a valid message.
type ParseError struct {
	Reason string
}

func (e *ParseError) Error() string { return "not a conventional commit message: " + e.Reason }

func refuse(format string, args ...any) error {
	return &ParseError{Reason: fmt.Sprintf(format, args...)}
}

// Header returns the first line.
func (m Message) Header() string {
	var out strings.Builder
	out.WriteString(m.Type)
	if m.Scope != "" {
		out.WriteString("(" + m.Scope + ")")
	}
	if m.Bang {
		out.WriteString("!")
	}
	out.WriteString(": " + m.Subject)
	return out.String()
}

// String returns the whole message, ready for git.
func (m Message) String() string {
	parts := []string{m.Header()}
	if m.Body != "" {
		parts = append(parts, m.Body)
	}
	if len(m.Footers) > 0 {
		lines := make([]string, 0, len(m.Footers))
		for _, footer := range m.Footers {
			lines = append(lines, footerLine(footer))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

func footerLine(footer Footer) string {
	if strings.HasPrefix(footer.Value, "#") {
		return footer.Token + " " + footer.Value
	}
	return footer.Token + ": " + footer.Value
}

// ValidScope reports whether a scope uses only the allowed characters.
func ValidScope(scope string) bool {
	if scope == "" {
		return false
	}
	for _, r := range scope {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', strings.ContainsRune("-_./", r):
		default:
			return false
		}
	}
	return true
}

// Parse reads a message. Trailing line ends are ignored; anything else that
// departs from the convention is a *ParseError.
func Parse(text string) (Message, error) {
	text = strings.TrimRight(text, "\n")
	if strings.Contains(text, "\r") {
		return Message{}, refuse("carriage return in the message")
	}
	header, rest, hasRest := strings.Cut(text, "\n")
	message, err := parseHeader(header)
	if err != nil {
		return Message{}, err
	}
	if !hasRest {
		return message, nil
	}
	if blank, _, _ := strings.Cut(rest, "\n"); blank != "" {
		return Message{}, refuse("the line after the header must be blank")
	}
	message.Body = strings.Trim(rest, "\n")
	message.Body, message.Footers = splitFooters(message.Body)
	for _, footer := range message.Footers {
		if footer.Token == "BREAKING CHANGE" || footer.Token == "BREAKING-CHANGE" {
			message.Breaking = true
		}
	}
	return message, nil
}

func parseHeader(header string) (Message, error) {
	if utf8.RuneCountInString(header) > MaxHeader {
		return Message{}, refuse("the header is longer than %d characters", MaxHeader)
	}
	prefix, subject, found := strings.Cut(header, ": ")
	if !found {
		return Message{}, refuse("the header has no \": \" after the type")
	}
	var message Message
	if strings.HasSuffix(prefix, "!") {
		message.Bang, message.Breaking = true, true
		prefix = strings.TrimSuffix(prefix, "!")
	}
	message.Type = prefix
	if open := strings.IndexByte(prefix, '('); open >= 0 {
		if !strings.HasSuffix(prefix, ")") {
			return Message{}, refuse("the scope is not closed")
		}
		message.Type, message.Scope = prefix[:open], prefix[open+1:len(prefix)-1]
		if !ValidScope(message.Scope) {
			return Message{}, refuse("scope %q is empty or has characters outside a-z 0-9 - _ . /", message.Scope)
		}
	}
	if !slices.Contains(Types, message.Type) {
		return Message{}, refuse("type %q is not one of %s", message.Type, strings.Join(Types, ", "))
	}
	if err := checkSubject(subject); err != nil {
		return Message{}, err
	}
	message.Subject = subject
	return message, nil
}

func checkSubject(subject string) error {
	if strings.TrimSpace(subject) != subject || subject == "" {
		return refuse("the subject is empty or has space around it")
	}
	first, _ := utf8.DecodeRuneInString(subject)
	if unicode.IsUpper(first) {
		return refuse("the subject starts with a capital letter")
	}
	if strings.HasSuffix(subject, ".") {
		return refuse("the subject ends with a period")
	}
	if strings.IndexFunc(subject, unicode.IsControl) >= 0 {
		return refuse("the subject has a control character")
	}
	return nil
}

// splitFooters takes the last paragraph off a body when every line of it is a
// trailer.
func splitFooters(body string) (string, []Footer) {
	last, before := body, ""
	if index := strings.LastIndex(body, "\n\n"); index >= 0 {
		before, last = strings.TrimRight(body[:index], "\n"), body[index+2:]
	}
	var footers []Footer
	for _, line := range strings.Split(last, "\n") {
		footer, ok := parseFooter(line)
		if !ok {
			return body, nil
		}
		footers = append(footers, footer)
	}
	return before, footers
}

func parseFooter(line string) (Footer, bool) {
	for _, token := range []string{"BREAKING CHANGE", "BREAKING-CHANGE"} {
		if value, ok := strings.CutPrefix(line, token+": "); ok && value != "" {
			return Footer{Token: token, Value: value}, true
		}
	}
	end := strings.IndexFunc(line, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-')
	})
	if end <= 0 {
		return Footer{}, false
	}
	token, tail := line[:end], line[end:]
	if value, ok := strings.CutPrefix(tail, ": "); ok && value != "" {
		return Footer{Token: token, Value: value}, true
	}
	if strings.HasPrefix(tail, " #") && len(tail) > 2 {
		return Footer{Token: token, Value: tail[1:]}, true
	}
	return Footer{}, false
}
