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

	"github.com/arafathusayn/herdsman/internal/cow"
	"github.com/arafathusayn/herdsman/internal/gitrefs"
	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/hooks"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

const (
	herdrProbeTimeout = 500 * time.Millisecond
	agentStartTimeout = 60 * time.Second
)

// WorktreeService implements the worktree verbs. Its zero value uses the
// caller's environment; the fields exist so that tests can replace it.
type WorktreeService struct {
	HerdrSocket string // Empty resolves the session's socket.
	Approvals   string // Empty uses the user's configuration folder.
	GH          string // Program for GitHub calls; empty means "gh".
	Repo        string // owner/name; empty reads it from origin.
	StateDir    string // Empty resolves the account's state folder.
	FileCap     int64  // Largest file a snapshot captures; 0 is the default.
	ConfigDir   string // Folder of the user's file; empty uses the standard one.

	caller  []string     // The caller's environment; nil means this process's.
	journal *taskJournal // Nil runs the effects without an operation.
}

// forCaller is the service for a request from a process with this environment.
func (s *WorktreeService) forCaller(environ []string) *WorktreeService {
	served := *s
	served.caller = environ
	return &served
}

// getenv reads the caller's environment.
func (s *WorktreeService) getenv(name string) string {
	if s.caller == nil {
		return os.Getenv(name)
	}
	prefix := name + "="
	for _, entry := range s.caller {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value
		}
	}
	return ""
}

// worktreeSchemaVersion is the version of the verbs' JSON. It only grows.
const worktreeSchemaVersion = "1.0.0"

// WorktreeSchema says which version of the JSON a response follows.
type WorktreeSchema struct {
	Version string `json:"version"`
}

// versioned is embedded in each response of the worktree verbs.
type versioned struct {
	Schema WorktreeSchema `json:"schema"`
}

func (v *versioned) stamp() { v.Schema.Version = worktreeSchemaVersion }

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
	// Integrator gives the agent the [policy] roots under the restricted profile.
	Integrator bool `json:"integrator,omitempty"`
}

// WorktreeNewResponse describes the worktree that was made.
type WorktreeNewResponse struct {
	versioned
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
	// AgentName is the agent started in the worktree, when one was.
	AgentName string `json:"agent_name,omitempty"`
	// Ports is the task's reserved block, starting at Port.
	Ports []int `json:"ports,omitempty"`
}

// WorktreeListRequest asks for the worktree table.
type WorktreeListRequest struct {
	Dir        string `json:"dir"`
	All        bool   `json:"all,omitempty"`
	Statusline bool   `json:"statusline,omitempty"`
	Full       bool   `json:"full,omitempty"`     // Add the slow cells and a summary.
	Skeleton   bool   `json:"skeleton,omitempty"` // Names only, without collecting.
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
	// The cells of a full list.
	Remote       string `json:"remote,omitempty"`
	RemoteAhead  int    `json:"remote_ahead,omitempty"`
	RemoteBehind int    `json:"remote_behind,omitempty"`
	Agent        string `json:"agent,omitempty"`
	PR           int    `json:"pr,omitempty"`
	Review       string `json:"review,omitempty"`
	CI           string `json:"ci,omitempty"`
	Port         int    `json:"port,omitempty"`
	PortsHeld    int    `json:"ports_held,omitempty"`
	Containers   int    `json:"containers,omitempty"`
	DiskBytes    int64  `json:"disk_bytes,omitempty"`
}

// WorktreeListSummary is the line under a full table.
type WorktreeListSummary struct {
	Worktrees int `json:"worktrees"`
	Dirty     int `json:"dirty"`
}

// WorktreeListResponse is the table, or one line of it for a status bar.
type WorktreeListResponse struct {
	versioned
	Rows       []WorktreeRow       `json:"rows"`
	Statusline string              `json:"statusline,omitempty"`
	Summary    WorktreeListSummary `json:"summary,omitzero"`
	Warnings   []string            `json:"warnings,omitempty"`
}

// WorktreeGoRequest asks where a selector leads.
type WorktreeGoRequest struct {
	Dir      string `json:"dir"`
	Selector string `json:"selector"`
	Focus    bool   `json:"focus,omitempty"`
}

// WorktreeGoResponse names the worktree a selector led to.
type WorktreeGoResponse struct {
	versioned
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
	versioned
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
	case "CleanPlan":
		return dispatch(ctx, request, s.CleanPlan)
	case "Clean":
		return dispatch(ctx, request, s.Clean)
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
	if stamped, ok := any(&response).(interface{ stamp() }); ok {
		stamped.stamp()
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
	naming  wtree.Naming
	route   string
	environ []string
	// What setup.env and setup.share list, and setup.ports.
	envFiles, shared []string
	ports            int
	project          string
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
	repo := &repository{git: git, state: state, reader: reader, common: common, dir: dir, route: s.getenv("HERDSMAN_ROUTE"), environ: s.caller}
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
	if err := s.loadSettings(ctx, repo); err != nil {
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

func (s *WorktreeService) approvals() (*hooks.Approvals, error) {
	if s.Approvals != "" {
		return &hooks.Approvals{Path: s.Approvals}, nil
	}
	base := s.getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".config")
	}
	return &hooks.Approvals{Path: filepath.Join(base, "herdsman", "approvals.json")}, nil
}

func (r *repository) vars(task, branch, path, kind, base, head string) wtree.Vars {
	values := map[string]string{
		"task": task, "repo": filepath.Base(r.main), "repo_path": r.main, "type": kind,
	}
	optional := map[string]string{
		"branch": branch, "worktree_path": path, "base": base, "default_branch": r.dflt,
		"route": r.route, "commit": head,
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
	port := wtree.HashPort(task)
	block := make(map[string]int, r.ports)
	for i := range r.ports {
		block[strconv.Itoa(i)] = port + i
	}
	return wtree.Vars{Values: values, Port: port, Ports: block}
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
	return &hooks.Runner{LogDir: filepath.Join(r.common, wtree.StateFolder, "logs"), Dir: path, Environ: r.environ, Env: hooks.Env{
		Task: vars.Values["task"], Branch: vars.Values["branch"], Worktree: path, Route: vars.Values["route"],
	}}
}

func (s *WorktreeService) herdrClient(ctx context.Context) *herdr.Client {
	socket := s.HerdrSocket
	if socket == "" {
		var err error
		if s.caller != nil {
			socket, err = herdr.SocketPathFor(s.getenv)
		} else {
			socket, err = herdr.SocketPath("")
		}
		if err != nil {
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
func (s *WorktreeService) create(ctx context.Context, r *repository, shape taskShape, task, branch, path, start string, resp *WorktreeNewResponse) error {
	if shape.ownClone {
		return r.cloneTask(ctx, branch, path, start)
	}
	client := s.herdrClient(ctx)
	if client != nil && start != "" {
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
	if _, err := r.git.Run(ctx, r.main, args...); err != nil {
		return err
	}
	if client != nil && start == "" {
		// herdr's create always makes a branch; one that exists can only be opened.
		params := map[string]any{"cwd": r.main, "path": path, "label": task, "focus": false}
		if err := client.Call(ctx, "worktree.open", params, nil); err != nil {
			resp.Warnings = append(resp.Warnings, "herdr did not open the worktree: "+err.Error())
		} else {
			resp.Herdr = true
		}
	}
	return nil
}

// New makes a task's worktree: base, names, the tree, its hooks and setup.
func (s *WorktreeService) New(ctx context.Context, req WorktreeNewRequest) (WorktreeNewResponse, error) {
	var resp WorktreeNewResponse
	if err := wtree.ValidateTask(req.Task); err != nil {
		return resp, err
	}
	if _, err := splitAgentCommand(req.Agent); err != nil {
		return resp, err
	}
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	shape, err := s.shapeOf(r)
	if err != nil {
		return resp, err
	}
	command, err := shape.admit(req.Agent)
	if err != nil {
		return resp, err
	}
	kind := req.Type
	if kind == "" {
		kind = "feat"
	}
	naming := r.naming
	path, err := naming.ResolvePath(r.vars(req.Task, "", "", kind, "", ""))
	if err != nil {
		return resp, err
	}
	var branch, start string
	var request *pullRequest
	if req.From != "" {
		pr, err := s.pullRequest(ctx, r, req.From)
		if err != nil {
			return resp, err
		}
		resp.Base = pr.Base.Ref
		if branch, start, err = r.fetchRequest(ctx, pr); err != nil {
			return resp, err
		}
		request = pr
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
	spec := treeSpec{Main: r.main, Task: req.Task, Branch: branch, Path: path, Start: start, OwnClone: shape.ownClone}
	if request != nil {
		spec.Identity = request.identity
	}
	if err := s.makeTree(ctx, r, spec, &resp); err != nil {
		return resp, err
	}
	if request != nil {
		resp.Warnings = append(resp.Warnings, renamedWarning(request, branch)...)
	}
	resp.Task, resp.Branch, resp.Path, resp.Port = req.Task, branch, path, vars.Port
	for i := range r.ports {
		resp.Ports = append(resp.Ports, vars.Port+i)
	}
	resp.Launch = "cd " + path
	if len(command) > 0 {
		if resp.Launch, err = shape.launchLine(command, path, req.Integrator); err != nil {
			return resp, fmt.Errorf("worktree %s was created, but: %w", path, err)
		}
	} else if req.Agent != "" {
		resp.Launch += " && " + req.Agent
	}
	if req.Integrator && !shape.ownClone {
		resp.Warnings = append(resp.Warnings, "integrator has no effect outside the restricted profile")
	}
	if req.Agent != "" {
		if !resp.Herdr {
			resp.Warnings = append(resp.Warnings, "the agent was not started; run the launch line")
		}
	} else if req.Prompt != "" {
		resp.Warnings = append(resp.Warnings, "the prompt was ignored: no agent was named")
	}
	if err := s.finishNew(ctx, r, req, vars, before, after, &resp); err != nil {
		return resp, fmt.Errorf("worktree %s was created, but: %w", path, err)
	}
	if err := s.enterWorkspace(ctx, req, &resp); err != nil {
		return resp, fmt.Errorf("worktree %s was created, but: %w", path, err)
	}
	return resp, nil
}

// quoteForShell makes text one word for a shell.
func quoteForShell(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// workspacePane finds the pane herdr opened in the worktree.
func workspacePane(ctx context.Context, client *herdr.Client, path string) (string, error) {
	panes, err := client.PaneList(ctx)
	if err != nil {
		return "", err
	}
	for _, pane := range panes {
		if pane.Cwd != nil && *pane.Cwd == path {
			return pane.ID, nil
		}
	}
	return "", fmt.Errorf("no herdr pane is at %s", path)
}

// enterWorkspace gives the worktree's herdr pane the task's environment and
// starts the agent there. herdr's worktree calls take no environment, so the
// values are exported at the pane's shell prompt.
func (s *WorktreeService) enterWorkspace(ctx context.Context, req WorktreeNewRequest, resp *WorktreeNewResponse) error {
	if !resp.Herdr {
		return nil
	}
	client := s.herdrClient(ctx)
	if client == nil {
		return errors.New("herdr stopped answering before the task's environment was set")
	}
	pane, err := workspacePane(ctx, client, resp.Path)
	if err != nil {
		return fmt.Errorf("the task's environment was not set: %w", err)
	}
	export := fmt.Sprintf("export HERDSMAN_TASK=%s HERDSMAN_BRANCH=%s HERDSMAN_WORKTREE_PATH=%s\n",
		quoteForShell(resp.Task), quoteForShell(resp.Branch), quoteForShell(resp.Path))
	if err := client.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane, "text": export}, nil); err != nil {
		return fmt.Errorf("the task's environment was not set: %w", err)
	}
	words, err := splitAgentCommand(req.Agent)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return nil
	}
	if _, err := client.AgentStart(ctx, resp.Task, words[0], pane, words[1:], int(agentStartTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("the agent did not start: %w; run the launch line", err)
	}
	resp.AgentName = resp.Task
	if req.Prompt != "" {
		if err := client.AgentPrompt(ctx, resp.Task, req.Prompt); err != nil {
			return fmt.Errorf("the agent started, but the prompt was not delivered: %w", err)
		}
	}
	return nil
}

// shareIgnored brings one ignored folder over from the main tree, sharing its
// blocks where the file system can. It leaves alone a folder the checkout made
// and one that is not ignored, which may be someone's work. A cache that did
// not come over only makes the install slower, so that is a warning.
func (r *repository) shareIgnored(ctx context.Context, folder string, resp *WorktreeNewResponse) {
	src, dst := filepath.Join(r.main, folder), filepath.Join(resp.Path, folder)
	if _, err := os.Lstat(src); err != nil {
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	if _, err := r.git.Run(ctx, r.main, "check-ignore", "-q", folder); err != nil {
		return
	}
	if _, err := cow.Clone(src, dst); err != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("could not bring %s over: %v", folder, err))
	}
}

// bringListed brings over what the configuration names. A listed path outside
// the repository or one that is tracked work stays, and the response says so.
func (r *repository) bringListed(ctx context.Context, listed []string, resp *WorktreeNewResponse) {
	for _, name := range listed {
		if !filepath.IsLocal(name) {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("setup lists %s, which is outside the repository; left alone", name))
			continue
		}
		if _, err := os.Lstat(filepath.Join(r.main, name)); err != nil {
			continue
		}
		if _, err := r.git.Run(ctx, r.main, "check-ignore", "-q", name); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("setup lists %s, which git does not ignore; left alone", name))
			continue
		}
		r.shareIgnored(ctx, name, resp)
	}
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
		for _, folder := range detected.Share {
			r.shareIgnored(ctx, folder, resp)
		}
		r.bringListed(ctx, slices.Concat(r.envFiles, r.shared), resp)
		setup, installs, err := s.setupPlan(r, vars, detected)
		if err != nil {
			return err
		}
		if _, err := runner.Run(ctx, "setup", setup); err != nil {
			return err
		}
		resp.Setup = installs
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
	if req.Skeleton {
		for _, row := range r.rows {
			resp.Rows = append(resp.Rows, tableRow(row))
		}
		return resp, nil
	}
	collector := wtree.Collector{Refs: r.reader, State: r.state, Git: r.git}
	if err := collector.Fill(ctx, r.rows, nil); err != nil {
		return resp, err
	}
	for _, row := range r.rows {
		resp.Rows = append(resp.Rows, tableRow(row))
	}
	if req.Full {
		s.fillFull(ctx, r, &resp)
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
		SHA  string `json:"sha"`
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

	identity string // Which pull request of which repository this is.
	sameRepo bool   // The head branch lives in this repository.
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
	pr.identity = strings.ToLower(repo) + "#" + strconv.Itoa(selector.Number)
	pr.sameRepo = pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, repo)
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

func requestMarker(branch string) string { return "branch." + branch + ".herdsman-pr" }

// markedRequest says which pull request a local branch was made for, if any.
func (r *repository) markedRequest(ctx context.Context, branch string) string {
	if branch == "" {
		return ""
	}
	out, err := r.git.Run(ctx, r.main, "config", "--get", requestMarker(branch))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out.Stdout))
}

func (r *repository) markRequest(ctx context.Context, pr *pullRequest, branch string) error {
	_, err := r.git.Run(ctx, r.main, "config", requestMarker(branch), pr.identity)
	return err
}

func (r *repository) branchExists(ctx context.Context, branch string) bool {
	_, err := r.git.Run(ctx, r.main, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return err == nil
}

// requestBranch picks the local branch of a pull request. A fork's head name
// says nothing about the local branch of that name, so a taken name gets a
// distinct one. It reports whether the branch already belongs to the request.
func (r *repository) requestBranch(ctx context.Context, pr *pullRequest) (string, bool, error) {
	if !r.branchExists(ctx, pr.Head.Ref) {
		return pr.Head.Ref, false, nil
	}
	if pr.sameRepo || r.markedRequest(ctx, pr.Head.Ref) == pr.identity {
		return pr.Head.Ref, true, nil
	}
	distinct := "pr-" + strconv.Itoa(pr.Number) + "-" + strings.ReplaceAll(pr.Head.Ref, "/", "-")
	if !r.branchExists(ctx, distinct) {
		return distinct, false, nil
	}
	if r.markedRequest(ctx, distinct) == pr.identity {
		return distinct, true, nil
	}
	return "", false, fmt.Errorf("pull request %d: the branches %s and %s belong to other work", pr.Number, pr.Head.Ref, distinct)
}

// fetchRequest brings the pull request's commit here and checks it is the one
// GitHub named. It returns the local branch, and the commit to start that
// branch at when it does not exist yet.
func (r *repository) fetchRequest(ctx context.Context, pr *pullRequest) (branch, start string, err error) {
	if pr.Head.SHA == "" {
		return "", "", fmt.Errorf("pull request %d: GitHub named no head commit", pr.Number)
	}
	private := "refs/herdsman/pull/" + strconv.Itoa(pr.Number)
	if _, err := r.git.Run(ctx, r.main, "fetch", "-q", "origin", fmt.Sprintf("+refs/pull/%d/head:%s", pr.Number, private)); err != nil {
		return "", "", err
	}
	defer r.git.Run(context.WithoutCancel(ctx), r.main, "update-ref", "-d", private)
	out, err := r.git.Run(ctx, r.main, "rev-parse", "--verify", "-q", private+"^{commit}")
	if err != nil {
		return "", "", err
	}
	fetched := strings.TrimSpace(string(out.Stdout))
	if !strings.EqualFold(fetched, pr.Head.SHA) {
		return "", "", fmt.Errorf("pull request %d: fetched %s, but GitHub names %s as its head", pr.Number, fetched, pr.Head.SHA)
	}
	branch, exists, err := r.requestBranch(ctx, pr)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return branch, fetched, nil
	}
	for _, row := range r.rows {
		if row.Branch == branch {
			return "", "", fmt.Errorf("pull request %d: branch %s is checked out at %s", pr.Number, branch, row.Path)
		}
	}
	ref := "refs/heads/" + branch
	if _, err := r.git.Run(ctx, r.main, "merge-base", "--is-ancestor", ref, fetched); err != nil {
		return "", "", fmt.Errorf("pull request %d: local branch %s has commits the request does not", pr.Number, branch)
	}
	if _, err := r.git.Run(ctx, r.main, "update-ref", ref, fetched); err != nil {
		return "", "", err
	}
	return branch, "", nil
}

func renamedWarning(pr *pullRequest, branch string) []string {
	if branch == pr.Head.Ref {
		return nil
	}
	return []string{fmt.Sprintf("the local branch %s is already other work, so pull request %d is on %s; to publish, push %s:%s", pr.Head.Ref, pr.Number, branch, branch, pr.Head.Ref)}
}

// holdsCommit says whether the row's branch contains the commit.
func (r *repository) holdsCommit(ctx context.Context, row wtree.Row, commit string) bool {
	if commit == "" {
		return false
	}
	_, err := r.git.Run(ctx, r.main, "merge-base", "--is-ancestor", commit, "refs/heads/"+row.Branch)
	return err == nil
}

// existingRequest finds the worktree that already holds the pull request.
func (r *repository) existingRequest(ctx context.Context, pr *pullRequest, task string) (wtree.Row, bool, error) {
	for _, row := range r.rows {
		if r.markedRequest(ctx, row.Branch) == pr.identity || (pr.sameRepo && row.Branch == pr.Head.Ref) {
			return row, true, nil
		}
	}
	for _, row := range r.rows {
		if row.Task != task {
			continue
		}
		// A worktree made before branches carried the marker counts only when
		// it really holds the request's commit.
		if row.Branch == pr.Head.Ref && !row.Main && r.markedRequest(ctx, row.Branch) == "" && r.holdsCommit(ctx, row, pr.Head.SHA) {
			return row, true, r.markRequest(ctx, pr, row.Branch)
		}
		return wtree.Row{}, false, fmt.Errorf("task %s is other work, on branch %s; pull request %d needs that name", task, row.Branch, pr.Number)
	}
	return wtree.Row{}, false, nil
}

func (s *WorktreeService) goRequest(ctx context.Context, r *repository, text string, selector wtree.Selector, resp *WorktreeGoResponse) (wtree.Row, error) {
	pr, err := s.pullRequest(ctx, r, text)
	if err != nil {
		return wtree.Row{}, err
	}
	task := "pr-" + strconv.Itoa(selector.Number)
	if row, found, err := r.existingRequest(ctx, pr, task); found || err != nil {
		return row, err
	}
	branch, start, err := r.fetchRequest(ctx, pr)
	if err != nil {
		return wtree.Row{}, err
	}
	path, err := r.naming.ResolvePath(r.vars(task, "", "", "feat", "", ""))
	if err != nil {
		return wtree.Row{}, err
	}
	if err := r.clearPath(path, false); err != nil {
		return wtree.Row{}, err
	}
	shape, err := s.shapeOf(r)
	if err != nil {
		return wtree.Row{}, err
	}
	gitDir := r.main
	if shape.ownClone {
		gitDir = path
	}
	var made WorktreeNewResponse
	spec := treeSpec{Main: r.main, Task: task, Branch: branch, Path: path, Start: start, Identity: pr.identity, GitOnly: true, OwnClone: shape.ownClone}
	if err := s.makeTree(ctx, r, spec, &made); err != nil {
		return wtree.Row{}, err
	}
	resp.Warnings = append(resp.Warnings, made.Warnings...)
	resp.Created = true
	resp.Warnings = append(resp.Warnings, renamedWarning(pr, branch)...)
	if resp.PushRemote, err = r.setPushRemote(ctx, pr, branch, gitDir); err != nil {
		return wtree.Row{}, err
	}
	return wtree.Row{Task: task, Branch: branch, Path: path}, nil
}

var defaultPorts = map[string]string{"https": "443", "http": "80", "ssh": "22", "git": "9418"}

// remoteAddress reduces a remote's URL to host and path, so that the https and
// ssh spellings of one repository compare equal. A port other than the
// scheme's own is a different server and stays.
func remoteAddress(url string) string {
	url = strings.TrimSpace(url)
	if scheme, rest, ok := strings.Cut(url, "://"); ok {
		host, path, _ := strings.Cut(rest, "/")
		host = host[strings.LastIndex(host, "@")+1:]
		if name, port, ported := strings.Cut(host, ":"); ported && port == defaultPorts[strings.ToLower(scheme)] {
			host = name
		}
		url = host + "/" + path
	} else if host, path, ok := strings.Cut(url, ":"); ok && !strings.Contains(host, "/") {
		url = host[strings.LastIndex(host, "@")+1:] + "/" + path
	}
	url = strings.TrimRight(url, "/")
	return strings.ToLower(strings.TrimSuffix(url, ".git"))
}

// pushesOnlyTo says whether every push address of the listing is the fork.
func pushesOnlyTo(listing, fork string) bool {
	want := remoteAddress(fork)
	for _, line := range strings.Fields(listing) {
		if remoteAddress(line) != want {
			return false
		}
	}
	return true
}

// forkRemote returns a remote that pushes to the fork: the owner's name when it
// is free or already that fork, else an unused name. It never edits a remote
// the user set up.
func (r *repository) forkRemote(ctx context.Context, gitDir, owner, url string) (string, error) {
	const attempts = 100
	for i := range attempts {
		name := owner
		if i == 1 {
			name = owner + "-fork"
		} else if i > 1 {
			name = owner + "-fork-" + strconv.Itoa(i)
		}
		out, err := r.git.Run(ctx, gitDir, "remote", "get-url", "--push", "--all", name)
		if err != nil {
			if _, err := r.git.Run(ctx, gitDir, "remote", "add", name, url); err != nil {
				return "", err
			}
			return name, nil
		}
		if pushesOnlyTo(string(out.Stdout), url) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no free remote name for %s after %d tries", owner, attempts)
}

// setPushRemote points a fork's branch at the fork, so that a push goes there.
func (r *repository) setPushRemote(ctx context.Context, pr *pullRequest, branch, gitDir string) (string, error) {
	head := pr.Head.Repo
	if head == nil || pr.sameRepo {
		return "", nil
	}
	owner := head.Owner.Login
	if !remoteName.MatchString(owner) || !strings.HasPrefix(head.CloneURL, "https://") {
		return "", fmt.Errorf("pull request %d: unusable fork %q at %q", pr.Number, owner, head.CloneURL)
	}
	remote, err := r.forkRemote(ctx, gitDir, owner, head.CloneURL)
	if err != nil {
		return "", err
	}
	_, err = r.git.Run(ctx, gitDir, "config", "branch."+branch+".pushRemote", remote)
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
		if req.Type == "" && len(r.hooks[setupHook]) > 0 {
			types = append(slices.Clone(types), setupHook)
		}
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
