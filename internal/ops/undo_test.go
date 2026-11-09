package ops_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// rig is an engine with the recovery effects over one state directory.
type rig struct {
	state string
	db    store.Conn
	eng   *ops.Engine
	rec   *ops.Recovery
}

type rigOptions struct {
	retention ops.Retention
	volumes   ops.VolumeStore
	now       func() time.Time
	fault     func(string)
	extra     map[string]ops.Effect
	quiesce   func() func()
	policy    func() (ops.Retention, error)
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	return openRig(t, filepath.Join(t.TempDir(), "state"), o)
}

func openRig(t *testing.T, state string, o rigOptions) *rig {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(state, "daemon"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "daemon", "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rec := ops.NewRecovery(ops.RecoveryOptions{StateDir: state, Retention: o.retention, Volumes: o.volumes, Quiesce: o.quiesce, Policy: o.policy})
	effects := rec.Effects()
	for k, v := range o.extra {
		effects[k] = v
	}
	eng := ops.NewEngine(db, ops.Options{Effects: effects, Now: o.now, Fault: o.fault})
	rec.Attach(eng)
	return &rig{state: state, db: db, eng: eng, rec: rec}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dirtyWorktree is a linked worktree on branch feat/x with staged, unstaged,
// untracked and ignored changes.
func dirtyWorktree(t *testing.T) (repo, tree string) {
	t.Helper()
	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(base, "repo")
	tree = filepath.Join(base, "tree")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	write(t, filepath.Join(repo, "b.txt"), "two\n")
	write(t, filepath.Join(repo, ".gitignore"), ".env\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first")
	git(t, repo, "worktree", "add", "-q", "-b", "feat/x", tree)
	write(t, filepath.Join(tree, "a.txt"), "staged\n")
	git(t, tree, "add", "a.txt")
	write(t, filepath.Join(tree, "a.txt"), "unstaged\n")
	write(t, filepath.Join(tree, "new.txt"), "untracked\n")
	write(t, filepath.Join(tree, ".env"), "SECRET=1\n")
	return repo, tree
}

func snapshotOf(t *testing.T, r *rig, task, tree string) string {
	t.Helper()
	op, err := r.rec.Snapshot(context.Background(), ops.SnapshotRequest{Task: task, Worktree: tree})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return op.ID
}

func removeWorktree(t *testing.T, repo, tree string) {
	t.Helper()
	git(t, repo, "worktree", "remove", "--force", tree)
	git(t, repo, "branch", "-D", "feat/x")
}

func assertRestored(t *testing.T, tree string) {
	t.Helper()
	for name, want := range map[string]string{"a.txt": "unstaged\n", "b.txt": "two\n", "new.txt": "untracked\n", ".env": "SECRET=1\n"} {
		if got := read(t, filepath.Join(tree, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := git(t, tree, "symbolic-ref", "HEAD"); got != "refs/heads/feat/x" {
		t.Errorf("HEAD = %s", got)
	}
	if got := git(t, tree, "show", ":a.txt"); got != "staged" {
		t.Errorf("staged a.txt = %q", got)
	}
	if got := git(t, tree, "check-ignore", ".env"); got != ".env" {
		t.Errorf(".env is not ignored: %q", got)
	}
}

func TestUndoRestoresToTheOriginalPathAndRelinksTheBranch(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	head := git(t, tree, "rev-parse", "HEAD")
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)

	res, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != tree || !res.Original {
		t.Fatalf("restored to %s (original %v), want %s", res.Target, res.Original, tree)
	}
	assertRestored(t, tree)
	if got := git(t, repo, "rev-parse", "refs/heads/feat/x"); got != head {
		t.Errorf("branch at %s, want %s", got, head)
	}
	if list := git(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(list, "worktree "+tree+"\n") {
		t.Errorf("the restored tree is not a worktree of the repository:\n%s", list)
	}
	// The worktree is a working one: git sees the same dirty state.
	if got := git(t, tree, "status", "--porcelain"); !strings.Contains(got, "MM a.txt") || !strings.Contains(got, "?? new.txt") {
		t.Errorf("status:\n%s", got)
	}
	if res, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: tree}); err == nil {
		t.Fatalf("a second undo onto the live tree succeeded: %+v", res)
	}
}

func TestUndoGoesToAFreshPathWhenTheOriginalIsTaken(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	write(t, filepath.Join(tree, "someone-else.txt"), "keep me\n")

	res, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target == tree || res.Original {
		t.Fatalf("restored over the taken path: %+v", res)
	}
	assertRestored(t, res.Target)
	if got := read(t, filepath.Join(tree, "someone-else.txt")); got != "keep me\n" {
		t.Errorf("the occupant changed: %q", got)
	}
}

func TestUndoRefusesAnOccupiedExplicitTarget(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	target := filepath.Join(t.TempDir(), "taken")
	write(t, filepath.Join(target, "x"), "x")
	_, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: target})
	if !errors.Is(err, snapshot.ErrTargetOccupied) || !strings.Contains(err.Error(), target) {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(target, "x")); got != "x" {
		t.Errorf("occupant changed: %q", got)
	}
}

func TestUndoStopsWhenTheBranchMovedNamingBothSides(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	head := git(t, tree, "rev-parse", "HEAD")
	id := snapshotOf(t, r, "t1", tree)
	git(t, repo, "worktree", "remove", "--force", tree)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "second")
	moved := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "branch", "-f", "feat/x", moved)

	_, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	if err == nil || !strings.Contains(err.Error(), moved) || !strings.Contains(err.Error(), head) {
		t.Fatalf("err = %v, want both %s and %s", err, moved, head)
	}
	if got := git(t, repo, "rev-parse", "refs/heads/feat/x"); got != moved {
		t.Errorf("branch moved to %s", got)
	}
	if _, serr := os.Stat(tree); !os.IsNotExist(serr) {
		t.Errorf("a tree appeared: %v", serr)
	}
}

func TestUndoStandaloneWhenTheRepositoryIsGone(t *testing.T) {
	src := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, src, "t1", tree)
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := src.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "imported")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	removeWorktree(t, repo, tree)
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	r := openRig(t, state, rigOptions{})
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: target}); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, target)
}

func TestUndoOfAnUnknownSnapshot(t *testing.T) {
	r := newRig(t, rigOptions{})
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: "nope"}); !errors.Is(err, ops.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoRefusesACorruptSnapshotBeforeAnyEffect(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	rec := snapshotRecord(t, r, id)
	if err := os.WriteFile(filepath.Join(rec.Dir, snapshot.BundleName), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	var ve *snapshot.VerifyError
	if !errors.As(err, &ve) && !strings.Contains(err.Error(), "bundle") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(tree); !os.IsNotExist(serr) {
		t.Errorf("a tree appeared: %v", serr)
	}
}

func snapshotRecord(t *testing.T, r *rig, id string) ops.SnapshotRecord {
	t.Helper()
	list, err := r.rec.Snapshots()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no snapshot %s in %+v", id, list)
	return ops.SnapshotRecord{}
}

// memVolumes is a volume runtime in memory.
type memVolumes struct{ data map[string]string }

func (m *memVolumes) Compare(_ context.Context, name, archive string) (bool, bool, error) {
	got, ok := m.data[name]
	if !ok {
		return false, false, nil
	}
	b, err := os.ReadFile(archive)
	return ok, got == string(b), err
}

func (m *memVolumes) Import(_ context.Context, name, archive string) error {
	b, err := os.ReadFile(archive)
	m.data[name] = string(b)
	return err
}

func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func addResources(t *testing.T, dir string, res ops.Resources, archives map[string][]byte) {
	t.Helper()
	for name, b := range archives {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ops.ResourcesName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestUndoReimportsScratchAndVolumes(t *testing.T) {
	vols := &memVolumes{data: map[string]string{}}
	r := newRig(t, rigOptions{volumes: vols})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	scratchTar := tarOf(t, map[string]string{"cache/note": "scratch data"})
	volumeTar := []byte("volume bytes")
	scratch := filepath.Join(t.TempDir(), "scratch")
	addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
		Scratch: []ops.ScratchArchive{{Path: scratch, Archive: "s0.tar", SHA256: sum(scratchTar)}},
		Volumes: []ops.VolumeArchive{{Name: "pgdata", Archive: "v0.tar", SHA256: sum(volumeTar)}},
	}, map[string][]byte{"s0.tar": scratchTar, "v0.tar": volumeTar})

	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(scratch, "cache", "note")); got != "scratch data" {
		t.Errorf("scratch = %q", got)
	}
	if vols.data["pgdata"] != "volume bytes" {
		t.Errorf("volume = %q", vols.data["pgdata"])
	}
}

func TestUndoRefusesConflictingResources(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, vols *memVolumes, scratch string)
		want  string
	}{
		{"volume differs", func(_ *testing.T, v *memVolumes, _ string) { v.data["pgdata"] = "other" }, "pgdata"},
		{"scratch differs", func(t *testing.T, _ *memVolumes, s string) { write(t, filepath.Join(s, "other"), "x") }, "scratch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vols := &memVolumes{data: map[string]string{}}
			r := newRig(t, rigOptions{volumes: vols})
			repo, tree := dirtyWorktree(t)
			id := snapshotOf(t, r, "t1", tree)
			removeWorktree(t, repo, tree)
			scratchTar := tarOf(t, map[string]string{"note": "mine"})
			volumeTar := []byte("volume bytes")
			scratch := filepath.Join(t.TempDir(), "scratch")
			addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
				Scratch: []ops.ScratchArchive{{Path: scratch, Archive: "s0.tar", SHA256: sum(scratchTar)}},
				Volumes: []ops.VolumeArchive{{Name: "pgdata", Archive: "v0.tar", SHA256: sum(volumeTar)}},
			}, map[string][]byte{"s0.tar": scratchTar, "v0.tar": volumeTar})
			c.setup(t, vols, scratch)
			_, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to name %s", err, c.want)
			}
			if _, serr := os.Stat(tree); !os.IsNotExist(serr) {
				t.Errorf("the tree appeared after a conflict: %v", serr)
			}
		})
	}
}

func TestUndoRefusesVolumesWithoutARuntime(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	body := []byte("v")
	addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
		Volumes: []ops.VolumeArchive{{Name: "pgdata", Archive: "v0.tar", SHA256: sum(body)}},
	}, map[string][]byte{"v0.tar": body})
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); !errors.Is(err, ops.ErrNoVolumeRuntime) {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoRefusesATamperedOrEscapingArchive(t *testing.T) {
	cases := []struct {
		name string
		res  func(body []byte) ops.Resources
		tar  []byte
	}{
		{"checksum", func([]byte) ops.Resources {
			return ops.Resources{Scratch: []ops.ScratchArchive{{Path: "/tmp/never-used-scratch", Archive: "a.tar", SHA256: "00"}}}
		}, tarOf(t, map[string]string{"a": "b"})},
		{"archive name leaves the folder", func(body []byte) ops.Resources {
			return ops.Resources{Scratch: []ops.ScratchArchive{{Path: "/tmp/never-used-scratch", Archive: "../a.tar", SHA256: sum(body)}}}
		}, tarOf(t, map[string]string{"a": "b"})},
		{"entry leaves the folder", func(body []byte) ops.Resources {
			return ops.Resources{Scratch: []ops.ScratchArchive{{Path: "", Archive: "a.tar", SHA256: sum(body)}}}
		}, tarOf(t, map[string]string{"../evil": "b"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			repo, tree := dirtyWorktree(t)
			id := snapshotOf(t, r, "t1", tree)
			removeWorktree(t, repo, tree)
			res := c.res(c.tar)
			if len(res.Scratch) == 1 && res.Scratch[0].Path == "" {
				res.Scratch[0].Path = filepath.Join(t.TempDir(), "out", "scratch")
			}
			addResources(t, snapshotRecord(t, r, id).Dir, res, map[string][]byte{"a.tar": c.tar})
			if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err == nil {
				t.Fatal("undo accepted the archive")
			}
			if _, serr := os.Stat(tree); !os.IsNotExist(serr) {
				t.Errorf("the tree appeared: %v", serr)
			}
		})
	}
}

func TestUndoRetriesCleanlyAfterAKillBeforePublication(t *testing.T) {
	armed := true
	r := newRig(t, rigOptions{fault: func(p string) {
		if p == "undo:staged" && armed {
			armed = false
			panic("killed")
		}
	}})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	func() {
		defer func() { _ = recover() }()
		_, _ = r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	}()
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the target appeared before publication: %v", err)
	}
	// The journal says the step was intended; a restart decides from the world.
	again := openRig(t, r.state, rigOptions{})
	if err := again.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, tree)
}

func TestUndoProbeReportsWhatTheWorldHolds(t *testing.T) {
	armed := true
	r := newRig(t, rigOptions{fault: func(p string) {
		if p == "undo:published" && armed {
			armed = false
			panic("killed")
		}
	}})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	func() {
		defer func() { _ = recover() }()
		_, _ = r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	}()
	assertRestored(t, tree)
	again := openRig(t, r.state, rigOptions{})
	if err := again.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, err := again.eng.List()
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Kind != "undo" || list[0].State != ops.OpDone {
		t.Fatalf("after recovery: %s %s %s", list[0].Kind, list[0].State, list[0].Reason)
	}
}

// --- retention ---

func TestCapFullRefusesTheCallersOperationAndDropsNothing(t *testing.T) {
	deletes := &scripted{verdict: ops.Absent}
	r := newRig(t, rigOptions{retention: ops.Retention{Cap: 1}, extra: map[string]ops.Effect{"delete": deletes}})
	_, tree := dirtyWorktree(t)
	first := r.seedSnapshot(t, "older", 1)

	op, err := r.eng.Submit(context.Background(), ops.Spec{Kind: "clean", Plan: "p", Steps: []ops.StepSpec{
		ops.SnapshotStep(ops.SnapshotRequest{Task: "t1", Worktree: tree}),
		{Name: "delete", Effect: "delete", Key: "delete:t1", Spec: "{}"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	done, err := r.eng.Drive(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != ops.OpFailed || !strings.Contains(done.Reason, "cap") || !strings.Contains(done.Reason, first) {
		t.Fatalf("state %s, reason %q: want a refusal naming what to prune", done.State, done.Reason)
	}
	if deletes.applied != 0 {
		t.Errorf("the destructive step ran %d times", deletes.applied)
	}
	list, err := r.rec.Snapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != first {
		t.Fatalf("snapshots = %+v, want only the older one", list)
	}
	if _, err := os.Stat(list[0].Dir); err != nil {
		t.Errorf("the older snapshot was dropped: %v", err)
	}
}

func TestSnapshotOverTheCapIsRemovedAfterMeasuring(t *testing.T) {
	r := newRig(t, rigOptions{retention: ops.Retention{Cap: 100}})
	repo, tree := dirtyWorktree(t)
	_, err := r.rec.Snapshot(context.Background(), ops.SnapshotRequest{Task: "t1", Worktree: tree})
	var opErr *ops.OperationError
	if !errors.As(err, &opErr) || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("err = %v", err)
	}
	list, lerr := r.rec.Snapshots()
	if lerr != nil || len(list) != 0 {
		t.Fatalf("snapshots = %+v, %v", list, lerr)
	}
	common := filepath.Join(repo, ".git", "herdsman", "snapshots")
	if entries, _ := os.ReadDir(common); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
	if refs := git(t, repo, "for-each-ref", "refs/herdsman"); refs != "" {
		t.Errorf("refs left behind: %s", refs)
	}
}

func TestSweepPrunesOnlyExpiredSnapshots(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	r := newRig(t, rigOptions{retention: ops.Retention{Undo: time.Hour}, now: clock})
	repo, tree := dirtyWorktree(t)
	old := snapshotOf(t, r, "t1", tree)
	oldDir := snapshotRecord(t, r, old).Dir
	now = now.Add(2 * time.Hour)
	fresh := snapshotOf(t, r, "t2", tree)

	pruned, err := r.rec.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pruned, []string{old}) {
		t.Fatalf("pruned %v, want %v", pruned, []string{old})
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("the expired folder stayed: %v", err)
	}
	if refs := git(t, repo, "for-each-ref", "--format=%(refname)", "refs/herdsman"); strings.Contains(refs, old) || !strings.Contains(refs, fresh) {
		t.Errorf("refs: %s", refs)
	}
	list, _ := r.rec.Snapshots()
	if len(list) != 1 || list[0].ID != fresh {
		t.Fatalf("left: %+v", list)
	}
	if pruned, err := r.rec.Sweep(context.Background()); err != nil || len(pruned) != 0 {
		t.Fatalf("second sweep: %v %v", pruned, err)
	}
}

// The settings are read when they are applied, so a change holds at once.
func TestSweepUsesTheConfiguredAge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	age := 24 * time.Hour
	r := newRig(t, rigOptions{now: func() time.Time { return now }, policy: func() (ops.Retention, error) { return ops.Retention{Undo: age}, nil }})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	now = now.Add(2 * time.Hour)
	if pruned, err := r.rec.Sweep(context.Background()); err != nil || len(pruned) != 0 {
		t.Fatalf("pruned %v, %v", pruned, err)
	}
	age = time.Hour
	if pruned, err := r.rec.Sweep(context.Background()); err != nil || !reflect.DeepEqual(pruned, []string{id}) {
		t.Fatalf("pruned %v, %v", pruned, err)
	}
}

// Settings that cannot be read may hold a longer age than the default, so
// nothing is deleted on a guess.
func TestSweepDeletesNothingWhenTheSettingsCannotBeRead(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	var broken error
	r := newRig(t, rigOptions{now: func() time.Time { return now }, policy: func() (ops.Retention, error) { return ops.Retention{Undo: time.Hour}, broken }})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	now = now.Add(365 * 24 * time.Hour)
	broken = errors.New("bad file")
	if _, err := r.rec.Sweep(context.Background()); err == nil || !strings.Contains(err.Error(), "bad file") {
		t.Fatalf("err = %v", err)
	}
	if list, _ := r.rec.Snapshots(); len(list) != 1 || list[0].ID != id {
		t.Fatalf("left: %+v", list)
	}
}

func TestConfiguredCapRefusesASnapshot(t *testing.T) {
	r := newRig(t, rigOptions{policy: func() (ops.Retention, error) { return ops.Retention{Cap: 1}, nil }})
	r.seedSnapshot(t, "older", 1)
	_, tree := dirtyWorktree(t)
	op, err := r.rec.Snapshot(context.Background(), ops.SnapshotRequest{Task: "t", Worktree: tree})
	if err == nil && op.State == ops.OpDone {
		t.Fatalf("a snapshot was admitted over the cap: %+v", op)
	}
}

func TestParseRetention(t *testing.T) {
	got, err := ops.ParseRetention("7d", "500MiB")
	if err != nil || got.Undo != 7*24*time.Hour || got.Cap != 500<<20 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := ops.ParseRetention("36h", "1048576"); err != nil || got.Undo != 36*time.Hour || got.Cap != 1<<20 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := ops.ParseRetention("", ""); err != nil || got != (ops.Retention{}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range [][2]string{{"soon", ""}, {"-1d", ""}, {"0h", ""}, {"", "lots"}, {"", "0"}, {"", "-1GiB"}, {"", "99999999999TiB"}, {"99999999999d", ""}} {
		if got, err := ops.ParseRetention(bad[0], bad[1]); err == nil {
			t.Errorf("%v parsed: %+v", bad, got)
		}
	}
}

func TestRetentionDefaults(t *testing.T) {
	d := ops.Retention{}.WithDefaults()
	if d.Undo != 30*24*time.Hour || d.Cap != 20<<30 {
		t.Fatalf("defaults = %+v", d)
	}
}

// seedSnapshot registers a folder of size bytes as an already taken snapshot.
func (r *rig) seedSnapshot(t *testing.T, id string, size int64) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec("INSERT INTO snapshots(id, task, dir, repo, size, created) VALUES (?,?,?,?,?,?)", id, "seed", dir, "", size, 1); err != nil {
		t.Fatal(err)
	}
	return id
}

// --- the export barrier ---

func TestAccountLockExcludesEveryOtherSubmit(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	r := newRig(t, rigOptions{fault: func(p string) {
		if p == "export:barrier" {
			once.Do(func() { close(entered) })
			<-block
		}
	}, extra: map[string]ops.Effect{"fx": &scripted{verdict: ops.Absent}}})
	done := make(chan error, 1)
	dest := filepath.Join(t.TempDir(), "export")
	go func() { _, err := r.rec.Export(context.Background(), dest); done <- err }()
	<-entered

	_, err := r.eng.Submit(context.Background(), ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "k1", Spec: "{}"}}})
	var locked *ops.LockedError
	if !errors.As(err, &locked) || locked.Lock != ops.AccountLock {
		t.Fatalf("a submit during the export: %v", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := r.eng.Submit(context.Background(), ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "k2", Spec: "{}"}}}); err != nil {
		t.Fatalf("after the export: %v", err)
	}
}

func TestExportWaitsForARunningOperation(t *testing.T) {
	r := newRig(t, rigOptions{extra: map[string]ops.Effect{"fx": &scripted{verdict: ops.Absent}}})
	op, err := r.eng.Submit(context.Background(), ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "k1", Spec: "{}"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(ctx, dest); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("export did not wait: %v", err)
	}
	if _, err := r.eng.Drive(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWaitsForTheBarrier(t *testing.T) {
	fx := &scripted{verdict: ops.Unknown, applyEr: errors.New("boom")}
	block := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	r := newRig(t, rigOptions{fault: func(p string) {
		if p == "export:barrier" {
			once.Do(func() { close(entered) })
			<-block
		}
	}, extra: map[string]ops.Effect{"fx": fx}})
	op, err := r.eng.Submit(context.Background(), ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "k1", Spec: "{}"}}})
	if err != nil {
		t.Fatal(err)
	}
	parked, err := r.eng.Drive(context.Background(), op.ID)
	if err != nil || parked.State != ops.OpStopped {
		t.Fatalf("%v %v", parked, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.rec.Export(context.Background(), filepath.Join(t.TempDir(), "export"))
		done <- err
	}()
	<-entered
	var locked *ops.LockedError
	if err := r.eng.Resolve(op.ID, 0, ops.ResolveRerun); !errors.As(err, &locked) {
		t.Fatalf("resolve during the export: %v", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.eng.Resolve(op.ID, 0, ops.ResolveRerun); err != nil {
		t.Fatal(err)
	}
}

// --- export and import ---

func accountRecords(t *testing.T, r *rig) []store.Record {
	t.Helper()
	var recs []store.Record
	if err := r.eng.Read(func(c store.Conn) error {
		var err error
		recs, err = store.AccountRecords(c)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestExportRefusesAnExistingDestinationAndOneInsideTheState(t *testing.T) {
	r := newRig(t, rigOptions{})
	taken := t.TempDir()
	if _, err := r.rec.Export(context.Background(), taken); !errors.Is(err, ops.ErrDestinationExists) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.rec.Export(context.Background(), filepath.Join(r.state, "daemon", "x")); err == nil {
		t.Fatal("an export inside the state directory was accepted")
	}
	if _, err := r.rec.Export(context.Background(), "relative"); err == nil {
		t.Fatal("a relative destination was accepted")
	}
}

func TestExportCopiesTheStateDirectoryAndSkipsRuntimeFiles(t *testing.T) {
	r := newRig(t, rigOptions{})
	write(t, filepath.Join(r.state, "routes", "a", "state.json"), "{}")
	if err := os.Symlink("state.json", filepath.Join(r.state, "routes", "a", "link")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.state, "run", "daemon.pid"), "1")
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dest, "state", "routes", "a", "state.json")); got != "{}" {
		t.Errorf("state.json = %q", got)
	}
	if got, err := os.Readlink(filepath.Join(dest, "state", "routes", "a", "link")); err != nil || got != "state.json" {
		t.Errorf("link = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "state", "run")); !os.IsNotExist(err) {
		t.Errorf("run/ was exported: %v", err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(dest, "state", "daemon", "account.db"+side)); !os.IsNotExist(err) {
			t.Errorf("a %s file was exported: %v", side, err)
		}
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("export mode %v, %v", info, err)
	}
}

func TestExportFailsWholeWhenAReferencedSnapshotIsMissing(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	if err := os.RemoveAll(snapshotRecord(t, r, id).Dir); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dest := filepath.Join(parent, "export")
	_, err := r.rec.Export(context.Background(), dest)
	if err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatalf("left beside the destination: %v", entries)
	}
}

func TestImportRefusesANonEmptyStateAndABadExport(t *testing.T) {
	r := newRig(t, rigOptions{})
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	occupied := t.TempDir()
	write(t, filepath.Join(occupied, "x"), "x")
	if err := ops.Import(context.Background(), dest, occupied); !errors.Is(err, ops.ErrStateNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	if err := ops.Import(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "s")); err == nil {
		t.Fatal("a folder with no export file was imported")
	}
	raw := read(t, filepath.Join(dest, ops.ExportManifestName))
	write(t, filepath.Join(dest, ops.ExportManifestName), strings.Replace(raw, `"schema": "1.`, `"schema": "9.`, 1))
	if err := ops.Import(context.Background(), dest, filepath.Join(t.TempDir(), "s")); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("a newer major was imported: %v", err)
	}
}

func TestImportRefusesAnExportWithoutAReferencedSnapshot(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dest, "snapshots", id)); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	state := filepath.Join(parent, "state")
	if err := ops.Import(context.Background(), dest, state); err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

func TestImportRefusesAnAlteredDatabase(t *testing.T) {
	r := newRig(t, rigOptions{extra: map[string]ops.Effect{"fx": &scripted{}}})
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(dest, "state", "daemon", "account.db")
	db, err := store.Open(copied, store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO operations(id, kind, plan, spec_sha256, preconditions, state, created, updated) VALUES ('X','k','p','s','[]','done',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ops.Import(context.Background(), dest, filepath.Join(t.TempDir(), "s")); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("err = %v", err)
	}
}

// TestExportGate runs exports against a loop that keeps creating snapshots
// and a sweep that keeps pruning them. Every export that finishes must import
// into an empty state directory holding exactly the records the barrier saw.
func TestExportGate(t *testing.T) {
	var clock atomic.Int64
	clock.Store(1_000_000)
	now := func() time.Time { return time.Unix(clock.Add(1), 0) }
	var mu sync.Mutex
	barrier := map[string][]store.Record{}
	barrierSnaps := map[string][]string{}
	var r *rig
	current := ""
	r = newRig(t, rigOptions{retention: ops.Retention{Undo: 40 * time.Second}, now: now, fault: func(p string) {
		if p != "export:barrier" {
			return
		}
		list, err := r.rec.Snapshots()
		if err != nil {
			panic(err)
		}
		ids := []string{}
		for _, s := range list {
			ids = append(ids, s.ID)
		}
		var recs []store.Record
		if err := r.eng.Read(func(c store.Conn) error {
			var err error
			recs, err = store.AccountRecords(c)
			return err
		}); err != nil {
			panic(err)
		}
		mu.Lock()
		barrier[current], barrierSnaps[current] = recs, ids
		mu.Unlock()
	}})
	_, tree := dirtyWorktree(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	var made, swept atomic.Int64
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			if _, err := r.rec.Snapshot(ctx, ops.SnapshotRequest{Task: "t" + strconv.Itoa(i), Worktree: tree}); err != nil {
				if ctx.Err() == nil {
					errs <- fmt.Errorf("snapshot: %w", err)
				}
				return
			}
			made.Add(1)
			clock.Add(10)
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			pruned, err := r.rec.Sweep(ctx)
			if err != nil {
				if ctx.Err() == nil {
					errs <- fmt.Errorf("sweep: %w", err)
				}
				return
			}
			swept.Add(int64(len(pruned)))
			time.Sleep(5 * time.Millisecond)
		}
	}()

	const rounds = 5
	for i := 0; i < rounds; i++ {
		time.Sleep(60 * time.Millisecond)
		dest := filepath.Join(t.TempDir(), "export-"+strconv.Itoa(i))
		mu.Lock()
		current = dest
		mu.Unlock()
		if _, err := r.rec.Export(context.Background(), dest); err != nil {
			t.Fatalf("export %d: %v", i, err)
		}
		state := filepath.Join(t.TempDir(), "state")
		if err := ops.Import(context.Background(), dest, state); err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
		mirror := openRig(t, state, rigOptions{})
		mu.Lock()
		wantRecs, wantIDs := barrier[dest], barrierSnaps[dest]
		mu.Unlock()
		if got := accountRecords(t, mirror); !reflect.DeepEqual(got, wantRecs) {
			t.Fatalf("export %d: %d records imported, the barrier saw %d", i, len(got), len(wantRecs))
		}
		list, err := mirror.rec.Snapshots()
		if err != nil {
			t.Fatal(err)
		}
		gotIDs := []string{}
		for _, s := range list {
			gotIDs = append(gotIDs, s.ID)
			if _, err := snapshot.Verify(context.Background(), s.Dir); err != nil {
				t.Fatalf("export %d: snapshot %s: %v", i, s.ID, err)
			}
			if !strings.HasPrefix(s.Dir, state) {
				t.Fatalf("export %d: snapshot %s is at %s, outside the imported state", i, s.ID, s.Dir)
			}
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("export %d: snapshots %v, the barrier saw %v", i, gotIDs, wantIDs)
		}
	}
	cancel()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if made.Load() == 0 || swept.Load() == 0 {
		t.Fatalf("the loops did no work: %d made, %d pruned", made.Load(), swept.Load())
	}
}

const killEnv = "HERDSMAN_TEST_KILL_EXPORT"

// TestExportKilledChild is the child of TestExportKillLeavesNoDestination.
func TestExportKilledChild(t *testing.T) {
	spec := os.Getenv(killEnv)
	if spec == "" {
		t.Skip("runs only as the child of the kill test")
	}
	state, dest, _ := strings.Cut(spec, "|")
	r := openRig(t, state, rigOptions{fault: func(p string) {
		if p == "export:staged" {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}})
	_, err := r.rec.Export(context.Background(), dest)
	t.Fatalf("export returned after the kill point: %v", err)
}

func TestExportKillLeavesNoDestination(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	seed := openRig(t, state, rigOptions{})
	_, tree := dirtyWorktree(t)
	snapshotOf(t, seed, "t1", tree)
	if err := seed.db.Close(); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	dest := filepath.Join(parent, "export")
	cmd := exec.Command(os.Args[0], "-test.run=^TestExportKilledChild$")
	cmd.Env = append(os.Environ(), killEnv+"="+state+"|"+dest)
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("the child did not die by the kill: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("a destination exists after the kill: %v", err)
	}

	// The restart finishes the journaled export from nothing.
	r := openRig(t, state, rigOptions{})
	if err := r.eng.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "export" {
		t.Fatalf("beside the destination: %v", entries)
	}
	if err := ops.Import(context.Background(), dest, filepath.Join(t.TempDir(), "s")); err != nil {
		t.Fatal(err)
	}
	if held, _ := r.eng.HeldLocks(); len(held) != 0 {
		t.Fatalf("locks left: %v", held)
	}
}

// --- paths the gates above do not reach ---

func TestExportCopiesRouteDatabases(t *testing.T) {
	r := newRig(t, rigOptions{})
	folder := filepath.Join(r.state, "routes", "one")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(r.state, "routes"), 0o700); err != nil {
		t.Fatal(err)
	}
	live, err := store.Open(filepath.Join(folder, "route.db"), store.Writer, store.KindRoute)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(state, "routes", "one", "route.db")
	info, err := os.Stat(copied)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("copied database: %v, %v", info, err)
	}
	c, err := store.Open(copied, store.Writer, store.KindRoute)
	if err != nil {
		t.Fatalf("the copy does not open: %v", err)
	}
	c.Close()
}

func TestExportProbeReadsTheDestination(t *testing.T) {
	r := newRig(t, rigOptions{})
	fx := r.rec.Effects()["account.export"]
	base := t.TempDir()
	step := func(dest string) ops.Step { return ops.Step{StepSpec: ops.StepSpec{Spec: dest}, OpID: "OP"} }
	manifest := func(name, body string) string {
		dest := filepath.Join(base, name)
		write(t, filepath.Join(dest, ops.ExportManifestName), body)
		return dest
	}
	foreign := filepath.Join(base, "foreign")
	write(t, filepath.Join(foreign, "x"), "x")
	for _, c := range []struct {
		name, dest string
		want       ops.Verdict
	}{
		{"nothing there", filepath.Join(base, "none"), ops.Absent},
		{"someone's folder", foreign, ops.Different},
		{"unreadable manifest", manifest("bad", "{"), ops.Different},
		{"another export", manifest("other", `{"op":"ELSE"}`), ops.Different},
		{"this export", manifest("mine", `{"op":"OP"}`), ops.Matches},
	} {
		if got, _, err := fx.Probe(context.Background(), step(c.dest)); err != nil || got != c.want {
			t.Errorf("%s: %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestPruneProbeAndApplyOfAGoneSnapshot(t *testing.T) {
	r := newRig(t, rigOptions{})
	fx := r.rec.Effects()["snapshot.prune"]
	gone := ops.Step{StepSpec: ops.StepSpec{Spec: "gone"}}
	if err := fx.Apply(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	if v, _, err := fx.Probe(context.Background(), gone); err != nil || v != ops.Matches {
		t.Fatalf("%v %v", v, err)
	}
	r.seedSnapshot(t, "held", 1)
	if v, _, err := fx.Probe(context.Background(), ops.Step{StepSpec: ops.StepSpec{Spec: "held"}}); err != nil || v != ops.Unknown {
		t.Fatalf("%v %v", v, err)
	}
}

func TestUndoProbeCallsAnotherTreeAConflict(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	other := t.TempDir()
	write(t, filepath.Join(other, "x"), "x")
	raw, _ := json.Marshal(map[string]string{"snapshot": id, "target": other})
	v, detail, err := r.rec.Effects()["undo.restore"].Probe(context.Background(), ops.Step{StepSpec: ops.StepSpec{Spec: string(raw)}})
	if err != nil || v != ops.Different || !strings.Contains(detail, other) {
		t.Fatalf("%v %q %v", v, detail, err)
	}
}

func TestUndoOfADetachedHead(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	head := git(t, tree, "rev-parse", "HEAD")
	git(t, tree, "checkout", "-q", "--detach")
	id := snapshotOf(t, r, "t1", tree)
	git(t, repo, "worktree", "remove", "--force", tree)
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, tree, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s", got, head)
	}
	if list := git(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(list, "worktree "+tree+"\n") {
		t.Errorf("not linked:\n%s", list)
	}
}

func TestUndoAcceptsTheBranchAtTheRecordedCommit(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	git(t, repo, "worktree", "remove", "--force", tree)
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, tree)
}

func TestUndoOfAnUnbornBranchStaysStandalone(t *testing.T) {
	r := newRig(t, rigOptions{})
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(base, "repo")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, tree, "init", "-q", "-b", "main")
	write(t, filepath.Join(tree, "a.txt"), "first\n")
	id := snapshotOf(t, r, "t1", tree)
	target := filepath.Join(base, "restored")
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: target}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(target, "a.txt")); got != "first\n" {
		t.Errorf("a.txt = %q", got)
	}
}

func TestUndoRefusesARelativeTarget(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: "relative"}); err == nil {
		t.Fatal("a relative target was accepted")
	}
}

func TestScratchWithTheSameContentCountsAsRestored(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "sub/", Mode: 0o700, Typeflag: tar.TypeDir},
		{Name: "sub/link", Linkname: "target", Typeflag: tar.TypeSymlink},
	} {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(filepath.Join(scratch, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(scratch, "sub", "link")); err != nil {
		t.Fatal(err)
	}
	addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
		Scratch: []ops.ScratchArchive{{Path: scratch, Archive: "s.tar", SHA256: sum(buf.Bytes())}},
	}, map[string][]byte{"s.tar": buf.Bytes()})
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err != nil {
		t.Fatal(err)
	}
	assertRestored(t, tree)
}

func TestScratchRefusesADeviceEntry(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "dev", Mode: 0o600, Typeflag: tar.TypeChar}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
		Scratch: []ops.ScratchArchive{{Path: filepath.Join(t.TempDir(), "scratch"), Archive: "s.tar", SHA256: sum(buf.Bytes())}},
	}, map[string][]byte{"s.tar": buf.Bytes()})
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoRefusesUnreadableResources(t *testing.T) {
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	write(t, filepath.Join(snapshotRecord(t, r, id).Dir, ops.ResourcesName), "{")
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id, Target: filepath.Join(t.TempDir(), "x")}); err == nil || !strings.Contains(err.Error(), ops.ResourcesName) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportRefusesRelativePathsAndAMissingDatabaseRow(t *testing.T) {
	if err := ops.Import(context.Background(), "relative", "/x"); err == nil {
		t.Fatal("a relative source was accepted")
	}
	r := newRig(t, rigOptions{})
	_, tree := dirtyWorktree(t)
	snapshotOf(t, r, "t1", tree)
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	raw := read(t, filepath.Join(dest, ops.ExportManifestName))
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m["snapshots"] = []string{}
	edited, _ := json.Marshal(m)
	write(t, filepath.Join(dest, ops.ExportManifestName), string(edited))
	if err := ops.Import(context.Background(), dest, filepath.Join(t.TempDir(), "s")); err == nil || !strings.Contains(err.Error(), "not in the export") {
		t.Fatalf("err = %v", err)
	}
}

func TestLockedErrorNamesTheHolder(t *testing.T) {
	err := (&ops.LockedError{Lock: "a", Holder: "b"}).Error()
	if !strings.Contains(err, "a") || !strings.Contains(err, "b") {
		t.Fatal(err)
	}
}

// --- staging, identity and ownership ---

func TestImportKeepsAnUnrelatedFolderBesideTheState(t *testing.T) {
	r := newRig(t, rigOptions{})
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "imported")
	keep := state + ".import-tmp/keep.txt"
	write(t, keep, "precious")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	if got := read(t, keep); got != "precious" {
		t.Fatalf("keep.txt = %q", got)
	}
}

func TestScratchKeepsAnUnrelatedFolderBesideIt(t *testing.T) {
	r := newRig(t, rigOptions{})
	repo, tree := dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	archive := tarOf(t, map[string]string{"note": "mine"})
	scratch := filepath.Join(t.TempDir(), "scratch")
	keep := scratch + ".herdsman-undo-tmp/keep.txt"
	write(t, keep, "precious")
	addResources(t, snapshotRecord(t, r, id).Dir, ops.Resources{
		Scratch: []ops.ScratchArchive{{Path: scratch, Archive: "s.tar", SHA256: sum(archive)}},
	}, map[string][]byte{"s.tar": archive})
	if _, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, keep); got != "precious" {
		t.Fatalf("keep.txt = %q", got)
	}
}

func TestImportRefusesASnapshotIDThatLeavesTheFolder(t *testing.T) {
	for _, id := range []string{"../../payload", "..", ".", "a/b", ""} {
		t.Run(id, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			parent := t.TempDir()
			dest := filepath.Join(parent, "export")
			if _, err := r.rec.Export(context.Background(), dest); err != nil {
				t.Fatal(err)
			}
			raw := read(t, filepath.Join(dest, ops.ExportManifestName))
			quoted, _ := json.Marshal([]string{id})
			write(t, filepath.Join(dest, ops.ExportManifestName), strings.Replace(raw, `"snapshots": []`, `"snapshots": `+string(quoted), 1))
			write(t, filepath.Join(parent, "payload", "keep.txt"), "outside")
			stateParent := t.TempDir()
			if err := ops.Import(context.Background(), dest, filepath.Join(stateParent, "state")); err == nil || !strings.Contains(err.Error(), "snapshot id") {
				t.Fatalf("err = %v", err)
			}
			if entries, _ := os.ReadDir(stateParent); len(entries) != 0 {
				t.Fatalf("written outside staging: %v", entries)
			}
		})
	}
}

func TestUndoDoesNotTakeAnUnrelatedTreeAtTheSameHeadForItsOwn(t *testing.T) {
	armed := false
	var repo, tree string
	r := newRig(t, rigOptions{fault: func(p string) {
		if armed && p == "step:0:intended" {
			armed = false
			git(t, filepath.Dir(tree), "clone", "-q", repo, tree)
			git(t, tree, "checkout", "-q", "--detach", "HEAD")
			write(t, filepath.Join(tree, "occupant.txt"), "someone else's work")
		}
	}})
	repo, tree = dirtyWorktree(t)
	id := snapshotOf(t, r, "t1", tree)
	removeWorktree(t, repo, tree)
	armed = true
	_, err := r.rec.Undo(context.Background(), ops.UndoRequest{Snapshot: id})
	var opErr *ops.OperationError
	if !errors.As(err, &opErr) || opErr.Op.State != ops.OpConflict {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(tree, "occupant.txt")); got != "someone else's work" {
		t.Fatalf("occupant = %q", got)
	}
}

func TestAConflictedExportCanResolveItsOwnBarrier(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "export")
	r := newRig(t, rigOptions{extra: map[string]ops.Effect{"fx": &scripted{}}, fault: func(p string) {
		if p == "export:barrier" {
			write(t, filepath.Join(dest, "occupant.txt"), "keep")
		}
	}})
	_, err := r.rec.Export(context.Background(), dest)
	var opErr *ops.OperationError
	if !errors.As(err, &opErr) || opErr.Op.State != ops.OpConflict {
		t.Fatalf("err = %v", err)
	}
	// Everyone else still waits for it.
	_, serr := r.eng.Submit(context.Background(), ops.Spec{Kind: "k", Plan: "p", Steps: []ops.StepSpec{{Name: "s", Effect: "fx", Key: "k1", Spec: "{}"}}})
	var locked *ops.LockedError
	if !errors.As(serr, &locked) {
		t.Fatalf("a submit during the conflict: %v", serr)
	}
	if err := r.eng.Resolve(opErr.Op.ID, 0, ops.ResolveDone); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

func TestImportRebasesAccountPathsOntoTheNewState(t *testing.T) {
	r := newRig(t, rigOptions{})
	inbox := filepath.Join(r.state, "repo-r1")
	outside := "/srv/elsewhere/inbox"
	for route, dir := range map[string]string{"r1": inbox, "r2": outside} {
		if _, err := r.db.Exec("INSERT INTO routes(route, inbox, repo, herdr_socket, db_path, created, state, open_gen, reserved_message_id, reserved_seq) VALUES (?,?,?,?,?,?,'active','g',0,0)",
			route, dir, "repo", "", filepath.Join(dir, "route.db"), 1); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "routes": map[string]any{
		"r1": map[string]any{"inbox": inbox, "repo": "repo", "herdr_socket": "", "created": 1},
		"r2": map[string]any{"inbox": outside, "repo": "repo", "herdr_socket": "", "created": 1},
	}})
	if err := os.WriteFile(filepath.Join(r.state, "daemon", "routes.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "export")
	if _, err := r.rec.Export(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "imported")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	var file struct {
		Routes map[string]struct {
			Inbox string `json:"inbox"`
		} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(state, "daemon", "routes.json"))), &file); err != nil {
		t.Fatal(err)
	}
	if got, want := file.Routes["r1"].Inbox, filepath.Join(state, "repo-r1"); got != want {
		t.Errorf("registry inbox = %s, want %s", got, want)
	}
	if got := file.Routes["r2"].Inbox; got != outside {
		t.Errorf("outside inbox moved to %s", got)
	}
	m := openRig(t, state, rigOptions{})
	rows, paths := map[string]string{}, map[string]string{}
	if err := m.db.Query("SELECT route, inbox, db_path FROM routes", nil, func(row store.Row) error {
		rows[row.Text(0)], paths[row.Text(0)] = row.Text(1), row.Text(2)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rows["r1"] != filepath.Join(state, "repo-r1") || rows["r2"] != outside {
		t.Errorf("table inboxes = %v", rows)
	}
	if paths["r1"] != filepath.Join(state, "repo-r1", "route.db") || paths["r2"] != filepath.Join(outside, "route.db") {
		t.Errorf("table database paths = %v", paths)
	}
}

func TestExportHoldsTheQuiesceGateForTheCopy(t *testing.T) {
	var held atomic.Bool
	var sawHeld atomic.Bool
	state := filepath.Join(t.TempDir(), "state")
	r := openRig(t, state, rigOptions{quiesce: func() func() {
		held.Store(true)
		return func() { held.Store(false) }
	}, fault: func(p string) {
		if p == "export:staged" {
			sawHeld.Store(held.Load())
		}
	}})
	if _, err := r.rec.Export(context.Background(), filepath.Join(t.TempDir(), "export")); err != nil {
		t.Fatal(err)
	}
	if !sawHeld.Load() || held.Load() {
		t.Fatalf("held during the copy: %v; released after: %v", sawHeld.Load(), !held.Load())
	}
}
