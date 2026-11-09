package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/commitmsg"
	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// Where a commit's message came from.
const (
	MessageGiven     = "given"
	MessageGenerated = "generated"
	MessageFallback  = "fallback"
)

// The parts of a change that Diff can show alone; empty means all of them.
const (
	DiffWorking   = "working"
	DiffCommitted = "committed"
	DiffRemote    = "remote"
)

const subjectsForStyle = "-20"

// restoreTimeout bounds putting a branch back after a refused squash.
const restoreTimeout = 30 * time.Second

// WorktreeCommitRequest asks for one commit in the worktree around Dir.
type WorktreeCommitRequest struct {
	Dir     string `json:"dir"`
	Squash  string `json:"squash,omitempty"` // Folds the commits since this ref's merge base into this one.
	DryRun  bool   `json:"dry_run,omitempty"`
	Message string `json:"message,omitempty"`
	Type    string `json:"type,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

// WorktreeCommitResponse says what was, or on a dry run would be, committed.
type WorktreeCommitResponse struct {
	versioned
	Task    string   `json:"task"`
	Branch  string   `json:"branch"`
	Commit  string   `json:"commit,omitempty"`
	Message string   `json:"message"`
	Source  string   `json:"source"`
	Notice  string   `json:"notice,omitempty"`
	Files   []string `json:"files"`
	DryRun  bool     `json:"dry_run,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
	Command string   `json:"command,omitempty"`
}

// WorktreeDiffRequest asks for the diff of one worktree.
type WorktreeDiffRequest struct {
	Dir      string   `json:"dir"`
	Selector string   `json:"selector,omitempty"` // Empty means the worktree around Dir.
	Mode     string   `json:"mode,omitempty"`
	Args     []string `json:"args,omitempty"` // Passed on to git diff.
}

// WorktreeDiffResponse carries git's output unchanged.
type WorktreeDiffResponse struct {
	versioned
	Task   string `json:"task"`
	Branch string `json:"branch"`
	Path   string `json:"path"`
	Base   string `json:"base,omitempty"` // The commit the diff starts from.
	Output []byte `json:"output"`
	Exit   int    `json:"exit"` // 1 when the arguments asked git to report differences.
}

// commitConfig is the [commit] section. Until the configuration loader lands
// it comes from the same file as the hooks.
type commitConfig struct {
	Stage      string `json:"stage"`
	Generation struct {
		Enabled        *bool  `json:"enabled"`
		Command        string `json:"command"`
		TemplateAppend string `json:"template_append"`
	} `json:"generation"`
}

func loadCommitConfig(folder string) (commitConfig, error) {
	var file struct {
		Commit commitConfig `json:"commit"`
	}
	path := filepath.Join(folder, "herdsman.config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		file.Commit.Stage = "all"
		return file.Commit, nil
	}
	if err != nil {
		return file.Commit, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file.Commit, fmt.Errorf("%s: %w", path, err)
	}
	switch file.Commit.Stage {
	case "":
		file.Commit.Stage = "all"
	case "all", "tracked", "none":
	default:
		return file.Commit, fmt.Errorf("%s: commit.stage is all, tracked or none, not %q", path, file.Commit.Stage)
	}
	return file.Commit, nil
}

// scratchIndex returns a runner on a copy of the worktree's index, so a change
// can be staged for reading without touching what the user has staged.
func scratchIndex(ctx context.Context, git *gitrun.Runner, path string) (*gitrun.Runner, func(), error) {
	out, err := git.Run(ctx, path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return nil, nil, err
	}
	folder, err := os.MkdirTemp("", "herdsman-index-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(folder) }
	copyPath := filepath.Join(folder, "index")
	data, err := os.ReadFile(strings.TrimSpace(string(out.Stdout)))
	if err == nil {
		err = os.WriteFile(copyPath, data, 0o600)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil // A repository with nothing staged yet has no index file.
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &gitrun.Runner{Tracer: git.Tracer, Git: git.Git, Index: copyPath}, cleanup, nil
}

func stage(ctx context.Context, git *gitrun.Runner, path, policy string) error {
	var err error
	switch policy {
	case "all":
		_, err = git.Run(ctx, path, "add", "-A")
	case "tracked":
		_, err = git.Run(ctx, path, "add", "-u")
	}
	return err
}

// headCommit returns the worktree's commit, or "" before the first one.
func headCommit(ctx context.Context, git *gitrun.Runner, path string) string {
	out, err := git.Run(ctx, path, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out.Stdout))
}

func resolveCommit(ctx context.Context, git *gitrun.Runner, path, ref string) (string, error) {
	out, err := git.Run(ctx, path, "rev-parse", "-q", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%q is not a commit here", ref)
	}
	return strings.TrimSpace(string(out.Stdout)), nil
}

func mergeBase(ctx context.Context, git *gitrun.Runner, path, commit, other string) (string, error) {
	out, err := git.Run(ctx, path, "merge-base", commit, other)
	if err != nil {
		return "", fmt.Errorf("%s has no history in common with its base %s: %w", commit, other, err)
	}
	return strings.TrimSpace(string(out.Stdout)), nil
}

// cachedArgs is a diff of the index against base; "" means the empty tree.
func cachedArgs(base string, rest ...string) []string {
	args := []string{"diff", "--cached"}
	if base != "" {
		args = append(args, base)
	}
	return append(args, rest...)
}

// composeInput gathers what the generator reads about the staged change.
func composeInput(ctx context.Context, git *gitrun.Runner, path, base string, files []string) (commitmsg.Input, error) {
	spec := []string{"--", "."}
	for _, file := range files {
		if commitmsg.IsLockFile(file) {
			spec = append(spec, ":(top,literal,exclude)"+file)
		}
	}
	diff, err := git.Run(ctx, path, cachedArgs(base, spec...)...)
	if err != nil {
		return commitmsg.Input{}, err
	}
	stat, err := git.Run(ctx, path, cachedArgs(base, "--stat")...)
	if err != nil {
		return commitmsg.Input{}, err
	}
	input := commitmsg.Input{Diff: string(diff.Stdout), Stat: string(stat.Stdout)}
	if log, err := git.Run(ctx, path, "log", subjectsForStyle, "--format=%s"); err == nil {
		if text := strings.TrimSpace(string(log.Stdout)); text != "" {
			input.Subjects = strings.Split(text, "\n")
		}
	}
	return input, nil
}

// writeMessage picks the message: the caller's, the generator's, or, when the
// generator cannot answer, the fallback. An answer that is not a valid message
// after the retry stops the commit.
func writeMessage(ctx context.Context, git *gitrun.Runner, req WorktreeCommitRequest, cfg commitConfig, branch, path, base string, files []string, resp *WorktreeCommitResponse) error {
	pin := commitmsg.Pin{Type: req.Type, Scope: req.Scope}
	if req.Message != "" {
		message, err := commitmsg.Parse(req.Message)
		if err != nil {
			return err
		}
		resp.Message, resp.Source = message.String(), MessageGiven
		return nil
	}
	useFallback := func(notice string) {
		resp.Message, resp.Source, resp.Notice = commitmsg.Fallback(branch, files, pin).String(), MessageFallback, notice
	}
	if cfg.Generation.Enabled != nil && !*cfg.Generation.Enabled {
		useFallback("generation is off; wrote the deterministic message")
		return nil
	}
	input, err := composeInput(ctx, git, path, base, files)
	if err != nil {
		return err
	}
	input.Branch, input.Target, input.Append, input.Pin = branch, req.Squash, cfg.Generation.TemplateAppend, pin
	resp.Prompt = commitmsg.Prompt(input)
	generator := &commitmsg.Generator{Command: cfg.Generation.Command, Dir: path}
	resp.Command, _ = generator.Resolve()
	message, err := generator.Generate(ctx, resp.Prompt, pin)
	var refused *commitmsg.UnparsableError
	switch {
	case errors.As(err, &refused):
		return fmt.Errorf("%w; nothing was committed, pass -m to write the message yourself", err)
	case err != nil:
		useFallback(err.Error() + "; wrote the deterministic message")
	default:
		resp.Message, resp.Source = message.String(), MessageGenerated
	}
	return nil
}

// checkCommitRequest refuses what no repository state could make valid.
func checkCommitRequest(req WorktreeCommitRequest) error {
	if req.Message != "" && (req.Type != "" || req.Scope != "") {
		return errors.New("-m already names the type and scope; drop --type and --scope")
	}
	return commitmsg.Pin{Type: req.Type, Scope: req.Scope}.Validate()
}

// Commit stages by policy, writes the message and commits in the worktree
// around Dir. A dry run stages into a scratch index and changes nothing.
func (s *WorktreeService) Commit(ctx context.Context, req WorktreeCommitRequest) (WorktreeCommitResponse, error) {
	resp := WorktreeCommitResponse{DryRun: req.DryRun}
	if err := checkCommitRequest(req); err != nil {
		return resp, err
	}
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	target, err := wtree.ResolveSelector(wtree.Selector{Kind: wtree.SelectCurrent}, r.selectContext(""))
	if err != nil {
		return resp, err
	}
	resp.Task, resp.Branch = target.Task, target.Branch
	if req.Squash != "" && target.IsDefault {
		return resp, fmt.Errorf("--squash would rewrite the default branch %s", target.Branch)
	}
	cfg, err := loadCommitConfig(filepath.Dir(r.main))
	if err != nil {
		return resp, err
	}
	kind, _ := commitmsg.BranchParts(target.Branch)
	vars := r.vars(target.Task, target.Branch, target.Path, kind, req.Squash, target.Head)
	git := r.git
	if req.DryRun {
		scratch, cleanup, err := scratchIndex(ctx, r.git, target.Path)
		if err != nil {
			return resp, err
		}
		defer cleanup()
		git = scratch
	} else {
		before, err := s.plan(r, "pre-commit", vars)
		if err != nil {
			return resp, err
		}
		if _, err := s.plan(r, "post-commit", vars); err != nil {
			return resp, err // Refused now, not after the commit exists.
		}
		if _, err := r.runner(vars, target.Path).Run(ctx, "pre-commit", before); err != nil {
			return resp, err
		}
	}
	if err := stage(ctx, git, target.Path, cfg.Stage); err != nil {
		return resp, err
	}
	head := headCommit(ctx, git, target.Path)
	base, err := squashBase(ctx, git, target.Path, head, req.Squash)
	if err != nil {
		return resp, err
	}
	names, err := git.Run(ctx, target.Path, cachedArgs(base, "--name-only", "-z")...)
	if err != nil {
		return resp, err
	}
	if len(names.Stdout) == 0 {
		return resp, errors.New("nothing to commit")
	}
	resp.Files = strings.Split(strings.TrimRight(string(names.Stdout), "\x00"), "\x00")
	if err := writeMessage(ctx, git, req, cfg, target.Branch, target.Path, base, resp.Files, &resp); err != nil {
		return resp, err
	}
	if req.DryRun {
		return resp, nil
	}
	if resp.Commit, err = record(ctx, r.git, target.Path, head, base, req.Squash != "", resp.Message); err != nil {
		return resp, err
	}
	vars = r.vars(target.Task, target.Branch, target.Path, kind, req.Squash, resp.Commit)
	after, err := s.plan(r, "post-commit", vars)
	if err != nil {
		return resp, err
	}
	_, err = r.runner(vars, target.Path).Start("post-commit", after)
	return resp, err
}

// squashBase is what the commit will be compared with: HEAD, or for a squash
// the point where the branch left the target.
func squashBase(ctx context.Context, git *gitrun.Runner, path, head, squash string) (string, error) {
	if squash == "" {
		return head, nil
	}
	if head == "" {
		return "", errors.New("--squash needs at least one commit")
	}
	other, err := resolveCommit(ctx, git, path, squash)
	if err != nil {
		return "", err
	}
	return mergeBase(ctx, git, path, head, other)
}

// record makes the commit. A squash moves the branch back first and puts it
// where it was if git then refuses the commit.
func record(ctx context.Context, git *gitrun.Runner, path, head, base string, squash bool, message string) (string, error) {
	if squash {
		if _, err := git.Run(ctx, path, "reset", "-q", "--soft", base); err != nil {
			return "", err
		}
	}
	if _, err := git.Run(ctx, path, "commit", "-q", "-m", message); err != nil {
		if squash {
			if restoreErr := restoreBranch(ctx, git, path, head); restoreErr != nil {
				return "", errors.Join(err, restoreErr)
			}
		}
		return "", err
	}
	return headCommit(ctx, git, path), nil
}

// restoreBranch moves the branch back to head and checks that it arrived. It
// outlives the caller's context: a cancelled commit must not strand the branch.
func restoreBranch(ctx context.Context, git *gitrun.Runner, path, head string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	_, err := git.Run(cleanup, path, "reset", "-q", "--soft", head)
	if err == nil && headCommit(cleanup, git, path) != head {
		err = errors.New("the branch did not move")
	}
	if err != nil {
		return fmt.Errorf("the branch was left behind its commits; recover with `git reset --soft %s`: %w", head, err)
	}
	return nil
}

// diffBase finds the commit a task's branch started from: the merge base with
// the default branch, preferring the remote's copy as `new` does.
func (r *repository) diffBase(ctx context.Context, path, head string) (string, error) {
	for _, candidate := range []string{"refs/remotes/origin/" + r.dflt, "refs/heads/" + r.dflt} {
		if other, err := resolveCommit(ctx, r.git, path, candidate); err == nil {
			return mergeBase(ctx, r.git, path, head, other)
		}
	}
	return "", fmt.Errorf("the default branch %q does not exist", r.dflt)
}

func upstreamBase(ctx context.Context, git *gitrun.Runner, path, head string) (string, error) {
	other, err := resolveCommit(ctx, git, path, "@{upstream}")
	if err != nil {
		return "", errors.New("this branch has no upstream to compare with")
	}
	return mergeBase(ctx, git, path, head, other)
}

// Diff runs git's own diff for one view of a worktree and returns its bytes.
func (s *WorktreeService) Diff(ctx context.Context, req WorktreeDiffRequest) (WorktreeDiffResponse, error) {
	var resp WorktreeDiffResponse
	r, err := s.open(ctx, req.Dir)
	if err != nil {
		return resp, err
	}
	defer r.close()
	selector := wtree.Selector{Kind: wtree.SelectCurrent}
	if req.Selector != "" {
		if selector, err = wtree.ParseSelector(req.Selector); err != nil {
			return resp, err
		}
	}
	previous, err := r.state.Previous(ctx)
	if err != nil {
		return resp, err
	}
	target, err := wtree.ResolveSelector(selector, r.selectContext(previous))
	if err != nil {
		return resp, err
	}
	resp.Task, resp.Branch, resp.Path = target.Task, target.Branch, target.Path
	head := headCommit(ctx, r.git, target.Path)
	git := r.git
	var args []string
	switch req.Mode {
	case DiffCommitted, DiffRemote:
		if head == "" {
			return resp, errors.New("this worktree has no commits yet")
		}
		find := r.diffBase
		if req.Mode == DiffRemote {
			find = func(ctx context.Context, path, head string) (string, error) {
				return upstreamBase(ctx, r.git, path, head)
			}
		}
		if resp.Base, err = find(ctx, target.Path, head); err != nil {
			return resp, err
		}
		args = append([]string{"diff", resp.Base, head}, req.Args...)
	case "", DiffWorking:
		resp.Base = head
		if req.Mode == "" && head != "" {
			if resp.Base, err = r.diffBase(ctx, target.Path, head); err != nil {
				return resp, err
			}
		}
		scratch, cleanup, err := scratchIndex(ctx, r.git, target.Path)
		if err != nil {
			return resp, err
		}
		defer cleanup()
		if err := stage(ctx, scratch, target.Path, "all"); err != nil {
			return resp, err
		}
		git, args = scratch, cachedArgs(resp.Base, req.Args...)
	default:
		return resp, fmt.Errorf("no diff mode %q", req.Mode)
	}
	result, err := git.Run(ctx, target.Path, args...)
	var exit *gitrun.ExitError
	if errors.As(err, &exit) && exit.Code == 1 && len(bytes.TrimSpace(result.Stderr)) == 0 {
		err = nil // --exit-code and --quiet report differences this way.
	}
	resp.Output, resp.Exit = result.Stdout, result.Exit
	return resp, err
}
