package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// catalogSource builds the catalog once and keeps it.
type catalogSource struct {
	run   runner
	once  sync.Once
	kinds []CatalogKind
}

func newCatalogSource(run runner) *catalogSource { return &catalogSource{run: run} }

func (s *catalogSource) get(ctx context.Context) Catalog {
	s.once.Do(func() {
		s.kinds = s.build(context.WithoutCancel(ctx))
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
	if k.Saved && model == "" && effort == "" {
		return []string{}, nil // a saved pick of the harness's own default
	}
	if len(k.Models) == 0 && !k.OtherModel {
		// A29: no list was read, so there is no model and effort to choose.
		return nil, fmt.Errorf("%s's model list could not be read: pick a harness whose list was read", k.Label)
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
	if len(efforts) == 0 && (k.Saved || (k.Kind != "claude" && k.Kind != "codex")) {
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

// checkpointerCommand is the program a new route's checkpointer pane runs:
// this herdsman binary (exe, the running binary with links resolved),
// started as `<exe> checkpointer`. It returns "" and the reason when there
// is none. A path with a quote or a control character is refused: it goes
// into a shell line.
func checkpointerCommand(exe string) (string, string) {
	if exe == "" {
		return "", "the herdsman binary was not found: turn the checkpointer off"
	}
	if strings.ContainsFunc(exe, func(r rune) bool { return r == '\'' || unicode.IsControl(r) }) {
		return "", "the herdsman path " + exe + " holds a quote or a control character"
	}
	return exe, ""
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
	exe, why := checkpointerCommand(s.cfg.Executable)
	c.CheckpointerOK, c.CheckpointerWhy = exe != "", why
	c.Previous = s.savedPicks()
	return c
}

// pickKinds is a catalog of the saved Herdr picks alone (A30): each picked
// harness offers each picked model once, with every effort a saved role
// chose for it, and the effort option a saved pick names; nothing is
// probed. A saved pick without a model (the harness's own default) keeps
// its harness, with no model to pick.
func pickKinds(picks []choices.Pick) []CatalogKind {
	out := []CatalogKind{}
	at := map[string]int{}
	for _, p := range picks {
		if p.Runner != "herdr" || !usable(p) {
			continue // an unusable pick is asked again
		}
		i, ok := at[p.Harness]
		if !ok {
			k := CatalogKind{Kind: p.Harness, Label: p.Harness, Models: []CatalogModel{}, SkillPrefix: "/", Saved: true}
			k.Checkpointer = p.Harness == "claude" || p.Harness == "codex"
			if p.Harness == "codex" {
				k.SkillPrefix = "$"
			}
			i, at[p.Harness] = len(out), len(out)
			out = append(out, k)
		}
		k := &out[i]
		if p.EffortFlag != "" {
			k.EffortFlag = p.EffortFlag
		}
		if p.Model == "" {
			continue
		}
		if k.DefaultModel == "" {
			k.DefaultModel = p.Model
		}
		j := slices.IndexFunc(k.Models, func(m CatalogModel) bool { return m.ID == p.Model })
		if j < 0 {
			k.Models = append(k.Models, CatalogModel{ID: p.Model, Label: p.Model, Efforts: []string{}, DefaultEffort: p.Effort})
			j = len(k.Models) - 1
		}
		if p.Effort != "" && !slices.Contains(k.Models[j].Efforts, p.Effort) {
			k.Models[j].Efforts = append(k.Models[j].Efforts, p.Effort)
		}
	}
	return out
}

// roleCatalogs are the catalogs a new route's coordinators are checked
// against: main for the request's own role, orch for the orchestrator of
// an adjutant route (the same as main on an orchestrator route).
type roleCatalogs struct{ main, orch Catalog }

// planCatalogs gives each coordinator its own catalog (A30, per role): its
// own saved pick's entry when that role is exactly its usable saved pick, so
// the previous choice never waits for or depends on a probe; else the
// probed one, built only when a role needs it.
func (s *Server) planCatalogs(r *http.Request, req RouteRequest) roleCatalogs {
	picks := s.savedPicks()
	var probed *Catalog
	of := func(role, kind, model, effort string) Catalog {
		for _, p := range picks {
			if p.Role == role && p.Runner == "herdr" && usable(p) && p.Harness == kind && p.Model == model && p.Effort == effort {
				// That role's own pick alone: another role's pick of the
				// same harness adds nothing to it (each role is its own unit).
				return Catalog{Kinds: pickKinds([]choices.Pick{p}), FromPicks: true}
			}
		}
		if probed == nil {
			c := s.catalog.get(r.Context())
			probed = &c
		}
		return *probed
	}
	if req.Role == "adjutant" {
		return roleCatalogs{main: of("adjutant", req.Kind, req.Model, req.Effort), orch: of("orchestrator", req.OrchKind, req.OrchModel, req.OrchEffort)}
	}
	c := of("orchestrator", req.Kind, req.Model, req.Effort)
	return roleCatalogs{main: c, orch: c}
}

// usable says whether a saved pick can start without a probe: an effort of
// a harness other than Claude and Codex needs the option that passes it.
func usable(p choices.Pick) bool {
	return p.Effort == "" || p.EffortFlag != "" || p.Harness == "claude" || p.Harness == "codex"
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
