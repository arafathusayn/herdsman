package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/approvals"
	"github.com/arafathusayn/herdsman/internal/gitrefs"
	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/wtree"
	"github.com/arafathusayn/herdsman/sdk/config"
)

const herdrProbeTimeout = 500 * time.Millisecond

// WorktreeService implements the worktree verbs. Its zero value uses the
// caller's environment; the fields exist so that tests can replace it.
type WorktreeService struct {
	HerdrSocket string // Empty resolves the session's socket.
	Approvals   string // Empty uses the user's configuration folder.
	GH          string // Program for GitHub calls; empty means "gh".
	Repo        string // owner/name; empty reads it from origin.
}

// WorktreeNewRequest asks for a task's worktree.
type WorktreeNewRequest struct {
	Dir     string `json:"dir"`
	Task    string `json:"task"`
	Base    string `json:"base,omitempty"`
	From    string `json:"from,omitempty"`
	Type    string `json:"type,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	NoSetup bool   `json:"no_setup,omitempty"`
	Clobber bool   `json:"clobber,omitempty"`
}

// WorktreeNewResponse describes the worktree that was made.
type WorktreeNewResponse struct {
	Task     string   `json:"task"`
	Branch   string   `json:"branch"`
	Path     string   `json:"path"`
	Base     string   `json:"base"`
	Port     int      `json:"port"`
	Herdr    bool     `json:"herdr"`
	Setup    []string `json:"setup,omitempty"`
	Launch   string   `json:"launch"`
	Logs     []string `json:"logs,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// WorktreeListRequest asks for the worktree table.
type WorktreeListRequest struct {
	Dir        string `json:"dir"`
	All        bool   `json:"all,omitempty"`
	Statusline bool   `json:"statusline,omitempty"`
}

// WorktreeRow is one line of the table. Path is empty for a branch with no tree.
type WorktreeRow struct {
	Task       string `json:"task"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	Head       string `json:"head"`
	Status     string `json:"status"`
	Marks      string `json:"marks"`
	Ahead      int    `json:"ahead"`
	Behind     int    `json:"behind"`
	HasBase    bool   `json:"has_base"`
	Subject    string `json:"subject"`
	CommitUnix int64  `json:"commit_unix"`
	Main       bool   `json:"main"`
	Default    bool   `json:"default"`
	Error      string `json:"error,omitempty"`
}

// WorktreeListResponse is the table, or one line of it for a status bar.
type WorktreeListResponse struct {
	Rows       []WorktreeRow `json:"rows"`
	Statusline string        `json:"statusline,omitempty"`
}

// WorktreeGoRequest asks where a selector leads.
type WorktreeGoRequest struct {
	Dir      string `json:"dir"`
	Selector string `json:"selector"`
	Focus    bool   `json:"focus,omitempty"`
}

// WorktreeGoResponse names the worktree a selector led to.
type WorktreeGoResponse struct {
	Task       string   `json:"task"`
	Branch     string   `json:"branch"`
	Path       string   `json:"path"`
	Created    bool     `json:"created"`
	Focused    bool     `json:"focused"`
	PushRemote string   `json:"push_remote,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// WorktreeHookRequest shows, approves or runs a hook. An empty Type means every
// configured hook, which only DryRun and Approve accept.
type WorktreeHookRequest struct {
	Dir     string `json:"dir"`
	Type    string `json:"type,omitempty"`
	From    string `json:"from,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
	Approve bool   `json:"approve,omitempty"`
}

// WorktreeHookCommand is one command of a hook, expanded.
type WorktreeHookCommand struct {
	Hook     string `json:"hook"`
	Name     string `json:"name"`
	Template string `json:"template"`
	Rendered string `json:"rendered"`
	Approved bool   `json:"approved"`
}

// WorktreeHookResponse is the plan, the approvals made, or the run's outcome.
type WorktreeHookResponse struct {
	Commands []WorktreeHookCommand `json:"commands,omitempty"`
	Approved []string              `json:"approved,omitempty"`
	OK       bool                  `json:"ok"`
	Result   string                `json:"result,omitempty"`
}

// WorktreeCall serves one verb for the command line in this process.
func WorktreeCall(ctx context.Context, method string, request []byte) ([]byte, error) {
	return (&WorktreeService{}).Call(ctx, method, request)
}

// Call decodes a request, runs the method and encodes its response.
func (s *WorktreeService) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case "New":
		return dispatch(ctx, request, s.New)
	case "List":
		return dispatch(ctx, request, s.List)
	case "Go":
		return dispatch(ctx, request, s.Go)
	case "Hook":
		return dispatch(ctx, request, s.Hook)
	case "Commit":
		return dispatch(ctx, request, s.Commit)
	case "Diff":
		return dispatch(ctx, request, s.Diff)
	case "TeardownPlan":
		return dispatch(ctx, request, s.TeardownPlan)
	case "Teardown":
		return dispatch(ctx, request, s.Teardown)
	case "ResourceAdd":
		return dispatch(ctx, request, s.ResourceAdd)
	case "LedgerHealth":
		return dispatch(ctx, request, s.LedgerHealth)
	}
	return nil, fmt.Errorf("worktree service: no method %q", method)
}

func dispatch[Req, Resp any](ctx context.Context, data []byte, method func(context.Context, Req) (Resp, error)) ([]byte, error) {
	var request Req
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("worktree service: decode request: %w", err)
	}
	response, err := method(ctx, request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

// repository is everything one call needs about the repository around Dir.
type repository struct {
	git     *gitrun.Runner
	state   *wtree.State
	reader  *gitrefs.Reader
	rows    []wtree.Row
	main    string
	common  string
	dir     string
	dflt    string
	hooks   hooks.Config
	project string
}

func (r *repository) close() { r.state.Close() }

func (s *WorktreeService) open(ctx context.Context, dir string) (*repository, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("worktree service: folder %q is not absolute", dir)
	}
	git := &gitrun.Runner{}
	out, err := git.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository: %w", dir, err)
	}
	common := strings.TrimSpace(string(out.Stdout))
	top := common
	if out, err := git.Run(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		top = strings.TrimSpace(string(out.Stdout))
	}
	reader, err := gitrefs.Open(top)
	if err != nil {
		return nil, err
	}
	state, err := wtree.OpenState(common)
	if err != nil {
		return nil, err
	}
	repo := &repository{git: git, state: state, reader: reader, common: common, dir: dir}
	collector := wtree.Collector{Refs: reader, State: state, Git: git}
	if repo.rows, err = collector.Skeleton(ctx); err != nil {
		state.Close()
		return nil, err
	}
	if len(repo.rows) == 0 {
		state.Close()
		return nil, fmt.Errorf("%s has no worktrees", common)
	}
	repo.main, repo.project = repo.rows[0].Path, repo.rows[0].Path
	repo.dflt = repo.defaultBranch(ctx)
	if repo.hooks, err = loadHooks(filepath.Dir(repo.main)); err != nil {
		state.Close()
		return nil, err
	}
	return repo, nil
}

func (r *repository) defaultBranch(ctx context.Context) string {
	for _, row := range r.rows {
		if row.IsDefault {
			return row.Branch
		}
	}
	if out, err := r.git.Run(ctx, r.main, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(string(out.Stdout)), "origin/")
	}
	return r.rows[0].Branch
}

// loadHooks reads the hooks of the configuration file beside the repository,
// in whichever format it was written. A file that cannot be read stops the
// command: running without the hooks someone wrote would be worse.
func loadHooks(folder string) (hooks.Config, error) {
	path, err := config.FileIn(folder)
	if err != nil || path == "" {
		return hooks.Config{}, err
	}
	result, err := config.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return hooks.Parse(result.HookTable())
}

func (s *WorktreeService) approvals() (*hooks.Approvals, error) {
	if s.Approvals != "" {
		return &hooks.Approvals{Path: s.Approvals}, nil
	}
	store, err := approvals.Default(os.Environ())
	if err != nil {
		return nil, err
	}
	return &hooks.Approvals{Path: store.Path()}, nil
}

func (r *repository) vars(task, branch, path, kind, base, head string) wtree.Vars {
	values := map[string]string{
		"task": task, "repo": filepath.Base(r.main), "repo_path": r.main, "type": kind,
	}
	optional := map[string]string{
		"branch": branch, "worktree_path": path, "base": base, "default_branch": r.dflt,
		"route": os.Getenv("HERDSMAN_ROUTE"), "commit": head,
	}
	if path != "" {
		optional["worktree_name"] = filepath.Base(path)
	}
	if len(head) >= 7 {
		optional["short_commit"] = head[:7]
	}
	for key, value := range optional {
		if value != "" {
			values[key] = value
		}
	}
	return wtree.Vars{Values: values, Port: wtree.HashPort(task)}
}

// plan expands a hook and refuses one that holds a template nobody accepted.
func (s *WorktreeService) plan(r *repository, hook string, vars wtree.Vars) (hooks.Plan, error) {
	plan, err := r.hooks.Plan(hook, vars)
	if err != nil {
		return nil, err
	}
	store, err := s.approvals()
	if err != nil {
		return nil, err
	}
	missing, err := store.Unapproved(r.project, plan)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("hook %s has commands that were not approved: %s; review them with `herdsman hook %s --dry-run`, accept them with `herdsman hook %s --approve`",
			hook, strings.Join(missing, "; "), hook, hook)
	}
	return plan, nil
}

func (r *repository) runner(vars wtree.Vars, path string) *hooks.Runner {
	return &hooks.Runner{LogDir: filepath.Join(r.common, wtree.StateFolder, "logs"), Dir: path, Env: hooks.Env{
		Task: vars.Values["task"], Branch: vars.Values["branch"], Worktree: path, Route: vars.Values["route"],
	}}
}

func (s *WorktreeService) herdrClient(ctx context.Context) *herdr.Client {
	socket := s.HerdrSocket
	if socket == "" {
		var err error
		if socket, err = herdr.SocketPath(""); err != nil {
			return nil
		}
	}
	client := herdr.New(socket)
	probe, cancel := context.WithTimeout(ctx, herdrProbeTimeout)
	defer cancel()
	if _, err := client.Check(probe); err != nil {
		return nil
	}
	return client
}

// resolveBase fetches the base and returns its name and the commit to start at.
func (r *repository) resolveBase(ctx context.Context, requested string, resp *WorktreeNewResponse) (base, start string, err error) {
	base = requested
	if base == "" {
		base = r.dflt
	}
	if err := wtree.ValidateBranch(base); err != nil {
		return "", "", err
	}
	if _, err := r.git.Run(ctx, r.main, "remote", "get-url", "origin"); err == nil {
		if _, err := r.git.Run(ctx, r.main, "fetch", "-q", "origin", base); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("could not fetch %s from origin; using the local copy", base))
		}
	}
	for _, candidate := range []string{"refs/remotes/origin/" + base, base} {
		out, err := r.git.Run(ctx, r.main, "rev-parse", "--verify", "-q", candidate+"^{commit}")
		if err == nil {
			return base, strings.TrimSpace(string(out.Stdout)), nil
		}
	}
	return "", "", fmt.Errorf("base %q does not exist", base)
}

// clearPath refuses an occupied path, or moves a stale folder aside on request.
func (r *repository) clearPath(path string, clobber bool) error {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	for _, row := range r.rows {
		if row.Path == path {
			return fmt.Errorf("%s is already the worktree of %s", path, row.Task)
		}
	}
	if !clobber {
		return fmt.Errorf("%s exists and is not a worktree; pass --clobber to rename it aside", path)
	}
	return os.Rename(path, fmt.Sprintf("%s.bak.%d", path, time.Now().Unix()))
}

// create makes the tree through herdr when it answers, else through git. With
// an empty start the branch already exists.
func (s *WorktreeService) create(ctx context.Context, r *repository, task, branch, path, start string, resp *WorktreeNewResponse) error {
	if client := s.herdrClient(ctx); client != nil && start != "" {
		params := map[string]any{"cwd": r.main, "branch": branch, "base": start, "path": path, "label": task, "focus": false}
		err := client.Call(ctx, "worktree.create", params, nil)
		if _, statErr := os.Stat(filepath.Join(path, ".git")); err == nil && statErr == nil {
			resp.Herdr = true
			return nil
		}
		resp.Warnings = append(resp.Warnings, "herdr did not create the worktree; used git instead")
	}
	args := []string{"worktree", "add", "-q", path, branch}
	if start != "" {
		args = []string{"worktree", "add", "-q", "-b", branch, path, start}
	}
	_, err := r.git.Run(ctx, r.main, args...)
	return err
}

// New makes a task's worktree: base, names, the tree, its hooks and setup.
func (s *WorktreeService) New(ctx context.Context, req WorktreeNewRequest) (WorktreeNewResponse, error) {
	var resp WorktreeNewResponse
	if err := wtree.ValidateTask(req.Task); err != nil {
		return resp, err
	}
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	kind := req.Type
	if kind == "" {
		kind = "feat"
	}
	naming := wtree.DefaultNaming()
	path, err := naming.ResolvePath(r.vars(req.Task, "", "", kind, "", ""))
	if err != nil {
		return resp, err
	}
	var branch, start string
	if req.From != "" {
		pr, err := s.pullRequest(ctx, r, req.From)
		if err != nil {
			return resp, err
		}
		branch, resp.Base = pr.Head.Ref, pr.Base.Ref
		if err := r.fetchRequest(ctx, pr); err != nil {
			return resp, err
		}
	} else {
		if resp.Base, start, err = r.resolveBase(ctx, req.Base, &resp); err != nil {
			return resp, err
		}
		if branch, err = naming.ResolveBranch(r.vars(req.Task, "", "", kind, resp.Base, "")); err != nil {
			return resp, err
		}
	}
	vars := r.vars(req.Task, branch, path, kind, resp.Base, start)
	before, err := s.plan(r, "pre-start", vars)
	if err != nil {
		return resp, err
	}
	after, err := s.plan(r, "post-start", vars)
	if err != nil {
		return resp, err
	}
	if err := r.clearPath(path, req.Clobber); err != nil {
		return resp, err
	}
	if err := s.create(ctx, r, req.Task, branch, path, start, &resp); err != nil {
		return resp, err
	}
	resp.Task, resp.Branch, resp.Path, resp.Port = req.Task, branch, path, vars.Port
	resp.Launch = "cd " + path
	if req.Agent != "" {
		resp.Launch += " && " + req.Agent
		resp.Warnings = append(resp.Warnings, "the agent was not started; run the launch line")
	} else if req.Prompt != "" {
		resp.Warnings = append(resp.Warnings, "the prompt was ignored: no agent was named")
	}
	if err := r.state.PutWorktree(ctx, wtree.Worktree{Task: req.Task, Branch: branch, Path: path, CreatedAt: time.Now()}); err != nil {
		return resp, fmt.Errorf("worktree %s was created, but: %w", path, err)
	}
	if err := s.finishNew(ctx, r, req, vars, before, after, &resp); err != nil {
		return resp, fmt.Errorf("worktree %s was created, but: %w", path, err)
	}
	return resp, nil
}

func (s *WorktreeService) finishNew(ctx context.Context, r *repository, req WorktreeNewRequest, vars wtree.Vars, before, after hooks.Plan, resp *WorktreeNewResponse) error {
	runner := r.runner(vars, resp.Path)
	if _, err := runner.Run(ctx, "pre-start", before); err != nil {
		return err
	}
	if !req.NoSetup {
		detected, err := wtree.Detect(resp.Path)
		if err != nil {
			return err
		}
		var setup hooks.Plan
		for _, install := range detected.Install {
			setup = append(setup, []hooks.Planned{{Command: hooks.Command{Name: "setup", Template: install}, Rendered: install}})
		}
		if _, err := runner.Run(ctx, "setup", setup); err != nil {
			return err
		}
		resp.Setup = detected.Install
	}
	logs, err := runner.Start("post-start", after)
	resp.Logs = logs
	return err
}

func tableRow(row wtree.Row) WorktreeRow {
	out := WorktreeRow{
		Task: row.Task, Branch: row.Branch, Path: row.Path, Head: row.Head, Status: row.Status.Symbols(),
		Marks: row.Marks(), Ahead: row.Divergence.Ahead, Behind: row.Divergence.Behind, HasBase: row.HasDivergence,
		Subject: row.Subject, Main: row.Main, Default: row.IsDefault,
	}
	if !row.CommitTime.IsZero() {
		out.CommitUnix = row.CommitTime.Unix()
	}
	if row.Err != nil {
		out.Error = row.Err.Error()
	}
	return out
}

// List returns the table, and with Statusline the line for the current tree.
func (s *WorktreeService) List(ctx context.Context, req WorktreeListRequest) (WorktreeListResponse, error) {
	var resp WorktreeListResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	collector := wtree.Collector{Refs: r.reader, State: r.state, Git: r.git}
	if err := collector.Fill(ctx, r.rows, nil); err != nil {
		return resp, err
	}
	for _, row := range r.rows {
		resp.Rows = append(resp.Rows, tableRow(row))
	}
	if req.Statusline {
		current, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectCurrent}, r.selectContext(""))
		if err != nil {
			return resp, err
		}
		resp.Statusline = statusline(tableRow(current))
	}
	if req.All {
		if err := r.appendBranches(ctx, &resp); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func statusline(row WorktreeRow) string {
	name := row.Branch
	if name == "" {
		name = row.Task
	}
	parts := []string{name}
	if cell := row.Status + row.Marks; cell != "" {
		parts = append(parts, cell)
	}
	if row.Ahead > 0 {
		parts = append(parts, "↑"+strconv.Itoa(row.Ahead))
	}
	if row.Behind > 0 {
		parts = append(parts, "↓"+strconv.Itoa(row.Behind))
	}
	return strings.Join(parts, " ")
}

func (r *repository) appendBranches(ctx context.Context, resp *WorktreeListResponse) error {
	refs, err := r.reader.Refs(ctx)
	if err != nil {
		return err
	}
	used := make([]string, 0, len(r.rows))
	for _, row := range r.rows {
		used = append(used, row.Branch)
	}
	for _, ref := range refs {
		name, ok := strings.CutPrefix(ref.Name, "refs/heads/")
		if ok && !slices.Contains(used, name) {
			resp.Rows = append(resp.Rows, WorktreeRow{Task: name, Branch: name, Head: ref.Object})
		}
	}
	return nil
}

func (r *repository) selectContext(previous string) wtree.SelectContext {
	return wtree.SelectContext{Rows: r.rows, Default: r.dflt, Current: r.dir, Previous: previous}
}

// Go finds the worktree a selector means, making one for a pull request.
func (s *WorktreeService) Go(ctx context.Context, req WorktreeGoRequest) (WorktreeGoResponse, error) {
	var resp WorktreeGoResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	selector, err := wtree.ParseSelector(req.Selector)
	if err != nil {
		return resp, err
	}
	previous, err := r.state.Previous(ctx)
	if err != nil {
		return resp, err
	}
	var target wtree.Row
	if selector.Kind == wtree.SelectPullRequest || selector.Kind == wtree.SelectMergeRequest {
		target, err = s.goRequest(ctx, r, req.Selector, selector, &resp)
	} else {
		target, err = wtree.ResolveSelector(selector, r.selectContext(previous))
	}
	if err != nil {
		return resp, err
	}
	resp.Task, resp.Branch, resp.Path = target.Task, target.Branch, target.Path
	vars := r.vars(target.Task, target.Branch, target.Path, "feat", "", target.Head)
	if err := s.runSwitch(ctx, r, vars, target.Path); err != nil {
		return resp, err
	}
	current, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectCurrent}, r.selectContext(""))
	if err == nil && current.Path != target.Path {
		if err := r.state.SetPrevious(ctx, current.Task); err != nil {
			return resp, err
		}
	}
	if req.Focus {
		s.focus(ctx, target.Path, &resp)
	}
	return resp, nil
}

func (s *WorktreeService) runSwitch(ctx context.Context, r *repository, vars wtree.Vars, path string) error {
	before, err := s.plan(r, "pre-switch", vars)
	if err != nil {
		return err
	}
	after, err := s.plan(r, "post-switch", vars)
	if err != nil {
		return err
	}
	runner := r.runner(vars, path)
	if _, err := runner.Run(ctx, "pre-switch", before); err != nil {
		return err
	}
	_, err = runner.Start("post-switch", after)
	return err
}

func (s *WorktreeService) focus(ctx context.Context, path string, resp *WorktreeGoResponse) {
	client := s.herdrClient(ctx)
	if client == nil {
		resp.Warnings = append(resp.Warnings, "herdr is not reachable; nothing was focused")
		return
	}
	panes, err := client.PaneList(ctx)
	if err == nil {
		for _, pane := range panes {
			if pane.Cwd != nil && *pane.Cwd == path {
				err = client.TabFocus(ctx, pane.TabID)
				resp.Focused = err == nil
				break
			}
		}
	}
	if !resp.Focused {
		resp.Warnings = append(resp.Warnings, "no herdr pane could be focused for this worktree")
	}
}

type pullRequest struct {
	Number int
	Head   struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

var (
	originRepo = regexp.MustCompile(`[:/]([^/:]+/[^/]+?)(\.git)?/?$`)
	remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
)

func (s *WorktreeService) repoName(ctx context.Context, r *repository) (string, error) {
	if s.Repo != "" {
		return s.Repo, nil
	}
	// The stored address: get-url would apply the user's insteadOf rewrites.
	out, err := r.git.Run(ctx, r.main, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", errors.New("this repository has no origin to ask about pull requests")
	}
	match := originRepo.FindStringSubmatch(strings.TrimSpace(string(out.Stdout)))
	if match == nil {
		return "", fmt.Errorf("cannot read owner/name from origin %q", strings.TrimSpace(string(out.Stdout)))
	}
	return match[1], nil
}

// pullRequest reads a pull request of this repository through gh.
func (s *WorktreeService) pullRequest(ctx context.Context, r *repository, text string) (*pullRequest, error) {
	selector, err := wtree.ParseSelector(text)
	if err != nil {
		return nil, err
	}
	if selector.Kind != wtree.SelectPullRequest {
		return nil, fmt.Errorf("%q is not a pull request; only GitHub pull requests can be fetched", text)
	}
	repo, err := s.repoName(ctx, r)
	if err != nil {
		return nil, err
	}
	if selector.Repo != "" && !strings.EqualFold(selector.Repo, repo) {
		return nil, fmt.Errorf("%s belongs to %s, not to this repository (%s)", text, selector.Repo, repo)
	}
	program := s.GH
	if program == "" {
		program = "gh"
	}
	out, err := exec.CommandContext(ctx, program, "api", "--include", fmt.Sprintf("repos/%s/pulls/%d", repo, selector.Number)).Output()
	if err != nil {
		return nil, fmt.Errorf("read pull request %d: %w", selector.Number, err)
	}
	pr := &pullRequest{Number: selector.Number}
	if err := json.Unmarshal(httpBody(out), pr); err != nil {
		return nil, fmt.Errorf("read pull request %d: %w", selector.Number, err)
	}
	if err := wtree.ValidateBranch(pr.Head.Ref); err != nil {
		return nil, fmt.Errorf("pull request %d: %w", selector.Number, err)
	}
	return pr, nil
}

// httpBody drops the status line and headers that `gh api --include` prints.
func httpBody(out []byte) []byte {
	if !bytes.HasPrefix(out, []byte("HTTP/")) {
		return out
	}
	for _, separator := range []string{"\r\n\r\n", "\n\n"} {
		if _, body, found := bytes.Cut(out, []byte(separator)); found {
			return body
		}
	}
	return out
}

func (r *repository) fetchRequest(ctx context.Context, pr *pullRequest) error {
	refspec := fmt.Sprintf("refs/pull/%d/head:refs/heads/%s", pr.Number, pr.Head.Ref)
	_, err := r.git.Run(ctx, r.main, "fetch", "-q", "origin", refspec)
	return err
}

func (s *WorktreeService) goRequest(ctx context.Context, r *repository, text string, selector wtree.Selector, resp *WorktreeGoResponse) (wtree.Row, error) {
	pr, err := s.pullRequest(ctx, r, text)
	if err != nil {
		return wtree.Row{}, err
	}
	task := "pr-" + strconv.Itoa(selector.Number)
	for _, row := range r.rows {
		if row.Task == task || row.Branch == pr.Head.Ref {
			return row, nil
		}
	}
	if err := r.fetchRequest(ctx, pr); err != nil {
		return wtree.Row{}, err
	}
	path, err := wtree.DefaultNaming().ResolvePath(r.vars(task, "", "", "feat", "", ""))
	if err != nil {
		return wtree.Row{}, err
	}
	if err := r.clearPath(path, false); err != nil {
		return wtree.Row{}, err
	}
	if _, err := r.git.Run(ctx, r.main, "worktree", "add", "-q", path, pr.Head.Ref); err != nil {
		return wtree.Row{}, err
	}
	if err := r.state.PutWorktree(ctx, wtree.Worktree{Task: task, Branch: pr.Head.Ref, Path: path, CreatedAt: time.Now()}); err != nil {
		return wtree.Row{}, err
	}
	resp.Created = true
	if resp.PushRemote, err = s.setPushRemote(ctx, r, pr); err != nil {
		return wtree.Row{}, err
	}
	return wtree.Row{Task: task, Branch: pr.Head.Ref, Path: path}, nil
}

// setPushRemote points a fork's branch at the fork, so that a push goes there.
func (s *WorktreeService) setPushRemote(ctx context.Context, r *repository, pr *pullRequest) (string, error) {
	repo, err := s.repoName(ctx, r)
	if err != nil {
		return "", err
	}
	head := pr.Head.Repo
	if head == nil || strings.EqualFold(head.FullName, repo) {
		return "", nil
	}
	remote := head.Owner.Login
	if !remoteName.MatchString(remote) || !strings.HasPrefix(head.CloneURL, "https://") {
		return "", fmt.Errorf("pull request %d: unusable fork %q at %q", pr.Number, remote, head.CloneURL)
	}
	if _, err := r.git.Run(ctx, r.main, "remote", "get-url", remote); err != nil {
		if _, err := r.git.Run(ctx, r.main, "remote", "add", remote, head.CloneURL); err != nil {
			return "", err
		}
	}
	_, err = r.git.Run(ctx, r.main, "config", "branch."+pr.Head.Ref+".pushRemote", remote)
	return remote, err
}

func hookTypes(requested string) ([]string, error) {
	if requested == "" {
		return hooks.Types, nil
	}
	if !slices.Contains(hooks.Types, requested) {
		return nil, fmt.Errorf("unknown hook %q; one of %s", requested, strings.Join(hooks.Types, ", "))
	}
	return []string{requested}, nil
}

// Hook shows, approves or runs a hook for the current worktree or From's.
func (s *WorktreeService) Hook(ctx context.Context, req WorktreeHookRequest) (WorktreeHookResponse, error) {
	var resp WorktreeHookResponse
	types, err := hookTypes(req.Type)
	if err != nil {
		return resp, err
	}
	if req.Type == "" && !req.DryRun && !req.Approve {
		return resp, errors.New("name the hook to run")
	}
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	selector := wtree.Selector{Kind: wtree.SelectCurrent}
	if req.From != "" {
		if selector, err = wtree.ParseSelector(req.From); err != nil {
			return resp, err
		}
	}
	target, err := wtree.ResolveSelector(selector, r.selectContext(""))
	if err != nil {
		return resp, err
	}
	vars := r.vars(target.Task, target.Branch, target.Path, "feat", "", target.Head)
	store, err := s.approvals()
	if err != nil {
		return resp, err
	}
	if req.DryRun || req.Approve {
		return s.describeHooks(r, store, types, vars, req.Approve)
	}
	plan, err := s.plan(r, req.Type, vars)
	if err != nil {
		return resp, err
	}
	result, err := r.runner(vars, target.Path).Run(ctx, req.Type, plan)
	resp.OK, resp.Result = result.OK, result.File
	return resp, err
}

func (s *WorktreeService) describeHooks(r *repository, store *hooks.Approvals, types []string, vars wtree.Vars, approve bool) (WorktreeHookResponse, error) {
	var resp WorktreeHookResponse
	for _, hook := range types {
		plan, err := r.hooks.Plan(hook, vars)
		if err != nil {
			return resp, err
		}
		missing, err := store.Unapproved(r.project, plan)
		if err != nil {
			return resp, err
		}
		for _, step := range plan {
			for _, command := range step {
				resp.Commands = append(resp.Commands, WorktreeHookCommand{
					Hook: hook, Name: command.Name, Template: command.Template, Rendered: command.Rendered,
					Approved: !slices.Contains(missing, command.Template),
				})
			}
		}
		if approve && len(missing) > 0 {
			if err := store.Approve(r.project, missing); err != nil {
				return resp, err
			}
			resp.Approved = append(resp.Approved, missing...)
		}
	}
	resp.OK = true
	return resp, nil
}
