package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The catalog of harnesses a new route can start: each harness's models and
// efforts as the harness itself lists them. The commands run with a fixed
// argv and no user input, read-only, under a short timeout, once per server
// life. When a command fails or its output does not parse, the last-known
// list from the checked-in real output under catalogdata/ stands in, marked
// offline; the form, the validation and the launch all use that same list.
// A kind is data: the form and the server treat every kind through its entry.

//go:embed catalogdata
var catalogFiles embed.FS

// catalogTimeout bounds each catalog command.
const catalogTimeout = 5 * time.Second

// modelID is a model name typed by hand: Claude's "Other model id" and the
// free-text model of other kinds. It is always one argv element.
var modelID = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,64}$`)

// CatalogModel is one model of a harness.
type CatalogModel struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
}

// CatalogKind is one harness the form can start.
type CatalogKind struct {
	Kind         string         `json:"kind"`
	Label        string         `json:"label"`
	Models       []CatalogModel `json:"models"`
	DefaultModel string         `json:"default_model"`
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
	// Offline marks the last-known list, used when the harness could not be asked.
	Offline bool `json:"offline"`
}

// Catalog is what GET /api/catalog returns.
type Catalog struct {
	Kinds []CatalogKind `json:"kinds"`
	// Today is the server's date, for the default route name.
	Today string `json:"today"`
	// Home is the server user's home folder, so the form can show "~".
	Home string `json:"home"`
	// CheckpointerOK says whether the checkpointer script was found;
	// CheckpointerWhy says where it was looked for when it was not.
	CheckpointerOK  bool   `json:"checkpointer_ok"`
	CheckpointerWhy string `json:"checkpointer_why,omitempty"`
}

func (c *Catalog) kind(k string) *CatalogKind {
	for i := range c.Kinds {
		if c.Kinds[i].Kind == k {
			return &c.Kinds[i]
		}
	}
	return nil
}

// runner runs one catalog command and returns its standard output.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	return cmd.Output()
}

// catalogSource builds the catalog once and keeps it.
type catalogSource struct {
	run   runner
	once  sync.Once
	kinds []CatalogKind
}

func newCatalogSource(run runner) *catalogSource { return &catalogSource{run: run} }

func (s *catalogSource) get(ctx context.Context) Catalog {
	s.once.Do(func() { s.kinds = s.build(context.WithoutCancel(ctx)) })
	return Catalog{Kinds: slices.Clone(s.kinds)}
}

func (s *catalogSource) build(ctx context.Context) []CatalogKind {
	ask := func(name string, args ...string) ([]byte, error) {
		c, cancel := context.WithTimeout(ctx, catalogTimeout)
		defer cancel()
		return s.run(c, name, args...)
	}
	var (
		wg            sync.WaitGroup
		claude, codex CatalogKind
		kinds         []string
	)
	wg.Go(func() {
		claude = fromOutput(parseClaudeHelp, "claude-help.txt", func() ([]byte, error) { return ask("claude", "--help") })
	})
	wg.Go(func() {
		codex = fromOutput(parseCodexModels, "codex-debug-models.json", func() ([]byte, error) { return ask("codex", "debug", "models") })
	})
	wg.Go(func() {
		out, err := ask("herdr", "agent", "start", "--help")
		if err == nil {
			kinds, err = parseKinds(out)
		}
		if err != nil {
			b, _ := catalogFiles.ReadFile("catalogdata/herdr-agent-start-help.txt")
			kinds, _ = parseKinds(b)
		}
	})
	wg.Wait()
	all := []CatalogKind{claude, codex}
	for _, k := range kinds {
		if k == "claude" || k == "codex" {
			continue
		}
		all = append(all, CatalogKind{Kind: k, Label: kindLabel(k), Models: []CatalogModel{}, FreeModel: true, SkillPrefix: "/"})
	}
	return all
}

// fromOutput parses a command's output, or the checked-in output, offline,
// when the command fails or its output does not parse.
func fromOutput(parse func([]byte) (CatalogKind, error), file string, run func() ([]byte, error)) CatalogKind {
	if out, err := run(); err == nil {
		if k, err := parse(out); err == nil {
			return k
		}
	}
	b, err := catalogFiles.ReadFile("catalogdata/" + file)
	if err != nil {
		panic(err) // embedded at build time
	}
	k, err := parse(b)
	if err != nil {
		panic(fmt.Sprintf("catalogdata/%s does not parse: %v", file, err))
	}
	k.Offline = true
	return k
}

func kindLabel(k string) string {
	r := []rune(k)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// defaultEffort is the default the harness reports when its list has it,
// else none: the user then picks one (A28: no level is preferred by name).
func defaultEffort(efforts []string, def string) string {
	if def != "" && slices.Contains(efforts, def) {
		return def
	}
	return ""
}

var effortWord = regexp.MustCompile(`^[a-z]{1,16}$`)

// parseCodexModels reads `codex debug models`: the listed models, in the
// printed order (the first is the newest), with their efforts.
func parseCodexModels(b []byte) (CatalogKind, error) {
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
		return CatalogKind{}, err
	}
	k := CatalogKind{Kind: "codex", Label: "Codex", Models: []CatalogModel{}, Checkpointer: true, SkillPrefix: "$"}
	for _, m := range out.Models {
		if m.Visibility != "list" {
			continue
		}
		if !modelID.MatchString(m.Slug) {
			return CatalogKind{}, fmt.Errorf("codex model %q has an unexpected name", m.Slug)
		}
		var efforts []string
		for _, l := range m.Levels {
			if !effortWord.MatchString(l.Effort) {
				return CatalogKind{}, fmt.Errorf("codex effort %q has an unexpected name", l.Effort)
			}
			efforts = append(efforts, l.Effort)
		}
		if len(efforts) == 0 {
			return CatalogKind{}, fmt.Errorf("codex model %s lists no efforts", m.Slug)
		}
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		k.Models = append(k.Models, CatalogModel{ID: m.Slug, Label: label, Efforts: efforts, DefaultEffort: defaultEffort(efforts, m.Default)})
	}
	if len(k.Models) == 0 {
		return CatalogKind{}, errors.New("codex lists no models")
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

// parseClaudeHelp reads the model aliases and the efforts from `claude
// --help`. Each alias resolves to the newest model of its family; the first
// is preselected.
func parseClaudeHelp(b []byte) (CatalogKind, error) {
	help := string(b)
	var aliases []string
	for _, m := range quotedAlias.FindAllStringSubmatch(helpOption(help, "--model"), -1) {
		aliases = append(aliases, m[1])
	}
	em := parenList.FindStringSubmatch(helpOption(help, "--effort"))
	if len(aliases) == 0 || em == nil {
		return CatalogKind{}, errors.New("claude --help names no model aliases or efforts")
	}
	efforts := strings.Split(em[1], ", ")
	k := CatalogKind{Kind: "claude", Label: "Claude", Models: []CatalogModel{}, OtherModel: true, Checkpointer: true, SkillPrefix: "/"}
	for _, a := range aliases {
		k.Models = append(k.Models, CatalogModel{ID: a, Label: a, Efforts: efforts, DefaultEffort: defaultEffort(efforts, "")})
	}
	k.DefaultModel = aliases[0]
	return k, nil
}

var possibleValues = regexp.MustCompile(`\[possible values: ([a-z0-9, -]+)\]`)

// parseKinds reads the agent kinds herdr can start from `herdr agent start --help`.
func parseKinds(b []byte) ([]string, error) {
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

// launchArgs builds the agent's argv from the catalog entry: a listed model
// and effort, a validated full id (Claude's Other model id) or a validated
// free-text model for a kind without a list. Nothing else reaches argv.
func launchArgs(k *CatalogKind, model, effort string) ([]string, error) {
	if k == nil {
		return nil, errors.New("unknown harness")
	}
	if k.FreeModel {
		if effort != "" {
			return nil, fmt.Errorf("%s takes no effort here", k.Kind)
		}
		if model == "" {
			return []string{}, nil
		}
		if !modelID.MatchString(model) {
			return nil, fmt.Errorf("the model must match %s", modelID)
		}
		return []string{"--model", model}, nil
	}
	var m *CatalogModel
	for i := range k.Models {
		if k.Models[i].ID == model {
			m = &k.Models[i]
		}
	}
	var efforts []string
	switch {
	case m != nil:
		efforts = m.Efforts
	case k.OtherModel && modelID.MatchString(model) && len(k.Models) > 0:
		efforts = k.Models[0].Efforts
	default:
		return nil, fmt.Errorf("%s has no model %q in its list", k.Label, model)
	}
	if !slices.Contains(efforts, effort) {
		return nil, fmt.Errorf("effort must be one of %s", strings.Join(efforts, ", "))
	}
	if k.Kind == "codex" {
		return []string{"--model", model, "-c", "model_reasoning_effort=" + effort}, nil
	}
	return []string{"--model", model, "--effort", effort}, nil
}

// skillDirs are the folders a harness loads skills from, in lookup order.
func skillDirs(home, kind string) []string {
	agents := filepath.Join(home, ".agents", "skills", "herdsman")
	switch kind {
	case "claude":
		return []string{filepath.Join(home, ".claude", "skills", "herdsman")}
	case "codex":
		return []string{filepath.Join(home, ".codex", "skills", "herdsman"), agents}
	}
	return []string{filepath.Join(home, "."+kind, "skills", "herdsman"), agents}
}

// skillInstalled reports whether the herdsman skill is where the harness
// looks for skills. It installs nothing.
func skillInstalled(home, kind string) bool {
	for _, d := range skillDirs(home, kind) {
		if st, err := os.Stat(filepath.Join(d, "SKILL.md")); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// checkpointerScript finds the skill's checkpointer.sh: under --skill-dir
// when given, else under the installed skill (following its link). It
// returns "" and the reason when there is none. A path with a quote or a
// control character is refused: it goes into a shell line.
func checkpointerScript(home, skillDir string) (string, string) {
	dirs := []string{skillDir}
	if skillDir == "" {
		dirs = []string{filepath.Join(home, ".claude", "skills", "herdsman"), filepath.Join(home, ".codex", "skills", "herdsman"), filepath.Join(home, ".agents", "skills", "herdsman")}
	}
	for _, d := range dirs {
		p := realPath(filepath.Join(d, "scripts", "checkpointer.sh"))
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if strings.ContainsFunc(p, func(r rune) bool { return r == '\'' || unicode.IsControl(r) }) {
			return "", "the checkpointer path " + p + " holds a quote or a control character"
		}
		return p, ""
	}
	if skillDir != "" {
		return "", "no scripts/checkpointer.sh under --skill-dir " + skillDir
	}
	return "", "the herdsman skill's checkpointer.sh was not found: install the skill, pass --skill-dir, or turn the checkpointer off"
}

// catalog serves GET /api/catalog: the cached harness lists plus what
// changes without a restart (the date, the skill and script lookups).
func (s *Server) catalogNow(r *http.Request) Catalog {
	c := s.catalog.get(r.Context())
	home, _ := os.UserHomeDir()
	c.Home = home
	c.Today = s.now().Format("2006-01-02")
	for i := range c.Kinds {
		c.Kinds[i].SkillInstalled = skillInstalled(home, c.Kinds[i].Kind)
	}
	script, why := checkpointerScript(home, s.cfg.SkillDir)
	c.CheckpointerOK, c.CheckpointerWhy = script != "", why
	return c
}

func (s *Server) catalogJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.catalogNow(r))
}
