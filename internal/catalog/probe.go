package catalog

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The harnesses' own model pages, linked so the user can read what each
// maker recommends now (A29 step 2). herdsman does not search them.
const (
	codexDocs  = "https://developers.openai.com/codex/models"
	claudeDocs = "https://docs.claude.com/en/docs/about-claude/models/overview"
)

// Runner runs a program with a fixed argv and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs the program with no standard input.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// A killed program's children may hold its output open: stop waiting
	// for them shortly after the deadline.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// Harness is one probed harness.
//
// JSON fields: kind, version (the first line of `<kind> --version`, or
// "unknown"), docs_url (the harness's model page), models (each with id,
// label, efforts and default_effort, "" when the harness reports none; in
// the harness's order, the newest first where it gives one) and error (why
// the harness could not be probed; then models is null).
type Harness struct {
	Kind    string  `json:"kind"`
	Version string  `json:"version"`
	DocsURL string  `json:"docs_url,omitempty"`
	Models  []Model `json:"models"`
	// For a harness whose help lists no models: whether it takes a model
	// option, the efforts and default its help lists, and where its models
	// come from.
	ModelFlag     bool     `json:"model_flag,omitempty"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	Note          string   `json:"note,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// Probed are the harnesses with a model list to probe.
var Probed = []string{"claude", "codex"}

// Others are other agent kinds herdr starts whose --help says how they
// take a model (kind names, not model names), each with its program: the
// cursor kind's command line program is cursor-agent.
var Others = map[string]string{"muse": "muse", "agy": "agy", "gemini": "gemini", "opencode": "opencode", "cursor": "cursor-agent"}

// OtherKinds are the keys of Others in a fixed order.
var OtherKinds = []string{"muse", "agy", "gemini", "opencode", "cursor"}

var (
	optionStart = regexp.MustCompile(`^\s*(?:-[A-Za-z], )?(--[a-z][a-z-]*)`)
	effortList  = regexp.MustCompile(`([a-z]+(?:\s*[|,]\s*[a-z]+){2,})`)
	effortDef   = regexp.MustCompile(`default:\s*([a-z]+)`)
	subcommand  = regexp.MustCompile(`(?m)^\s+(?:[a-z-]+ )?models\b`)
)

// ParseHelp reads a harness's --help: whether it takes --model, the efforts
// an effort option lists (and its default), and whether it has a models
// subcommand. It names no model: the harness's own list is the source.
func ParseHelp(kind, program string, help []byte) Harness {
	h := Harness{Kind: kind}
	lines := strings.Split(string(help), "\n")
	for i, l := range lines {
		m := optionStart.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if m[1] == "--model" {
			h.ModelFlag = true
		}
		if !strings.Contains(m[1], "effort") || h.Efforts != nil {
			continue
		}
		block := l
		for _, next := range lines[i+1:] {
			if optionStart.MatchString(next) || strings.TrimSpace(next) == "" {
				break
			}
			block += " " + strings.TrimSpace(next)
		}
		if e := effortList.FindString(block); e != "" {
			h.Efforts = strings.FieldsFunc(e, func(r rune) bool { return r == '|' || r == ',' || r == ' ' })
			if d := effortDef.FindStringSubmatch(block); d != nil {
				h.DefaultEffort = DefaultEffort(h.Efforts, d[1])
			}
		}
	}
	h.Note = "models: from the harness's own list"
	if subcommand.Match(help) {
		h.Note += " (" + program + " models)"
	}
	return h
}

// probes are the command and the parser of each probed harness.
var probes = map[string]struct {
	args  []string
	parse func([]byte) (Kind, error)
}{
	"claude": {[]string{"--help"}, ParseClaudeHelp},
	"codex":  {[]string{"debug", "models"}, ParseCodexModels},
}

// version is the first line of `<program> --version`, or "unknown"; it
// runs under the same per-command deadline as every probe.
func version(ctx context.Context, run Runner, program string) string {
	if v, err := ask(ctx, run, program, "--version"); err == nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(v)), "\n"); line != "" {
			return line
		}
	}
	return "unknown"
}
