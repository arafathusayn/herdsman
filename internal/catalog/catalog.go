// Package catalog reads each harness's models and efforts as the harness
// itself reports them (A28: herdsman names no model and no effort): Codex
// from `codex debug models`, Claude from `claude --help`, and the agent
// kinds herdr can start from `herdr agent start --help`. herdsman web's New
// route form and `herdsman models` share it.
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ModelID is a model name typed by hand: Claude's "Other model id" and the
// free-text model of other kinds. It is always one argv element.
var ModelID = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,64}(/[A-Za-z0-9._:\[\]-]{1,64})?$`)

// Model is one model of a harness.
type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}

// Kind is one harness: its models as it lists them, and what herdsman
// knows about starting it.
type Kind struct {
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	Models       []Model `json:"models"`
	DefaultModel string  `json:"default_model"`
	// OtherModel offers a full model id beside the listed models (Claude).
	OtherModel bool `json:"other_model"`
	// FreeModel means the harness has no list: the model is typed.
	FreeModel bool `json:"free_model"`
	// Checkpointer is whether the checkpointer can read this harness's transcripts.
	Checkpointer bool `json:"checkpointer"`
	// SkillPrefix starts a skill command in this harness: "/herdsman" or "$herdsman".
	SkillPrefix string `json:"skill_prefix"`
	// SkillInstalled is whether the herdsman skill was found for this harness.
	SkillInstalled bool `json:"skill_installed"`
	// Offline marks a harness whose list could not be read: it offers no models.
	Offline bool `json:"offline"`
	// DocsURL is the harness maker's page of its models, for the user to read.
	DocsURL string `json:"docs_url,omitempty"`
	// Version is the first line of the harness's --version, when asked.
	Version string `json:"version,omitempty"`
	// Unavailable says why the harness's list could not be read: then
	// Offline is set and Models is empty (A29).
	Unavailable string `json:"unavailable,omitempty"`
	// EffortFlag is the effort option a harness other than Claude and Codex
	// names in its --help, used to pass the picked effort.
	EffortFlag string `json:"effort_flag,omitempty"`
}

// DefaultEffort is the default the harness reports when its list has it,
// else none: the user then picks one (A28: no level is preferred by name).
func DefaultEffort(efforts []string, def string) string {
	if def != "" && slices.Contains(efforts, def) {
		return def
	}
	return ""
}

var effortWord = regexp.MustCompile(`^[a-z]{1,16}$`)

// ParseCodexModels reads `codex debug models`: the listed models, in the
// printed order (the first is the newest), with their efforts.
func ParseCodexModels(b []byte) (Kind, error) {
	var out struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Default     string `json:"default_reasoning_level"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return Kind{}, err
	}
	k := Kind{Kind: "codex", Label: "Codex", Models: []Model{}, Checkpointer: true, SkillPrefix: "$", DocsURL: codexDocs}
	for _, m := range out.Models {
		if m.Visibility != "list" {
			continue
		}
		if !ModelID.MatchString(m.Slug) {
			return Kind{}, fmt.Errorf("codex model %q has an unexpected name", m.Slug)
		}
		var efforts []string
		for _, l := range m.Levels {
			if !effortWord.MatchString(l.Effort) {
				return Kind{}, fmt.Errorf("codex effort %q has an unexpected name", l.Effort)
			}
			efforts = append(efforts, l.Effort)
		}
		if len(efforts) == 0 {
			return Kind{}, fmt.Errorf("codex model %s lists no efforts", m.Slug)
		}
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		k.Models = append(k.Models, Model{ID: m.Slug, Label: label, Efforts: efforts, DefaultEffort: DefaultEffort(efforts, m.Default)})
	}
	if len(k.Models) == 0 {
		return Kind{}, errors.New("codex lists no models")
	}
	k.DefaultModel = k.Models[0].ID
	return k, nil
}

// helpOption joins an option's description in `--help` output across its
// wrapped lines.
func helpOption(help, name string) string {
	lines := strings.Split(help, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, name+" ") && !strings.Contains(t, ", "+name+" ") {
			continue
		}
		block := []string{t}
		for _, next := range lines[i+1:] {
			nt := strings.TrimSpace(next)
			if nt == "" || strings.HasPrefix(nt, "-") {
				break
			}
			block = append(block, nt)
		}
		return strings.Join(block, " ")
	}
	return ""
}

var (
	quotedAlias = regexp.MustCompile(`'([a-z][a-z0-9.-]*)'`)
	parenList   = regexp.MustCompile(`\(([a-z]+(?:, [a-z]+)+)\)`)
)

// ParseClaudeHelp reads the model aliases and the efforts from `claude
// --help`. Each alias resolves to the newest model of its family; the first
// is preselected.
func ParseClaudeHelp(b []byte) (Kind, error) {
	help := string(b)
	var aliases []string
	for _, m := range quotedAlias.FindAllStringSubmatch(helpOption(help, "--model"), -1) {
		aliases = append(aliases, m[1])
	}
	em := parenList.FindStringSubmatch(helpOption(help, "--effort"))
	if len(aliases) == 0 || em == nil {
		return Kind{}, errors.New("claude --help names no model aliases or efforts")
	}
	efforts := strings.Split(em[1], ", ")
	k := Kind{Kind: "claude", Label: "Claude", Models: []Model{}, OtherModel: true, Checkpointer: true, SkillPrefix: "/", DocsURL: claudeDocs}
	for _, a := range aliases {
		k.Models = append(k.Models, Model{ID: a, Label: a, Efforts: efforts, DefaultEffort: DefaultEffort(efforts, "")})
	}
	k.DefaultModel = aliases[0]
	return k, nil
}

var possibleValues = regexp.MustCompile(`\[possible values: ([a-z0-9, -]+)\]`)

// ParseKinds reads the agent kinds herdr can start from `herdr agent start --help`.
func ParseKinds(b []byte) ([]string, error) {
	m := possibleValues.FindSubmatch(b)
	if m == nil {
		return nil, errors.New("herdr agent start --help lists no kinds")
	}
	var out []string
	for k := range strings.SplitSeq(string(m[1]), ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out, nil
}
