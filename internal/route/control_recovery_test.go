package route_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
)

// undoable is a rig on the real undo: the delete's backend is the pipeline the
// daemon runs, and undo is the operation a person runs.
func undoable(t *testing.T, retention ops.Retention) (*rig, *ops.Recovery) {
	t.Helper()
	r, _ := recoverable(t)
	undo := ops.NewRecovery(ops.RecoveryOptions{StateDir: r.private, Retention: retention})
	r.undo = undo
	r.recovery = route.Pipeline{Recovery: undo}
	return r, undo
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestARealDeleteIsUndoneByUndo(t *testing.T) {
	r, undo := undoable(t, ops.Retention{})
	p, start, e, done := deleted(t, r)
	defer done()
	note, secret := filepath.Join(p.Folder, "note"), filepath.Join(p.Inbox, "secret")
	for _, f := range []string{note, secret} {
		if err := os.WriteFile(f, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	op := drive(t, e, control(t, start, route.VerbDelete, 2).Spec)
	if op.State != ops.OpDone {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	for _, gone := range []string{p.Folder, p.Inbox} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: %v", gone, err)
		}
	}
	res, err := undo.Undo(context.Background(), ops.UndoRequest{Snapshot: op.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, note); got != note {
		t.Fatalf("route folder: %q", got)
	}
	if got := read(t, secret); got != secret {
		t.Fatalf("inbox: %q", got)
	}
	if !strings.HasPrefix(res.Target, r.repo+".undo-") || res.Original {
		t.Fatalf("the repository tree went to %s", res.Target)
	}
}

func damageOneScratch(t *testing.T, dest string) {
	t.Helper()
	var res ops.Resources
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dest, ops.ResourcesName))), &res); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, res.Scratch[len(res.Scratch)-1].Archive), []byte("damaged"), 0o600); err != nil {
		t.Error(err)
	}
}

func TestADamagedScratchArchiveStopsTheDelete(t *testing.T) {
	r, _ := undoable(t, ops.Retention{})
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	r.onFault = func(point string) {
		if point == "step:1:done" {
			damageOneScratch(t, c.Snapshot)
		}
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("deleted over a damaged archive")
	}
	assertKept(t, r, p)
}

func TestAnInboxChangedAfterTheSnapshotStopsTheDelete(t *testing.T) {
	r, _ := undoable(t, ops.Retention{})
	p, start, e, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	r.onFault = func(point string) {
		if point == "step:1:done" {
			if err := os.WriteFile(filepath.Join(p.Inbox, "late"), []byte("x"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	if op := drive(t, e, c.Spec); op.State == ops.OpDone {
		t.Fatal("deleted an inbox the archive does not hold")
	}
	assertKept(t, r, p)
}

func TestAFullSnapshotCapRefusesTheDelete(t *testing.T) {
	r, _ := undoable(t, ops.Retention{Cap: 1})
	p, start, e, done := deleted(t, r)
	defer done()
	op := drive(t, e, control(t, start, route.VerbDelete, 2).Spec)
	if op.State == ops.OpDone || !strings.Contains(op.Reason, "cap") {
		t.Fatalf("%s: %s", op.State, op.Reason)
	}
	assertKept(t, r, p)
}

// Undo cannot restore a snapshot it has no record of, whatever the folder holds.
func TestASnapshotUndoDoesNotKnowIsNotVerified(t *testing.T) {
	r, _ := undoable(t, ops.Retention{})
	p, _, _, done := deleted(t, r)
	defer done()
	err := r.recovery.Verify(context.Background(), filepath.Join(r.repo, "unrecorded"), []route.SnapshotTarget{{Path: p.Folder}})
	if err == nil || !strings.Contains(err.Error(), "no record") {
		t.Fatalf("got %v", err)
	}
}

// A crash during the capture leaves no snapshot; the rerun takes it again.
func TestACrashedCaptureIsTakenAgain(t *testing.T) {
	r, _ := undoable(t, ops.Retention{})
	p, start, _, done := deleted(t, r)
	defer done()
	c := control(t, start, route.VerbDelete, 2)
	q := route.SnapshotRequest{Route: "harbor", Repo: r.repo, Operation: "01CRASHED", Dest: c.Snapshot, Targets: []string{p.Folder, p.Inbox}, Remove: []string{p.Folder, p.Inbox}}
	if err := os.MkdirAll(c.Snapshot+".partial", 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.recovery.Capture(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(c.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := r.recovery.Capture(ctx, q); err != nil {
		t.Fatalf("the second capture: %v", err)
	}
	if _, err := os.Stat(c.Snapshot + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := r.recovery.Verify(ctx, c.Snapshot, []route.SnapshotTarget{{Path: p.Folder, Live: p.Folder}, {Path: p.Inbox, Live: p.Inbox}}); err != nil {
		t.Fatal(err)
	}
}
