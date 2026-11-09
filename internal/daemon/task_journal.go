package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

const (
	effectTree   = "wtree.tree"
	effectRecord = "wtree.record"
)

// treeSpec is what a worktree's creation needs, kept in the journal so that a
// restarted daemon can finish it with no caller present.
type treeSpec struct {
	Main     string `json:"main"`
	Task     string `json:"task"`
	Branch   string `json:"branch"`
	Path     string `json:"path"`
	Start    string `json:"start,omitempty"`    // Empty: the branch exists.
	Identity string `json:"identity,omitempty"` // The pull request it was made for.
	GitOnly  bool   `json:"git_only,omitempty"`
	Socket   string `json:"socket,omitempty"` // Where the caller's herdr listens.
	OwnClone bool   `json:"own_clone,omitempty"` // The restricted profile: a clone of its own, not a worktree.
}

// applyTree makes the worktree, through herdr unless the spec says git.
func (s *WorktreeService) applyTree(ctx context.Context, r *repository, spec treeSpec, resp *WorktreeNewResponse) error {
	if spec.OwnClone {
		return r.cloneTask(ctx, spec.Branch, spec.Path, spec.Start)
	}
	if !spec.GitOnly {
		return s.create(ctx, r, taskShape{}, spec.Task, spec.Branch, spec.Path, spec.Start, resp)
	}
	args := []string{"worktree", "add", "-q", spec.Path, spec.Branch}
	if spec.Start != "" {
		args = []string{"worktree", "add", "-q", "-b", spec.Branch, spec.Path, spec.Start}
	}
	_, err := r.git.Run(ctx, r.main, args...)
	return err
}

// applyRecord writes down what the worktree is.
func applyRecord(ctx context.Context, r *repository, spec treeSpec) error {
	if spec.Identity != "" {
		if _, err := r.git.Run(ctx, r.main, "config", requestMarker(spec.Branch), spec.Identity); err != nil {
			return err
		}
	}
	return r.state.PutWorktree(ctx, wtree.Worktree{Task: spec.Task, Branch: spec.Branch, Path: spec.Path, CreatedAt: time.Now()})
}

// probeClone says whether the task's own clone is there and on its branch.
func probeClone(ctx context.Context, r *repository, spec treeSpec) (ops.Verdict, string, error) {
	if _, err := os.Lstat(spec.Path); errors.Is(err, fs.ErrNotExist) {
		return ops.Absent, "no clone at " + spec.Path, nil
	} else if err != nil {
		return ops.Unknown, "", err
	}
	out, err := r.git.Run(ctx, spec.Path, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return ops.Different, spec.Path + " exists and is not a clone on " + spec.Branch, nil
	}
	if strings.TrimSpace(string(out.Stdout)) != spec.Branch {
		return ops.Different, spec.Path + " is on another branch", nil
	}
	return ops.Matches, "", nil
}

// probeTree says whether the worktree the spec names is there, and is that one.
func probeTree(ctx context.Context, r *repository, spec treeSpec) (ops.Verdict, string, error) {
	if spec.OwnClone {
		return probeClone(ctx, r, spec)
	}
	out, err := r.git.Run(ctx, r.main, "worktree", "list", "--porcelain")
	if err != nil {
		return ops.Unknown, "", err
	}
	for block := range strings.SplitSeq(string(out.Stdout), "\n\n") {
		if !strings.Contains(block+"\n", "worktree "+spec.Path+"\n") {
			continue
		}
		if strings.Contains(block+"\n", "branch refs/heads/"+spec.Branch+"\n") {
			return ops.Matches, "", nil
		}
		return ops.Different, spec.Path + " is a worktree of another branch", nil
	}
	if _, err := os.Lstat(spec.Path); errors.Is(err, fs.ErrNotExist) {
		return ops.Absent, "no worktree at " + spec.Path, nil
	} else if err != nil {
		return ops.Unknown, "", err
	}
	return ops.Different, spec.Path + " exists and is not a worktree", nil
}

func probeRecord(ctx context.Context, r *repository, spec treeSpec) (ops.Verdict, string, error) {
	rows, err := r.state.Worktrees(ctx)
	if err != nil {
		return ops.Unknown, "", err
	}
	for _, row := range rows {
		if row.Task == spec.Task {
			if row.Path == spec.Path {
				return ops.Matches, "", nil
			}
			return ops.Different, "task " + spec.Task + " is recorded at " + row.Path, nil
		}
	}
	return ops.Absent, "no record of " + spec.Task, nil
}

// taskJournal runs worktree creation as journaled operations.
type taskJournal struct {
	engine func() *ops.Engine
	// pending holds the response of a call in progress, by step key.
	pending sync.Map
}

func (d *Daemon) taskJournal() *taskJournal {
	d.tasksOnce.Do(func() { d.tasks = &taskJournal{engine: func() *ops.Engine { return d.ops }} })
	return d.tasks
}

type taskEffect struct {
	journal *taskJournal
	apply   func(context.Context, *WorktreeService, *repository, treeSpec, ops.Step, *taskJournal) error
	probe   func(context.Context, *repository, treeSpec) (ops.Verdict, string, error)
}

func (e taskEffect) open(ctx context.Context, st ops.Step) (*WorktreeService, *repository, treeSpec, error) {
	var spec treeSpec
	if err := json.Unmarshal([]byte(st.Spec), &spec); err != nil {
		return nil, nil, spec, err
	}
	service := &WorktreeService{HerdrSocket: spec.Socket}
	r, err := service.open(ctx, spec.Main)
	return service, r, spec, err
}

// Apply implements ops.Effect.
func (e taskEffect) Apply(ctx context.Context, st ops.Step) error {
	service, r, spec, err := e.open(ctx, st)
	if err != nil {
		return err
	}
	defer r.close()
	return e.apply(ctx, service, r, spec, st, e.journal)
}

// Probe implements ops.Effect.
func (e taskEffect) Probe(ctx context.Context, st ops.Step) (ops.Verdict, string, error) {
	_, r, spec, err := e.open(ctx, st)
	if err != nil {
		return ops.Unknown, "", err
	}
	defer r.close()
	return e.probe(ctx, r, spec)
}

func (j *taskJournal) effects() map[string]ops.Effect {
	tree := func(ctx context.Context, service *WorktreeService, r *repository, spec treeSpec, st ops.Step, j *taskJournal) error {
		resp := &WorktreeNewResponse{}
		if waiting, ok := j.pending.LoadAndDelete(st.Key); ok {
			resp = waiting.(*WorktreeNewResponse)
		}
		return service.applyTree(ctx, r, spec, resp)
	}
	record := func(ctx context.Context, _ *WorktreeService, r *repository, spec treeSpec, _ ops.Step, _ *taskJournal) error {
		return applyRecord(ctx, r, spec)
	}
	return map[string]ops.Effect{
		effectTree:   taskEffect{journal: j, apply: tree, probe: probeTree},
		effectRecord: taskEffect{journal: j, apply: record, probe: probeRecord},
	}
}

// herdrSocket is where the caller's herdr listens; a path nothing listens on
// when that cannot be told, so a recovery never reaches some other herdr.
func (s *WorktreeService) herdrSocket(r *repository) string {
	if s.HerdrSocket != "" {
		return s.HerdrSocket
	}
	socket, err := herdr.SocketPathFor(s.getenv)
	if err != nil {
		return filepath.Join(r.common, "no-herdr.sock")
	}
	return socket
}

// makeTree makes and records the worktree, under an operation when a daemon
// serves the call.
func (s *WorktreeService) makeTree(ctx context.Context, r *repository, spec treeSpec, resp *WorktreeNewResponse) error {
	if s.journal == nil {
		if err := s.applyTree(ctx, r, spec, resp); err != nil {
			return err
		}
		if err := applyRecord(ctx, r, spec); err != nil {
			return fmt.Errorf("worktree %s was created, but: %w", spec.Path, err)
		}
		return nil
	}
	spec.Socket = s.herdrSocket(r)
	return s.journal.run(ctx, r, spec, resp)
}

func (j *taskJournal) run(ctx context.Context, r *repository, spec treeSpec, resp *WorktreeNewResponse) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	engine := j.engine()
	treeKey, recordKey := "wtree-tree:"+spec.Path, "wtree-record:"+spec.Path
	op, err := engine.Submit(ctx, ops.Spec{
		Kind: "task.new", Plan: "make the worktree of " + spec.Task,
		Locks: []string{ops.TaskLock(r.common + ":" + spec.Task)},
		Steps: []ops.StepSpec{
			{Name: "worktree", Effect: effectTree, Key: treeKey, Spec: string(data), Retry: ops.RetryIdempotent},
			{Name: "record", Effect: effectRecord, Key: recordKey, Spec: string(data), Retry: ops.RetryIdempotent},
		},
	})
	if err != nil {
		return err
	}
	j.pending.Store(treeKey, resp)
	defer j.pending.Delete(treeKey)
	op, err = engine.Drive(ctx, op.ID)
	if err != nil {
		return err
	}
	if op.State != ops.OpDone {
		return fmt.Errorf("the worktree of %s was not made (%s): %s", spec.Task, op.State, op.Reason)
	}
	return nil
}
