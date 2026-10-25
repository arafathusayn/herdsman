package catalog

import (
	"context"
	"errors"
	"os/exec"
	"strings"
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
	Error   string  `json:"error,omitempty"`
}

// Probed are the harnesses with a model list to probe.
var Probed = []string{"claude", "codex"}

// probes are the command and the parser of each probed harness.
var probes = map[string]struct {
	args  []string
	parse func([]byte) (Kind, error)
}{
	"claude": {[]string{"--help"}, ParseClaudeHelp},
	"codex":  {[]string{"debug", "models"}, ParseCodexModels},
}

// Probe asks one harness for its version and its models.
func Probe(ctx context.Context, run Runner, kind string) Harness {
	h := Harness{Kind: kind}
	p, ok := probes[kind]
	if !ok {
		h.Error = "no model list to probe"
		return h
	}
	cmd := kind + " " + strings.Join(p.args, " ")
	out, err := run(ctx, kind, p.args...)
	if errors.Is(err, exec.ErrNotFound) {
		h.Error = "not found on PATH"
		return h
	}
	if err != nil {
		h.Error = cmd + ": " + err.Error()
		return h
	}
	k, err := p.parse(out)
	if err != nil {
		h.Error = cmd + ": " + err.Error()
		return h
	}
	h.Version = "unknown"
	if v, err := run(ctx, kind, "--version"); err == nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(v)), "\n"); line != "" {
			h.Version = line
		}
	}
	h.DocsURL, h.Models = k.DocsURL, k.Models
	return h
}
