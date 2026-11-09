package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// The effect names of the recovery steps.
const (
	EffectSnapshot = "snapshot.take"
	effectPrune    = "snapshot.prune"
	effectUndo     = "undo.restore"
	effectExport   = "account.export"
)

// The retention defaults.
const (
	DefaultUndoRetention       = 30 * 24 * time.Hour
	DefaultSnapshotCap   int64 = 20 << 30
)

// ErrCapFull means a snapshot would not fit under the cap. Older snapshots
// are never dropped to make room: the operation asking for one refuses.
var ErrCapFull = errors.New("snapshot cap full")

// Retention bounds the snapshots an account keeps.
type Retention struct {
	Undo time.Duration
	Cap  int64
}

// WithDefaults fills every unset field.
func (r Retention) WithDefaults() Retention {
	if r.Undo == 0 {
		r.Undo = DefaultUndoRetention
	}
	if r.Cap == 0 {
		r.Cap = DefaultSnapshotCap
	}
	return r
}

// SnapshotRecord is one row of the snapshot table.
type SnapshotRecord struct {
	ID      string `json:"id"`
	Task    string `json:"task"`
	Dir     string `json:"dir"`
	Repo    string `json:"repo,omitempty"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

// RecoveryOptions configures a Recovery.
type RecoveryOptions struct {
	StateDir  string
	Retention Retention
	// Volumes is the container runtime; nil refuses a snapshot with volumes.
	Volumes VolumeStore
	// Quiesce stops every writer the journal does not cover and returns the
	// call that lets them go. Export holds it for the whole copy. A writer
	// held back by it must not be waiting on a journal lock, or the two wait
	// for each other.
	Quiesce func() (release func())
}

// Recovery is undo, retention, export and import over one engine.
type Recovery struct {
	opt RecoveryOptions
	eng *Engine
}

// NewRecovery makes a Recovery; Attach gives it its engine.
func NewRecovery(o RecoveryOptions) *Recovery {
	o.Retention = o.Retention.WithDefaults()
	return &Recovery{opt: o}
}

// Attach binds the engine that was built with Effects.
func (r *Recovery) Attach(e *Engine) { r.eng = e }

// Effects are the step kinds to register with the engine.
func (r *Recovery) Effects() map[string]Effect {
	return map[string]Effect{
		EffectSnapshot: snapshotEffect{r},
		effectPrune:    pruneEffect{r},
		effectUndo:     undoEffect{r},
		effectExport:   exportEffect{r},
	}
}

// OperationError is an operation that ended without finishing its work.
type OperationError struct{ Op *Operation }

func (e *OperationError) Error() string {
	return fmt.Sprintf("operation %s %s: %s", e.Op.ID, e.Op.State, e.Op.Reason)
}

func finished(op *Operation, err error) (*Operation, error) {
	if err != nil {
		return op, err
	}
	if op.State != OpDone {
		return op, &OperationError{Op: op}
	}
	return op, nil
}

// run journals spec, waiting its turn, and drives it to its end.
func (r *Recovery) run(ctx context.Context, spec Spec) (*Operation, error) {
	op, err := r.eng.SubmitWaiting(ctx, spec)
	if err != nil {
		return nil, err
	}
	return finished(r.eng.Drive(ctx, op.ID))
}

// SnapshotRequest says what to capture.
type SnapshotRequest struct {
	Task        string   `json:"task"`
	Worktree    string   `json:"worktree"`
	Rebuildable []string `json:"rebuildable,omitempty"`
	FileCap     int64    `json:"file_cap,omitempty"`
}

// SnapshotStep is the step a destructive operation puts before the steps that
// need the snapshot: when it refuses, they do not run.
func SnapshotStep(q SnapshotRequest) StepSpec {
	spec, err := json.Marshal(q)
	if err != nil {
		panic(err) // strings and integers always marshal
	}
	return StepSpec{Name: "snapshot " + q.Task, Effect: EffectSnapshot, Key: "snapshot:" + q.Task, Spec: string(spec), Retry: RetryIdempotent}
}

// Snapshot takes one snapshot as an operation of its own.
func (r *Recovery) Snapshot(ctx context.Context, q SnapshotRequest) (*Operation, error) {
	return r.run(ctx, Spec{Kind: "snapshot", Plan: "snapshot " + q.Task, Locks: []string{TaskLock(q.Task)}, Steps: []StepSpec{SnapshotStep(q)}})
}

// Snapshots lists every snapshot, oldest first.
func (r *Recovery) Snapshots() ([]SnapshotRecord, error) {
	var out []SnapshotRecord
	err := r.eng.Read(func(c store.Conn) error {
		var err error
		out, err = loadSnapshots(c)
		return err
	})
	return out, err
}

func loadSnapshots(c store.Conn) ([]SnapshotRecord, error) {
	out := []SnapshotRecord{}
	err := c.Query("SELECT id, task, dir, repo, size, created FROM snapshots ORDER BY created, id", nil, func(row store.Row) error {
		out = append(out, SnapshotRecord{ID: row.Text(0), Task: row.Text(1), Dir: row.Text(2), Repo: row.Text(3), Size: row.Int64(4), Created: row.Int64(5)})
		return nil
	})
	return out, err
}

func (r *Recovery) snapshotByID(id string) (*SnapshotRecord, error) {
	list, err := r.Snapshots()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("%w: snapshot %s", ErrNotFound, id)
}

// capError says why a snapshot does not fit and what could be pruned.
func capError(limit, used, need int64, oldest []SnapshotRecord) error {
	names := make([]string, 0, len(oldest))
	for _, s := range oldest {
		names = append(names, s.ID)
	}
	return fmt.Errorf("%w: the cap is %d bytes, %d are used and %d more are needed; nothing was dropped, prune one of (oldest first): %s",
		ErrCapFull, limit, used, need, strings.Join(names, ", "))
}

// admit refuses when the snapshots already held plus need exceed the cap, or
// when the cap is already reached and a snapshot of any size could not fit.
func admit(c store.Conn, limit, need int64) error {
	list, err := loadSnapshots(c)
	if err != nil {
		return err
	}
	var used int64
	for _, s := range list {
		used += s.Size
	}
	if used+need > limit || used >= limit {
		return capError(limit, used, need, list)
	}
	return nil
}

type snapshotEffect struct{ r *Recovery }

func (fx snapshotEffect) has(id string) (bool, error) {
	_, err := fx.r.snapshotByID(id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Apply starts by clearing what an interrupted attempt left: the folder is
// named after this operation, so nothing else can own it.
func (fx snapshotEffect) Apply(ctx context.Context, st Step) error {
	var q SnapshotRequest
	if err := json.Unmarshal([]byte(st.Spec), &q); err != nil {
		return err
	}
	if done, err := fx.has(st.OpID); done || err != nil {
		return err
	}
	r := fx.r
	if err := r.eng.tx(func(c store.Conn) error { return admit(c, r.opt.Retention.Cap, 0) }); err != nil {
		return err
	}
	common, err := runGit(ctx, q.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	dest := filepath.Join(common, "herdsman", "snapshots", st.OpID)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	m, err := snapshot.Take(ctx, snapshot.Options{Worktree: q.Worktree, Dest: dest, Task: q.Task, Op: st.OpID, Rebuildable: q.Rebuildable, FileCap: q.FileCap})
	if err != nil {
		return err
	}
	size, err := treeSize(dest)
	if err != nil {
		return err
	}
	err = r.eng.tx(func(c store.Conn) error {
		if err := admit(c, r.opt.Retention.Cap, size); err != nil {
			return err
		}
		_, err := c.Exec("INSERT INTO snapshots(id, task, dir, repo, size, created) VALUES (?,?,?,?,?,?)", st.OpID, q.Task, dest, common, size, r.eng.now())
		return err
	})
	if err != nil {
		_, refErr := runGit(ctx, common, "update-ref", "-d", m.SnapshotRef)
		return errors.Join(err, refErr, os.RemoveAll(dest))
	}
	return nil
}

// Register records a snapshot taken outside a snapshot step, so that undo and
// retention know it. content is its folder as it is now, for the cap's sake.
// Registering the same id again changes nothing.
func (r *Recovery) Register(rec SnapshotRecord, content string) error {
	if _, err := r.snapshotByID(rec.ID); err == nil || !errors.Is(err, ErrNotFound) {
		return err
	}
	size, err := treeSize(content)
	if err != nil {
		return err
	}
	return r.eng.tx(func(c store.Conn) error {
		if err := admit(c, r.opt.Retention.Cap, size); err != nil {
			return err
		}
		_, err := c.Exec("INSERT INTO snapshots(id, task, dir, repo, size, created) VALUES (?,?,?,?,?,?)", rec.ID, rec.Task, rec.Dir, rec.Repo, size, r.eng.now())
		return err
	})
}

// Probe trusts the row alone: it is written after the folder is complete, and
// a folder without one is a partial attempt that Apply clears.
func (fx snapshotEffect) Probe(_ context.Context, st Step) (Verdict, string, error) {
	done, err := fx.has(st.OpID)
	if err != nil {
		return Unknown, "", err
	}
	if done {
		return Matches, "the snapshot is recorded", nil
	}
	return Absent, "no snapshot is recorded", nil
}

func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Sweep prunes every snapshot older than the retention period. The cap plays
// no part: only age removes a snapshot without someone asking.
func (r *Recovery) Sweep(ctx context.Context) ([]string, error) {
	list, err := r.Snapshots()
	if err != nil {
		return nil, err
	}
	cutoff := r.eng.opt.Now().Add(-r.opt.Retention.Undo).Unix()
	var ids, locks []string
	var steps []StepSpec
	for _, s := range list {
		if s.Created >= cutoff {
			continue
		}
		ids = append(ids, s.ID)
		locks = append(locks, SnapshotLock(s.ID))
		steps = append(steps, StepSpec{Name: "prune " + s.ID, Effect: effectPrune, Key: "prune:" + s.ID, Spec: s.ID, Retry: RetryIdempotent})
	}
	if len(steps) == 0 {
		return nil, nil
	}
	if _, err := r.run(ctx, Spec{Kind: "retention.sweep", Plan: fmt.Sprintf("prune %d expired snapshots", len(steps)), Locks: locks, Steps: steps}); err != nil {
		return nil, err
	}
	return ids, nil
}

type pruneEffect struct{ r *Recovery }

// Apply removes the folder before the row, so an interruption leaves a row
// that the retry finishes rather than a folder nothing points at.
func (fx pruneEffect) Apply(ctx context.Context, st Step) error {
	rec, err := fx.r.snapshotByID(st.Spec)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if m, err := snapshot.ReadManifest(rec.Dir); err == nil && rec.Repo != "" {
		// The repository may be gone; the ref is only reachable through it.
		_, _ = runGit(ctx, rec.Repo, "update-ref", "-d", m.SnapshotRef)
	}
	if err := os.RemoveAll(rec.Dir); err != nil {
		return err
	}
	return fx.r.eng.tx(func(c store.Conn) error {
		_, err := c.Exec("DELETE FROM snapshots WHERE id = ?", st.Spec)
		return err
	})
}

// Probe is Matches once the row is gone; otherwise the retry finishes it.
func (fx pruneEffect) Probe(_ context.Context, st Step) (Verdict, string, error) {
	_, err := fx.r.snapshotByID(st.Spec)
	if errors.Is(err, ErrNotFound) {
		return Matches, "the snapshot is gone", nil
	}
	if err != nil {
		return Unknown, "", err
	}
	return Unknown, "the snapshot is still recorded", nil
}

// runGit runs git in dir with hooks off and no inherited repository variables.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}
