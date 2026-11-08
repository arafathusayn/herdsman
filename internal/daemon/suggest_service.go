package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/suggest"
)

// ErrNoContextRepo reports a project entry asked for where no context repo exists.
var ErrNoContextRepo = errors.New("this project has no context repo to hold a shared suggestion")

// userEntrySince marks an entry written by a person: it is not tied to a
// release, so any binary may show it.
const userEntrySince = "0.0.0"

// SuggestService serves the suggestion catalog. It reads, ranks and records,
// and has no method that runs a recipe: only the command line can, from
// Run, with a person at a terminal. Its zero value uses the caller's folders;
// the fields exist so that tests can replace them.
type SuggestService struct {
	ConfigDir  string                  // Empty uses XDG_CONFIG_HOME or ~/.config.
	CacheDir   string                  // Empty uses XDG_CACHE_HOME or ~/.cache.
	Home       string                  // Empty uses the user's home folder.
	Harnesses  []string                // Empty looks the known harnesses up on PATH.
	ContextDir func(dir string) string // The project's context repo; "" or nil means none.
}

// SuggestSearchRequest asks for a ranked list. An empty Text gives the top
// entries for this machine.
type SuggestSearchRequest struct {
	Dir  string `json:"dir,omitempty"`
	Text string `json:"text,omitempty"`
	Kind string `json:"kind,omitempty"`
	For  string `json:"for,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// SuggestSearchResponse is a ranking, each item with its reasons, and whatever
// could not be read.
type SuggestSearchResponse struct {
	Dir      string             `json:"dir,omitempty"`
	Items    []suggest.Scored   `json:"items"`
	Excluded []suggest.Excluded `json:"excluded"`
	Problems []suggest.Problem  `json:"problems,omitempty"`
}

// SuggestGetRequest names one entry.
type SuggestGetRequest struct {
	Dir string `json:"dir,omitempty"`
	ID  string `json:"id"`
}

// SuggestGetResponse is the entry in full.
type SuggestGetResponse struct {
	Entry suggest.Entry `json:"entry"`
}

// SuggestAddRequest describes a new freeform entry.
type SuggestAddRequest struct {
	Dir     string   `json:"dir,omitempty"`
	Project bool     `json:"project,omitempty"`
	ID      string   `json:"id,omitempty"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	URL     string   `json:"url,omitempty"`
	Repo    string   `json:"repo,omitempty"`
	Body    string   `json:"body"`
}

// SuggestAddResponse says where the entry was written.
type SuggestAddResponse struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// SuggestPlanRequest names the entry whose recipe is wanted.
type SuggestPlanRequest struct {
	Dir string `json:"dir,omitempty"`
	ID  string `json:"id"`
}

// SuggestPlanResponse is a recipe and whether a person has accepted it.
type SuggestPlanResponse struct {
	Recipe   suggest.Recipe `json:"recipe"`
	Approved bool           `json:"approved"`
}

// SuggestSourcesRequest asks for the remote catalogs.
type SuggestSourcesRequest struct{}

// SuggestSourcesResponse lists the pinned remote catalogs.
type SuggestSourcesResponse struct {
	Sources []suggest.Pin `json:"sources"`
}

// SuggestSourceAddRequest adds a remote catalog; Ref is resolved to a commit.
type SuggestSourceAddRequest struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
}

// SuggestSourceAddResponse is the pin that was stored.
type SuggestSourceAddResponse struct {
	Pin suggest.Pin `json:"pin"`
}

// SuggestSourceRemoveRequest names a remote catalog.
type SuggestSourceRemoveRequest struct {
	URL string `json:"url"`
}

// SuggestSourceRemoveResponse confirms the removal.
type SuggestSourceRemoveResponse struct {
	URL string `json:"url"`
}

// SuggestSourcePreviewRequest asks what moving a pin to Ref would change.
type SuggestSourcePreviewRequest struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
}

// SuggestSourcePinRequest moves a pin. From must still be the stored commit,
// so a pin cannot move to anything but what the caller just saw.
type SuggestSourcePinRequest struct {
	URL  string `json:"url"`
	From string `json:"from"`
	To   string `json:"to"`
}

// SuggestSourcePinResponse is the stored pin.
type SuggestSourcePinResponse struct {
	Pin suggest.Pin `json:"pin"`
}

// SuggestCall serves one verb for the command line in this process.
func SuggestCall(ctx context.Context, method string, request []byte) ([]byte, error) {
	return (&SuggestService{}).Call(ctx, method, request)
}

// SuggestRun applies a recipe for the command line in this process.
func SuggestRun(ctx context.Context, dir, id string, t suggest.Terminal) error {
	return (&SuggestService{}).Run(ctx, dir, id, t)
}

// Call decodes a request, runs the method and encodes its response.
func (s *SuggestService) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case "Search":
		return dispatch(ctx, request, s.Search)
	case "Get":
		return dispatch(ctx, request, s.Get)
	case "Add":
		return dispatch(ctx, request, s.Add)
	case "Plan":
		return dispatch(ctx, request, s.Plan)
	case "Sources":
		return dispatch(ctx, request, s.Sources)
	case "SourceAdd":
		return dispatch(ctx, request, s.SourceAdd)
	case "SourceRemove":
		return dispatch(ctx, request, s.SourceRemove)
	case "SourcePreview":
		return dispatch(ctx, request, s.SourcePreview)
	case "SourcePin":
		return dispatch(ctx, request, s.SourcePin)
	}
	return nil, fmt.Errorf("suggest service: no method %q", method)
}

func (s *SuggestService) folder(field, env, fallback string) string {
	if field != "" {
		return field
	}
	if base := os.Getenv(env); base != "" {
		return base
	}
	return filepath.Join(s.home(), fallback)
}

func (s *SuggestService) home() string {
	if s.Home != "" {
		return s.Home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func (s *SuggestService) configDir() string {
	return filepath.Join(s.folder(s.ConfigDir, "XDG_CONFIG_HOME", ".config"), "herdsman")
}

func (s *SuggestService) cacheDir() string {
	return filepath.Join(s.folder(s.CacheDir, "XDG_CACHE_HOME", ".cache"), "herdsman", "suggest")
}

func (s *SuggestService) sourcesPath() string {
	return filepath.Join(s.configDir(), "suggest-sources.txt")
}

func (s *SuggestService) contextDir(dir string) string {
	if s.ContextDir == nil {
		return ""
	}
	return s.ContextDir(dir)
}

func (s *SuggestService) load(ctx context.Context, dir string) (suggest.Catalog, error) {
	pins, err := s.pins()
	if err != nil {
		return suggest.Catalog{}, err
	}
	sources := []suggest.Source{
		suggest.Bundled(),
		suggest.DirSource(suggest.SourceUser, "user", filepath.Join(s.configDir(), "suggest")),
	}
	if root := s.contextDir(dir); root != "" {
		sources = append(sources, suggest.DirSource(suggest.SourceProject, "project", filepath.Join(root, "suggest")))
	}
	remote := suggest.NewRemote(s.cacheDir())
	for _, pin := range pins {
		sources = append(sources, remote.Source(ctx, pin))
	}
	return suggest.Load(sources...), nil
}

// Search ranks the catalog for this machine and project.
func (s *SuggestService) Search(ctx context.Context, r SuggestSearchRequest) (SuggestSearchResponse, error) {
	catalog, err := s.load(ctx, r.Dir)
	if err != nil {
		return SuggestSearchResponse{}, err
	}
	ranking := suggest.Rank(catalog.Entries, s.machine(r.Dir), suggest.Query{Text: r.Text, Kind: r.Kind, For: r.For, Tag: r.Tag})
	return SuggestSearchResponse{Dir: r.Dir, Items: ranking.Items, Excluded: ranking.Excluded, Problems: catalog.Problems}, nil
}

func (s *SuggestService) find(ctx context.Context, dir, id string) (suggest.Entry, error) {
	catalog, err := s.load(ctx, dir)
	if err != nil {
		return suggest.Entry{}, err
	}
	for _, e := range catalog.Entries {
		if e.ID == id {
			return e, nil
		}
	}
	return suggest.Entry{}, fmt.Errorf("no suggestion has the id %q", id)
}

// Get returns one entry in full.
func (s *SuggestService) Get(ctx context.Context, r SuggestGetRequest) (SuggestGetResponse, error) {
	e, err := s.find(ctx, r.Dir, r.ID)
	return SuggestGetResponse{Entry: e}, err
}

func (s *SuggestService) approvals() *hooks.Approvals {
	return &hooks.Approvals{Path: filepath.Join(s.configDir(), "approvals.json")}
}

// Plan returns what applying an entry would do. It changes nothing.
func (s *SuggestService) Plan(ctx context.Context, r SuggestPlanRequest) (SuggestPlanResponse, error) {
	e, err := s.find(ctx, r.Dir, r.ID)
	if err != nil {
		return SuggestPlanResponse{}, err
	}
	recipe, ok := suggest.RecipeOf(e)
	if !ok {
		return SuggestPlanResponse{}, suggest.ErrNoRecipe
	}
	approved, err := s.approvals().Approved(suggest.ApprovalScope, recipe.Checksum)
	return SuggestPlanResponse{Recipe: recipe, Approved: approved}, err
}

// Run applies a recipe. It is not a service method, so it is reachable only
// from the process a person started, and the terminal check stays with it.
func (s *SuggestService) Run(ctx context.Context, dir, id string, t suggest.Terminal) error {
	e, err := s.find(ctx, dir, id)
	if err != nil {
		return err
	}
	applier := suggest.Applier{Store: s.approvals(), Exec: suggest.ExecRecipe}
	return applier.Run(ctx, e, dir, t)
}

var slugBreaks = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes an id from a title.
func slug(title string) string {
	text := slugBreaks.ReplaceAllString(strings.ToLower(title), "-")
	return strings.Trim(text[:min(len(text), 64)], "-")
}

func quoted(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// renderEntry writes an entry file. A value holding a line break is refused
// first, because it could otherwise end the front matter early.
func renderEntry(e suggest.Entry) ([]byte, error) {
	fields := slices.Concat([]string{e.ID, string(e.Kind), e.Title, e.URL, e.Repo}, e.Tags)
	for _, field := range fields {
		if strings.ContainsAny(field, "\n\r") {
			return nil, fmt.Errorf("the value %q has a line break", field)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nid: %s\nkind: %s\ntitle: %s\n", e.ID, e.Kind, quoted(e.Title))
	tags := make([]string, 0, len(e.Tags))
	for _, tag := range e.Tags {
		tags = append(tags, quoted(tag))
	}
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tags, ", "))
	if e.URL != "" {
		fmt.Fprintf(&b, "url: %s\n", quoted(e.URL))
	}
	if e.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", quoted(e.Repo))
	}
	fmt.Fprintf(&b, "since: %s\n---\n%s\n", e.Since, e.Body)
	return []byte(b.String()), nil
}

// Add writes a new entry to the user's catalog or the project's. The written
// text is parsed back and validated first, so a file that the catalog would
// refuse never lands.
func (s *SuggestService) Add(_ context.Context, r SuggestAddRequest) (SuggestAddResponse, error) {
	folder := filepath.Join(s.configDir(), "suggest")
	if r.Project {
		root := s.contextDir(r.Dir)
		if root == "" {
			return SuggestAddResponse{}, ErrNoContextRepo
		}
		folder = filepath.Join(root, "suggest")
	}
	id := r.ID
	if id == "" {
		id = slug(r.Title)
	}
	entry := suggest.Entry{ID: id, Kind: suggest.Kind(r.Kind), Title: r.Title, Tags: r.Tags, URL: r.URL, Repo: r.Repo, Since: userEntrySince, Body: strings.TrimSpace(r.Body)}
	data, err := renderEntry(entry)
	if err != nil {
		return SuggestAddResponse{}, err
	}
	parsed, err := suggest.ParseEntry(data)
	if err != nil {
		return SuggestAddResponse{}, err
	}
	if err := suggest.Validate(parsed); err != nil {
		return SuggestAddResponse{}, err
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return SuggestAddResponse{}, err
	}
	path := filepath.Join(folder, id+".md")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return SuggestAddResponse{}, err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return SuggestAddResponse{ID: id, Path: path}, err
}

func (s *SuggestService) pins() ([]suggest.Pin, error) {
	data, err := os.ReadFile(s.sourcesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return []suggest.Pin{}, nil
	}
	if err != nil {
		return nil, err
	}
	pins := []suggest.Pin{}
	for _, line := range strings.Fields(string(data)) {
		pin, err := suggest.ParsePin(line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.sourcesPath(), err)
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

func (s *SuggestService) storePins(pins []suggest.Pin) error {
	path := s.sourcesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, pin := range pins {
		b.WriteString(pin.String() + "\n")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".suggest-sources-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = io.WriteString(temp, b.String())
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Sources lists the remote catalogs with their pins.
func (s *SuggestService) Sources(context.Context, SuggestSourcesRequest) (SuggestSourcesResponse, error) {
	pins, err := s.pins()
	return SuggestSourcesResponse{Sources: pins}, err
}

func indexOfPin(pins []suggest.Pin, url string) int {
	return slices.IndexFunc(pins, func(p suggest.Pin) bool { return p.URL == url })
}

// SourceAdd fetches a remote catalog into the cache and pins the commit found.
func (s *SuggestService) SourceAdd(ctx context.Context, r SuggestSourceAddRequest) (SuggestSourceAddResponse, error) {
	pins, err := s.pins()
	if err != nil {
		return SuggestSourceAddResponse{}, err
	}
	if indexOfPin(pins, r.URL) >= 0 {
		return SuggestSourceAddResponse{}, fmt.Errorf("the source %s is already added", r.URL)
	}
	pin, err := suggest.NewRemote(s.cacheDir()).Fetch(ctx, r.URL, r.Ref)
	if err != nil {
		return SuggestSourceAddResponse{}, err
	}
	return SuggestSourceAddResponse{Pin: pin}, s.storePins(append(pins, pin))
}

func (s *SuggestService) pinOf(url string) ([]suggest.Pin, int, error) {
	pins, err := s.pins()
	if err != nil {
		return nil, 0, err
	}
	i := indexOfPin(pins, url)
	if i < 0 {
		return nil, 0, fmt.Errorf("no source has the address %s", url)
	}
	return pins, i, nil
}

// SourceRemove forgets a remote catalog.
func (s *SuggestService) SourceRemove(_ context.Context, r SuggestSourceRemoveRequest) (SuggestSourceRemoveResponse, error) {
	pins, i, err := s.pinOf(r.URL)
	if err != nil {
		return SuggestSourceRemoveResponse{}, err
	}
	return SuggestSourceRemoveResponse(r), s.storePins(slices.Delete(pins, i, i+1))
}

// SourcePreview fetches and returns the entry diff. The pin does not move.
func (s *SuggestService) SourcePreview(ctx context.Context, r SuggestSourcePreviewRequest) (suggest.Update, error) {
	pins, i, err := s.pinOf(r.URL)
	if err != nil {
		return suggest.Update{}, err
	}
	return suggest.NewRemote(s.cacheDir()).PreviewUpdate(ctx, pins[i], r.Ref)
}

// SourcePin moves a pin to a commit the caller has already shown.
func (s *SuggestService) SourcePin(_ context.Context, r SuggestSourcePinRequest) (SuggestSourcePinResponse, error) {
	pins, i, err := s.pinOf(r.URL)
	if err != nil {
		return SuggestSourcePinResponse{}, err
	}
	if pins[i].Commit != r.From {
		return SuggestSourcePinResponse{}, fmt.Errorf("the source %s moved since the preview: it is at %s", r.URL, pins[i].Commit)
	}
	pin, err := suggest.ParsePin(r.URL + "@" + r.To)
	if err != nil {
		return SuggestSourcePinResponse{}, err
	}
	pins[i] = pin
	return SuggestSourcePinResponse{Pin: pin}, s.storePins(pins)
}

// knownHarnesses are the programs looked up on PATH.
var knownHarnesses = []string{"claude", "codex", "opencode"}

// ecosystemFiles maps a marker file to the ecosystem it signals.
var ecosystemFiles = []struct{ file, ecosystem string }{
	{"go.mod", "go"},
	{"package.json", "node"},
	{"Cargo.toml", "rust"},
	{"pyproject.toml", "python"},
}

// machine detects what the ranking depends on. The project's labels and recent
// events stay empty until projects have a record to read them from.
func (s *SuggestService) machine(dir string) suggest.Machine {
	m := suggest.Machine{Platform: runtime.GOOS, Harnesses: s.Harnesses}
	if len(m.Harnesses) == 0 {
		for _, name := range knownHarnesses {
			if _, err := exec.LookPath(name); err == nil {
				m.Harnesses = append(m.Harnesses, name)
			}
		}
	}
	if dir != "" {
		for _, marker := range ecosystemFiles {
			if _, err := os.Stat(filepath.Join(dir, marker.file)); err == nil {
				m.Ecosystems = append(m.Ecosystems, marker.ecosystem)
			}
		}
	}
	m.Installed = s.installed()
	return m
}

var remoteURLLine = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)\s*$`)

// installed reads the skills folder: each folder is an id, and its git origin
// is a repository, so that a skill under another name is still recognized.
func (s *SuggestService) installed() suggest.Installed {
	var found suggest.Installed
	root := filepath.Join(s.home(), ".claude", "skills")
	dirents, err := os.ReadDir(root)
	if err != nil {
		return found
	}
	for _, d := range dirents {
		found.IDs = append(found.IDs, d.Name())
		config, err := os.ReadFile(filepath.Join(root, d.Name(), ".git", "config"))
		if err != nil {
			continue
		}
		if match := remoteURLLine.FindSubmatch(config); match != nil {
			found.Repos = append(found.Repos, string(match[1]))
		}
	}
	return found
}
