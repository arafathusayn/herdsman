package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/arafathusayn/herdsman/internal/approvals"
	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/suggest"
	"github.com/arafathusayn/herdsman/sdk/config"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
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
	// Labels gives the labels of the project dir belongs to; nil means none.
	Labels func(dir string) []string

	pinsMu     sync.Mutex // Held from reading the pins to storing them.
	remoteOnce sync.Once
	fetcher    *suggest.Remote // One, because it serializes fetches of its cache.
}

// suggestRefusal is a failure the caller caused, with the code it is served as.
// The command line prints its text alone.
type suggestRefusal struct {
	code rpc.Code
	text string
}

func (r *suggestRefusal) Error() string { return r.text }

func refuseSuggest(code rpc.Code, format string, args ...any) error {
	return &suggestRefusal{code: code, text: fmt.Sprintf(format, args...)}
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
type SuggestSourcesRequest struct {
	Dir string `json:"dir,omitempty"`
}

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

// SuggestRun applies a recipe for the command line in this process.
func SuggestRun(ctx context.Context, dir, id string, t suggest.Terminal) error {
	return productionSuggest(nil).Run(ctx, dir, id, t)
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

func (s *SuggestService) remote() *suggest.Remote {
	s.remoteOnce.Do(func() { s.fetcher = suggest.NewRemote(s.cacheDir()) })
	return s.fetcher
}

func (s *SuggestService) contextDir(dir string) string {
	if s.ContextDir == nil {
		return ""
	}
	return s.ContextDir(dir)
}

func (s *SuggestService) load(ctx context.Context, dir string) (suggest.Catalog, error) {
	pins, err := s.pins(dir)
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
	remote := s.remote()
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
	return suggest.Entry{}, refuseSuggest(rpc.NotFound, "no suggestion has the id %q", id)
}

// Get returns one entry in full.
func (s *SuggestService) Get(ctx context.Context, r SuggestGetRequest) (SuggestGetResponse, error) {
	e, err := s.find(ctx, r.Dir, r.ID)
	return SuggestGetResponse{Entry: e}, err
}

func (s *SuggestService) approvals() *hooks.Approvals {
	if s.ConfigDir != "" {
		return &hooks.Approvals{Path: filepath.Join(s.configDir(), approvals.FileName)}
	}
	store, err := approvals.Default(os.Environ())
	if err != nil {
		return &hooks.Approvals{Path: filepath.Join(s.configDir(), approvals.FileName)}
	}
	return &hooks.Approvals{Path: store.Path()}
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

// Add writes a new entry to the user's catalog or the project's. The id's
// pattern keeps the file inside the folder, and the text is parsed back first,
// so a file that the catalog would refuse never lands.
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
	// The values as given are what names the file, so they are what must pass:
	// a value can read as valid once parsed, with the rest of it a comment.
	if err := suggest.Validate(entry); err != nil {
		return SuggestAddResponse{}, refuseSuggest(rpc.InvalidArgument, "%s", err)
	}
	data, err := renderEntry(entry)
	if err != nil {
		return SuggestAddResponse{}, refuseSuggest(rpc.InvalidArgument, "%s", err)
	}
	if _, err := suggest.ParseEntry(data); err != nil {
		return SuggestAddResponse{}, refuseSuggest(rpc.InvalidArgument, "%s", err)
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

// legacyPins reads the file pins were kept in before the configuration held them.
func (s *SuggestService) legacyPins() ([]suggest.Pin, error) {
	data, err := os.ReadFile(s.sourcesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pins []suggest.Pin
	for _, line := range strings.Fields(string(data)) {
		pin, err := suggest.ParsePin(line)
		if err != nil {
			// Not %w: a damaged file here is the machine's fault, whereas the
			// error's type would blame the request.
			return nil, fmt.Errorf("%s: %s", s.sourcesPath(), err)
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

// userConfigFolder is where the user's configuration file lives.
func (s *SuggestService) userConfigFolder() string {
	if s.ConfigDir != "" {
		return s.configDir()
	}
	return config.UserDir(os.Environ())
}

// parseConfiguredPins reads [suggest].sources. A bad entry is the machine's
// fault, as in legacyPins, so its error type does not blame the request.
func parseConfiguredPins(sources []string) ([]suggest.Pin, error) {
	pins := make([]suggest.Pin, 0, len(sources))
	for _, source := range sources {
		pin, err := suggest.ParsePin(source)
		if err != nil {
			return nil, fmt.Errorf("suggest.sources: %s", err)
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

// mergePins keeps the first pin of each address, so the stronger source wins.
func mergePins(groups ...[]suggest.Pin) []suggest.Pin {
	merged := []suggest.Pin{}
	for _, group := range groups {
		for _, pin := range group {
			if indexOfPin(merged, pin.URL) < 0 {
				merged = append(merged, pin)
			}
		}
	}
	return merged
}

// repositoryRoot walks up from dir to the folder holding .git; "" when none.
func repositoryRoot(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	for at := filepath.Clean(dir); ; at = filepath.Dir(at) {
		if _, err := os.Lstat(filepath.Join(at, ".git")); err == nil {
			return at
		}
		if at == filepath.Dir(at) {
			return ""
		}
	}
}

// pins are the effective remote catalogs for dir: the configuration in its
// usual precedence, then any left in the older file.
func (s *SuggestService) pins(dir string) ([]suggest.Pin, error) {
	result, err := config.Load(config.Options{RepoRoot: repositoryRoot(dir), UserDir: s.userConfigFolder()})
	if err != nil {
		return nil, err
	}
	configured, err := parseConfiguredPins(result.Config.GetSuggest().GetSources())
	if err != nil {
		return nil, err
	}
	legacy, err := s.legacyPins()
	if err != nil {
		return nil, err
	}
	return mergePins(configured, legacy), nil
}

// userFile is the user's configuration file, the one a change to the pins goes
// to: the existing file in whatever format it has, or a new TOML file.
func (s *SuggestService) userFile() (string, error) {
	result, err := config.Load(config.Options{UserDir: s.userConfigFolder()})
	if err != nil {
		return "", err
	}
	if result.UserPath != "" {
		return result.UserPath, nil
	}
	return filepath.Join(s.userConfigFolder(), "config.toml"), nil
}

// readUserConfig is replaced only by the test of a file that changes while it
// is being read.
var readUserConfig = config.ReadFile

// changeablePins are the pins a person can change from here: their own file's
// and the older file's. A repository's pins change by editing that repository.
func (s *SuggestService) changeablePins() ([]suggest.Pin, error) {
	path, err := s.userFile()
	if err != nil {
		return nil, err
	}
	var own []suggest.Pin
	if _, statErr := os.Stat(path); statErr == nil {
		result, err := readUserConfig(path)
		if err != nil {
			return nil, err
		}
		if own, err = parseConfiguredPins(result.Config.GetSuggest().GetSources()); err != nil {
			return nil, err
		}
	}
	legacy, err := s.legacyPins()
	if err != nil {
		return nil, err
	}
	return mergePins(own, legacy), nil
}

// storePins writes the pins to the user's configuration and only then retires
// the older file, so a failure leaves every pin where it was.
func (s *SuggestService) storePins(pins []suggest.Pin) error {
	path, err := s.userFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return err
		}
	}
	texts := make([]string, 0, len(pins))
	for _, pin := range pins {
		// The configuration's text form of a list is comma separated.
		if strings.Contains(pin.String(), ",") {
			return refuseSuggest(rpc.InvalidArgument, "the address %s has a comma, which a configured source cannot hold", pin.URL)
		}
		texts = append(texts, pin.String())
	}
	if len(texts) == 0 {
		_, err = config.UnsetFile(path, sourcesKey)
	} else {
		err = config.SetFile(path, sourcesKey, strings.Join(texts, ","))
	}
	if err != nil {
		return err
	}
	if err := os.Remove(s.sourcesPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Sources lists the remote catalogs with their pins.
func (s *SuggestService) Sources(_ context.Context, r SuggestSourcesRequest) (SuggestSourcesResponse, error) {
	pins, err := s.pins(r.Dir)
	return SuggestSourcesResponse{Sources: pins}, err
}

func indexOfPin(pins []suggest.Pin, url string) int {
	return slices.IndexFunc(pins, func(p suggest.Pin) bool { return p.URL == url })
}

// changePins runs one read, change and store of the pins as a unit, so that two
// calls at once cannot lose each other's change.
func (s *SuggestService) changePins(change func([]suggest.Pin) ([]suggest.Pin, error)) error {
	s.pinsMu.Lock()
	defer s.pinsMu.Unlock()
	pins, err := s.changeablePins()
	if err != nil {
		return err
	}
	changed, err := change(pins)
	if err != nil {
		return err
	}
	return s.storePins(changed)
}

func sourceExists(url string) error {
	return refuseSuggest(rpc.AlreadyExists, "the source %s is already added", url)
}

func findPin(pins []suggest.Pin, url string) (int, error) {
	i := indexOfPin(pins, url)
	if i < 0 {
		return 0, refuseSuggest(rpc.NotFound, "no source has the address %s", url)
	}
	return i, nil
}

// SourceAdd fetches a remote catalog into the cache and pins the commit found.
// The fetch can be slow, so it happens outside the unit that stores the pin,
// which checks for the source again.
func (s *SuggestService) SourceAdd(ctx context.Context, r SuggestSourceAddRequest) (SuggestSourceAddResponse, error) {
	pins, err := s.changeablePins()
	if err != nil {
		return SuggestSourceAddResponse{}, err
	}
	if indexOfPin(pins, r.URL) >= 0 {
		return SuggestSourceAddResponse{}, sourceExists(r.URL)
	}
	pin, err := s.remote().Fetch(ctx, r.URL, r.Ref)
	if err != nil {
		return SuggestSourceAddResponse{}, err
	}
	err = s.changePins(func(current []suggest.Pin) ([]suggest.Pin, error) {
		if indexOfPin(current, r.URL) >= 0 {
			return nil, sourceExists(r.URL)
		}
		return append(current, pin), nil
	})
	return SuggestSourceAddResponse{Pin: pin}, err
}

// SourceRemove forgets a remote catalog.
func (s *SuggestService) SourceRemove(_ context.Context, r SuggestSourceRemoveRequest) (SuggestSourceRemoveResponse, error) {
	err := s.changePins(func(current []suggest.Pin) ([]suggest.Pin, error) {
		i, err := findPin(current, r.URL)
		if err != nil {
			return nil, err
		}
		return slices.Delete(current, i, i+1), nil
	})
	if err != nil {
		return SuggestSourceRemoveResponse{}, err
	}
	return SuggestSourceRemoveResponse(r), nil
}

// SourcePreview fetches and returns the entry diff. The pin does not move.
func (s *SuggestService) SourcePreview(ctx context.Context, r SuggestSourcePreviewRequest) (suggest.Update, error) {
	pins, err := s.changeablePins()
	if err != nil {
		return suggest.Update{}, err
	}
	i, err := findPin(pins, r.URL)
	if err != nil {
		return suggest.Update{}, err
	}
	return s.remote().PreviewUpdate(ctx, pins[i], r.Ref)
}

// SourcePin moves a pin to a commit the caller has already shown.
func (s *SuggestService) SourcePin(_ context.Context, r SuggestSourcePinRequest) (SuggestSourcePinResponse, error) {
	var pin suggest.Pin
	err := s.changePins(func(current []suggest.Pin) ([]suggest.Pin, error) {
		i, err := findPin(current, r.URL)
		if err != nil {
			return nil, err
		}
		if current[i].Commit != r.From {
			return nil, refuseSuggest(rpc.Aborted, "the source %s moved since the preview: it is at %s", r.URL, current[i].Commit)
		}
		if pin, err = suggest.ParsePin(r.URL + "@" + r.To); err != nil {
			return nil, err
		}
		current[i] = pin
		return current, nil
	})
	if err != nil {
		return SuggestSourcePinResponse{}, err
	}
	return SuggestSourcePinResponse{Pin: pin}, nil
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

// machine detects what the ranking depends on.
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
	if s.Labels != nil {
		m.Labels = s.Labels(dir)
	}
	if root := s.contextDir(dir); root != "" {
		m.RecentEvents = recentEventKinds(root)
	}
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

// suggestRPC serves the catalog on the daemon's RPC surface. It translates
// messages and failures and holds no behaviour of its own.
type suggestRPC struct {
	s       *SuggestService
	inspect bool // A daemon in inspect mode changes nothing.
}

// refuseInInspect guards every method that writes, the cache included. The
// daemon's own guard covers its 0.x routes and not this surface, where every
// call is a POST, so each writing method asks here and reads stay served.
func (r *suggestRPC) refuseInInspect(method string) error {
	if !r.inspect {
		return nil
	}
	return rpc.Errorf(rpc.FailedPrecondition, "this daemon runs in inspect mode and changes nothing; %s was refused", method)
}

var _ suggestv1.SuggestService = (*suggestRPC)(nil)

// NewSuggestService returns the catalog service for a caller in this process.
func NewSuggestService() suggestv1.SuggestService {
	return &suggestRPC{s: productionSuggest(nil)}
}

// suggestFailure is how a failed call is served. The caller's own deadline or
// cancellation wins over whatever it caused.
func suggestFailure(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var refusal *suggestRefusal
	var bad *suggest.BadSourceError
	switch {
	case errors.As(err, &refusal):
		return rpc.Errorf(refusal.code, "%s", refusal.text)
	case errors.As(err, &bad):
		return rpc.Errorf(rpc.InvalidArgument, "%s", bad.Reason)
	case errors.Is(err, suggest.ErrNoRecipe), errors.Is(err, ErrNoContextRepo):
		return rpc.Errorf(rpc.FailedPrecondition, "%s", err)
	case errors.Is(err, fs.ErrExist):
		return rpc.Errorf(rpc.AlreadyExists, "%s", err)
	}
	return rpc.Errorf(rpc.Internal, "%s", err)
}

func sourceToProto(ref suggest.SourceRef) *suggestv1.SourceRef {
	return &suggestv1.SourceRef{Kind: string(ref.Kind), Name: ref.Name, Commit: ref.Commit}
}

func pinToProto(pin suggest.Pin) *suggestv1.Pin {
	return &suggestv1.Pin{Url: pin.URL, Commit: pin.Commit}
}

func reasonToProto(reason suggest.Reason) *suggestv1.Reason {
	return &suggestv1.Reason{Code: reason.Code, Value: reason.Value, Points: int32(reason.Points)}
}

func problemsToProto(problems []suggest.Problem) []*suggestv1.Problem {
	out := make([]*suggestv1.Problem, 0, len(problems))
	for _, problem := range problems {
		out = append(out, &suggestv1.Problem{Source: sourceToProto(problem.Source), File: problem.File, Error: problem.Err})
	}
	return out
}

func entryToProto(e suggest.Entry) *suggestv1.Suggestion {
	out := &suggestv1.Suggestion{
		Id: e.ID, Kind: string(e.Kind), Title: e.Title, Tags: e.Tags, Url: e.URL, Repo: e.Repo, Since: e.Since, Body: e.Body,
		AppliesTo: &suggestv1.AppliesTo{Harnesses: e.AppliesTo.Harnesses, Ecosystems: e.AppliesTo.Ecosystems, Platforms: e.AppliesTo.Platforms},
		Source:    sourceToProto(e.Source),
	}
	if e.Install != nil {
		out.Install = &suggestv1.Install{Command: e.Install.Command, Mcp: e.Install.MCP, SkillRepo: e.Install.SkillRepo, Touches: e.Install.Touches}
	}
	return out
}

// Search serves a ranking, each item with its reasons.
func (r *suggestRPC) Search(ctx context.Context, req *suggestv1.SearchRequest) (*suggestv1.SearchResponse, error) {
	found, err := r.s.Search(ctx, SuggestSearchRequest{Dir: req.GetDir(), Text: req.GetText(), Kind: req.GetKind(), For: req.GetFor(), Tag: req.GetTag()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	res := &suggestv1.SearchResponse{Dir: found.Dir, Problems: problemsToProto(found.Problems)}
	for _, item := range found.Items {
		scored := &suggestv1.Scored{Entry: entryToProto(item.Entry), Score: int32(item.Score)}
		for _, reason := range item.Reasons {
			scored.Reasons = append(scored.Reasons, reasonToProto(reason))
		}
		res.Items = append(res.Items, scored)
	}
	for _, left := range found.Excluded {
		res.Excluded = append(res.Excluded, &suggestv1.Excluded{Id: left.ID, Reason: reasonToProto(left.Reason)})
	}
	return res, nil
}

// Get serves one entry in full.
func (r *suggestRPC) Get(ctx context.Context, req *suggestv1.GetRequest) (*suggestv1.GetResponse, error) {
	if req.GetId() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no suggestion")
	}
	got, err := r.s.Get(ctx, SuggestGetRequest{Dir: req.GetDir(), ID: req.GetId()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	return &suggestv1.GetResponse{Entry: entryToProto(got.Entry)}, nil
}

// Add records a new entry.
func (r *suggestRPC) Add(ctx context.Context, req *suggestv1.AddRequest) (*suggestv1.AddResponse, error) {
	if err := r.refuseInInspect("Add"); err != nil {
		return nil, err
	}
	added, err := r.s.Add(ctx, SuggestAddRequest{Dir: req.GetDir(), Project: req.GetProject(), ID: req.GetId(), Kind: req.GetKind(), Title: req.GetTitle(), Tags: req.GetTags(), URL: req.GetUrl(), Repo: req.GetRepo(), Body: req.GetBody()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	return &suggestv1.AddResponse{Id: added.ID, Path: added.Path}, nil
}

// Plan serves what applying an entry would do.
func (r *suggestRPC) Plan(ctx context.Context, req *suggestv1.PlanRequest) (*suggestv1.PlanResponse, error) {
	if req.GetId() == "" {
		return nil, rpc.Errorf(rpc.InvalidArgument, "the request names no suggestion")
	}
	plan, err := r.s.Plan(ctx, SuggestPlanRequest{Dir: req.GetDir(), ID: req.GetId()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	recipe := plan.Recipe
	return &suggestv1.PlanResponse{Approved: plan.Approved, Recipe: &suggestv1.Recipe{
		Id: recipe.ID, Title: recipe.Title, Source: sourceToProto(recipe.Source), Kind: string(recipe.Kind),
		Text: recipe.Text, Touches: recipe.Touches, Checksum: recipe.Checksum,
	}}, nil
}

// Sources serves the remote catalogs with their pins.
func (r *suggestRPC) Sources(ctx context.Context, req *suggestv1.SourcesRequest) (*suggestv1.SourcesResponse, error) {
	listed, err := r.s.Sources(ctx, SuggestSourcesRequest{Dir: req.GetDir()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	res := &suggestv1.SourcesResponse{}
	for _, pin := range listed.Sources {
		res.Sources = append(res.Sources, pinToProto(pin))
	}
	return res, nil
}

// SourceAdd pins a new remote catalog.
func (r *suggestRPC) SourceAdd(ctx context.Context, req *suggestv1.SourceAddRequest) (*suggestv1.SourceAddResponse, error) {
	if err := r.refuseInInspect("SourceAdd"); err != nil {
		return nil, err
	}
	added, err := r.s.SourceAdd(ctx, SuggestSourceAddRequest{URL: req.GetUrl(), Ref: req.GetRef()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	return &suggestv1.SourceAddResponse{Pin: pinToProto(added.Pin)}, nil
}

// SourceRemove forgets a remote catalog.
func (r *suggestRPC) SourceRemove(ctx context.Context, req *suggestv1.SourceRemoveRequest) (*suggestv1.SourceRemoveResponse, error) {
	if err := r.refuseInInspect("SourceRemove"); err != nil {
		return nil, err
	}
	removed, err := r.s.SourceRemove(ctx, SuggestSourceRemoveRequest{URL: req.GetUrl()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	return &suggestv1.SourceRemoveResponse{Url: removed.URL}, nil
}

// SourcePreview serves what moving a pin would change.
func (r *suggestRPC) SourcePreview(ctx context.Context, req *suggestv1.SourcePreviewRequest) (*suggestv1.SourcePreviewResponse, error) {
	if err := r.refuseInInspect("SourcePreview"); err != nil {
		return nil, err
	}
	update, err := r.s.SourcePreview(ctx, SuggestSourcePreviewRequest{URL: req.GetUrl(), Ref: req.GetRef()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	res := &suggestv1.SourcePreviewResponse{From: pinToProto(update.From), To: pinToProto(update.To), Problems: problemsToProto(update.Problems)}
	for _, change := range update.Changes {
		res.Changes = append(res.Changes, &suggestv1.Change{Id: change.ID, Action: string(change.Action), Diff: change.Diff})
	}
	return res, nil
}

// SourcePin moves a pin to a commit the caller has already shown.
func (r *suggestRPC) SourcePin(ctx context.Context, req *suggestv1.SourcePinRequest) (*suggestv1.SourcePinResponse, error) {
	if err := r.refuseInInspect("SourcePin"); err != nil {
		return nil, err
	}
	moved, err := r.s.SourcePin(ctx, SuggestSourcePinRequest{URL: req.GetUrl(), From: req.GetFrom(), To: req.GetTo()})
	if err != nil {
		return nil, suggestFailure(ctx, err)
	}
	return &suggestv1.SourcePinResponse{Pin: pinToProto(moved.Pin)}, nil
}
