package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/arafathusayn/herdsman/internal/catalog"
	"github.com/arafathusayn/herdsman/internal/choices"
)

// The catalog of harnesses a new route can start: each harness's models and
// efforts as the harness itself lists them. The commands run with a fixed
// argv and no user input, read-only, under a short timeout, once per server
// life (catalog.Pipeline, shared with `herdsman models`). When a command
// fails or its output does not parse, the harness offers no models, marked
// offline with the reason; the form, the validation and the launch all use
// that same entry.
// A kind is data: the form and the server treat every kind through its entry.

// The catalog's types and parsers live in internal/catalog, which
// `herdsman models` shares; these names keep this package's code as it was.
type (
	CatalogModel = catalog.Model
	CatalogKind  = catalog.Kind
)

var (
	modelID          = catalog.ModelID
	defaultEffort    = catalog.DefaultEffort
	parseCodexModels = catalog.ParseCodexModels
	parseClaudeHelp  = catalog.ParseClaudeHelp
	parseKinds       = catalog.ParseKinds
)

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
	// Previous is the last pick of each role (A30), for "Use previous
	// choice"; empty when none was saved or the store cannot be read.
	Previous []choices.Pick `json:"previous"`
	// FromPicks marks a catalog of the saved picks alone (?probe=0): no
	// harness was asked.
	FromPicks bool `json:"from_picks,omitempty"`
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
	built atomic.Bool // the probe has finished
}

func newCatalogSource(run runner) *catalogSource { return &catalogSource{run: run} }

func (s *catalogSource) get(ctx context.Context) Catalog {
	s.once.Do(func() {
		s.kinds = s.build(context.WithoutCancel(ctx))
		s.built.Store(true)
	})
	return Catalog{Kinds: slices.Clone(s.kinds)}
}

func (s *catalogSource) build(ctx context.Context) []CatalogKind {
	// The probe `herdsman models` runs too (A29): herdr's installed kinds,
	// each with its own list, or offline with none and the reason.
	return catalog.Pipeline(ctx, catalog.Runner(s.run), "")
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
	if len(k.Models) == 0 && !k.OtherModel {
		// No list was read: the harness starts with its own default model.
		if model != "" || effort != "" {
			return nil, fmt.Errorf("%s lists no models here, so none can be picked", k.Label)
		}
		return []string{}, nil
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
	if len(efforts) == 0 && k.Kind != "claude" && k.Kind != "codex" {
		if effort != "" {
			return nil, fmt.Errorf("%s lists no efforts", k.Label)
		}
		return []string{"--model", model}, nil
	}
	if !slices.Contains(efforts, effort) {
		return nil, fmt.Errorf("effort must be one of %s", strings.Join(efforts, ", "))
	}
	switch k.Kind {
	case "claude":
	case "codex":
		return []string{"--model", model, "-c", "model_reasoning_effort=" + effort}, nil
	default:
		if k.EffortFlag == "" {
			return nil, fmt.Errorf("%s names no effort option", k.Label)
		}
		return []string{"--model", model, k.EffortFlag, effort}, nil
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
	var c Catalog
	if r.URL.Query().Get("probe") == "0" {
		c = Catalog{Kinds: pickKinds(s.savedPicks()), FromPicks: true}
	} else {
		c = s.catalog.get(r.Context())
	}
	home, _ := os.UserHomeDir()
	c.Home = home
	c.Today = s.now().Format("2006-01-02")
	for i := range c.Kinds {
		c.Kinds[i].SkillInstalled = skillInstalled(home, c.Kinds[i].Kind)
	}
	script, why := checkpointerScript(home, s.cfg.SkillDir)
	c.CheckpointerOK, c.CheckpointerWhy = script != "", why
	c.Previous = s.savedPicks()
	return c
}

// pickKinds is a catalog of the saved Herdr picks alone (A30): each picked
// harness offers its picked models with their picked efforts, so "Use
// previous choice" starts at once; nothing is probed.
func pickKinds(picks []choices.Pick) []CatalogKind {
	out := []CatalogKind{}
	at := map[string]int{}
	for _, p := range picks {
		if p.Runner != "herdr" || p.Model == "" {
			continue
		}
		i, ok := at[p.Harness]
		if !ok {
			k := CatalogKind{Kind: p.Harness, Label: p.Harness, Models: []CatalogModel{}, SkillPrefix: "/", DefaultModel: p.Model}
			k.Checkpointer = p.Harness == "claude" || p.Harness == "codex"
			if p.Harness == "codex" {
				k.SkillPrefix = "$"
			}
			i, at[p.Harness] = len(out), len(out)
			out = append(out, k)
		}
		m := CatalogModel{ID: p.Model, Label: p.Model, Efforts: []string{}, DefaultEffort: p.Effort}
		if p.Effort != "" {
			m.Efforts = []string{p.Effort}
		}
		out[i].Models = append(out[i].Models, m)
	}
	return out
}

// planCatalog is the catalog a new route is checked against: the saved
// picks' catalog while the probe has not finished and every coordinator
// of the request is exactly a saved pick (A30), else the probed one.
func (s *Server) planCatalog(r *http.Request, req RouteRequest) Catalog {
	if !s.catalog.built.Load() {
		picks := s.savedPicks()
		is := func(role, kind, model, effort string) bool {
			for _, p := range picks {
				if p.Role == role && p.Runner == "herdr" && p.Harness == kind && p.Model == model && p.Effort == effort {
					return true
				}
			}
			return false
		}
		ok := is(req.Role, req.Kind, req.Model, req.Effort)
		if req.Role == "adjutant" {
			ok = ok && is("orchestrator", req.OrchKind, req.OrchModel, req.OrchEffort)
		}
		if ok {
			return Catalog{Kinds: pickKinds(picks), FromPicks: true}
		}
	}
	return s.catalog.get(r.Context())
}

// savedPicks is each role's last pick (A30), read without asking any
// harness; empty when there is no store or it cannot be read.
func (s *Server) savedPicks() []choices.Pick {
	if s.cfg.Choices != nil {
		if f, err := s.cfg.Choices.Load(); err == nil && f.Picks != nil {
			return f.Picks
		}
	}
	return []choices.Pick{}
}

// choicesJSON answers GET /api/choices: the saved picks, runner included,
// before any probe, so the form can ask "Use previous choice?" at once.
func (s *Server) choicesJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, choices.File{Picks: s.savedPicks()})
}

func (s *Server) catalogJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.catalogNow(r))
}
