package web

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// RouteSetup is the setup the New route form chose (A32). The orchestrator
// receives it as text after the request (RouteSetup.text), so the skill
// reads one contract whether the user took the recommended setup or
// answered each item.
//
// JSON fields: recommended; checkpointer (what the user asked for; the text
// shows the effective value); github_account (the confirmed gh account, or
// omitted); roles (the coordinators' picks: role, runner, harness, model,
// effort); answers (one {item, answer} per setup item: the recommended
// sentence, or the user's answer on the custom path; an empty answer means
// the route asks).
type RouteSetup struct {
	Recommended   bool           `json:"recommended"`
	Checkpointer  bool           `json:"checkpointer"`
	GitHubAccount string         `json:"github_account,omitempty"`
	Roles         []choices.Pick `json:"roles"`
	Answers       []SetupAnswer  `json:"answers"`
}

// SetupAnswer is one setup item and its answer.
type SetupAnswer struct {
	Item   string `json:"item"`
	Answer string `json:"answer"`
}

const (
	maxSetupAnswers = 24
	maxSetupItem    = 40  // runes
	maxSetupAnswer  = 400 // runes
)

var ghLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// oneLine refuses text with a control character, a newline included.
func oneLine(s string, most int) error {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > most {
		return fmt.Errorf("must be valid text of at most %d characters", most)
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errors.New("must be one line without control characters")
	}
	return nil
}

func (s RouteSetup) check() error {
	if s.GitHubAccount != "" && !ghLogin.MatchString(s.GitHubAccount) {
		return fmt.Errorf("github account %q is not a GitHub login", s.GitHubAccount)
	}
	if len(s.Roles) > len(choices.Roles) {
		return fmt.Errorf("at most %d roles", len(choices.Roles))
	}
	for _, p := range s.Roles {
		if p.Route != "" || !p.At.IsZero() {
			return errors.New("a setup role has no route or time")
		}
		if err := p.Check(); err != nil {
			return err
		}
	}
	if len(s.Answers) > maxSetupAnswers {
		return fmt.Errorf("at most %d answers", maxSetupAnswers)
	}
	for _, a := range s.Answers {
		if a.Item == "" {
			return errors.New("an answer names no item")
		}
		if err := oneLine(a.Item, maxSetupItem); err != nil {
			return fmt.Errorf("item %w", err)
		}
		if err := oneLine(a.Answer, maxSetupAnswer); err != nil {
			return fmt.Errorf("answer to %s %w", a.Item, err)
		}
	}
	return nil
}

// text is what the orchestrator receives after the request: a heading,
// the effective adjutant and checkpointer, the GitHub account, one line per
// role and one per answer.
func (s RouteSetup) text(req RouteRequest, checkpointer bool) string {
	onOff := map[bool]string{true: "on", false: "off"}
	kind := "custom"
	if s.Recommended {
		kind = "recommended"
	}
	lines := []string{
		"Setup (" + kind + "):",
		"- adjutant: " + onOff[req.Role == "adjutant"],
		"- checkpointer: " + onOff[checkpointer],
	}
	if s.GitHubAccount != "" {
		lines = append(lines, "- GitHub account: "+s.GitHubAccount)
	}
	for _, p := range s.Roles {
		l := fmt.Sprintf("- %s: runner %s, harness %s", p.Role, p.Runner, p.Harness)
		if p.Model != "" {
			l += ", model " + p.Model
		}
		if p.Effort != "" {
			l += ", effort " + p.Effort
		}
		lines = append(lines, l)
	}
	for _, a := range s.Answers {
		ans := a.Answer
		if ans == "" {
			ans = "ask me"
		}
		lines = append(lines, "- "+a.Item+": "+ans)
	}
	return strings.Join(lines, "\n")
}
