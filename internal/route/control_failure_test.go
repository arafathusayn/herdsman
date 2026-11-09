package route

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

// fakeRecovery answers Verify with err and counts the calls.
type fakeRecovery struct {
	err        error
	captureErr error
	calls      int
}

func (f *fakeRecovery) Capture(context.Context, SnapshotRequest) error { return f.captureErr }
func (f *fakeRecovery) Verify(context.Context, string, []SnapshotTarget) error {
	f.calls++
	return f.err
}

// deadHerdr is a client whose every call fails, as when herdr has gone away.
func deadHerdr(t *testing.T) *herdr.Client {
	t.Helper()
	return herdr.New(filepath.Join(t.TempDir(), "no-socket"))
}

// routeOnDisk returns step data for a route with a folder and an inbox, each
// holding one file.
func routeOnDisk(t *testing.T) stepData {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	d := stepData{Route: "harbor", Repo: repo, Folder: filepath.Join(repo, ".herdsman", "harbor-2026-11-09"), Inbox: filepath.Join(root, "inbox"), Workspace: "harbor", Name: "orchestrator", Label: labelCheckpoint}
	for _, dir := range []string{d.Folder, d.Inbox} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targets := []string{d.Folder, d.Inbox}
	d.Control = &controlData{Verb: VerbStop, Generation: 1, Agents: []string{"orchestrator"}, Snapshot: filepath.Join(root, "snapshot"), Captures: targets, Targets: targets}
	return d
}

func assertInPlace(t *testing.T, d stepData) {
	t.Helper()
	for _, target := range d.Control.Targets {
		if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
			t.Fatalf("not put back: %v", err)
		}
		if exists(stagedPath(target)) {
			t.Fatalf("%s left staged", target)
		}
	}
}

func TestPlanControlRefusesAnUnusableStart(t *testing.T) {
	ctx := context.Background()
	if _, err := PlanControl(ctx, ControlRequest{Verb: VerbPause}); err == nil {
		t.Fatal("no start was accepted")
	}
	if _, err := PlanControl(ctx, ControlRequest{Verb: VerbPause, Start: &ops.Operation{}}); err == nil {
		t.Fatal("a start with no steps was accepted")
	}
	broken := ops.Step{StepSpec: ops.StepSpec{Spec: "{"}}
	if _, err := PlanControl(ctx, ControlRequest{Verb: VerbPause, Start: &ops.Operation{Steps: []ops.Step{broken}}}); err == nil {
		t.Fatal("a corrupt first step was accepted")
	}
	good := ops.Step{StepSpec: ops.StepSpec{Spec: `{"route":"harbor"}`}}
	agent := ops.Step{StepSpec: ops.StepSpec{Effect: effectAgent, Spec: "{"}}
	if _, err := PlanControl(ctx, ControlRequest{Verb: VerbPause, Start: &ops.Operation{Steps: []ops.Step{good, agent}}}); err == nil {
		t.Fatal("a corrupt agent step was accepted")
	}
	attach := ops.Step{StepSpec: ops.StepSpec{Effect: effectAttach, Spec: "{"}}
	if _, err := PlanControl(ctx, ControlRequest{Verb: VerbResume, From: ControlStopped, Start: &ops.Operation{Steps: []ops.Step{good, attach}}}); err == nil {
		t.Fatal("a corrupt attach step was accepted")
	}
	if _, err := PlanControl(ctx, ControlRequest{Verb: "explode", Start: &ops.Operation{Steps: []ops.Step{good}}}); err == nil {
		t.Fatal("an unknown verb was accepted")
	}
}

func TestPlanDeleteNeedsARepository(t *testing.T) {
	spec, _ := json.Marshal(stepData{Route: "harbor", Repo: filepath.Join(t.TempDir(), "absent")})
	start := &ops.Operation{Steps: []ops.Step{{StepSpec: ops.StepSpec{Spec: string(spec)}}}}
	if _, err := PlanControl(context.Background(), ControlRequest{Verb: VerbDelete, Start: start}); err == nil {
		t.Fatal("a delete was planned in no repository")
	}
}

func TestSnapshotRootIsSharedByWorktrees(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, linked := filepath.Join(root, "repo"), filepath.Join(root, "linked")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	commit := exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base")
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", linked).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	main, err := snapshotRoot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	other, err := snapshotRoot(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if main != other {
		t.Fatalf("%s is not %s", other, main)
	}
}

func TestControlRecordErrors(t *testing.T) {
	d := routeOnDisk(t)
	if _, err := ReadControlRecord(d.Folder); err == nil {
		t.Fatal("a missing record was read")
	}
	if err := writeFileNew(filepath.Join(controlDir(d.Folder), "record.json"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadControlRecord(d.Folder); err == nil {
		t.Fatal("a corrupt record was read")
	}
}

func TestApplyRecordFailsOnACorruptRowAndRunsOnce(t *testing.T) {
	d := routeOnDisk(t)
	ctx := context.Background()
	if err := writeFileNew(rowPath(d), []byte("{")); err != nil {
		t.Fatal(err)
	}
	if err := applyRecord(ctx, Deps{}, nil, d); err == nil {
		t.Fatal("a corrupt row was recorded")
	}
	if err := writeFileNew(recordStamp(d), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyRecord(ctx, Deps{}, nil, d); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(controlDir(d.Folder), "record.json")) {
		t.Fatal("a recorded step wrote again")
	}
}

func TestApplyRecordFailsWhenTheFolderCannotBeWritten(t *testing.T) {
	d := routeOnDisk(t)
	if err := os.WriteFile(controlDir(d.Folder), nil, 0o600); err != nil { // a file where a folder must go
		t.Fatal(err)
	}
	if err := applyRecord(context.Background(), Deps{}, nil, d); err == nil {
		t.Fatal("the record was lost without an error")
	}
}

func TestAGoneHerdrIsBestEffortForAgents(t *testing.T) {
	ctx := context.Background()
	for name, apply := range map[string]func(context.Context, Deps, *herdr.Client, stepData) error{"interrupt": applyInterrupt, "last call": applyLastCall} {
		t.Run(name, func(t *testing.T) {
			d := routeOnDisk(t)
			if err := apply(ctx, Deps{}, deadHerdr(t), d); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(rowPath(d))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), StatusNotReached) {
				t.Fatalf("row %s", raw)
			}
		})
	}
}

func TestAGoneHerdrFailsStepsThatMustReachIt(t *testing.T) {
	ctx := context.Background()
	d := routeOnDisk(t)
	if err := applyQuiet(ctx, Deps{}, deadHerdr(t), d); err == nil {
		t.Fatal("quiet")
	}
	if err := applyClosePane(ctx, Deps{}, deadHerdr(t), d); err == nil {
		t.Fatal("close pane")
	}
	if v, _, err := probeClosePane(ctx, Deps{}, deadHerdr(t), d); err == nil || v != ops.Unknown {
		t.Fatalf("probe close pane: %v %v", v, err)
	}
	if err := closeWorkspace(ctx, deadHerdr(t), d.Workspace); err == nil {
		t.Fatal("close workspace")
	}
	if err := applyContinue(ctx, Deps{}, deadHerdr(t), d); err == nil {
		t.Fatal("continue")
	}
	if exists(sentStamp(d)) {
		t.Fatal("an unsent prompt was stamped as sent")
	}
	if err := writeFileNew(holdFile(d), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyUnhold(ctx, Deps{}, deadHerdr(t), d); err == nil {
		t.Fatal("unhold with herdr gone left the hold unreported")
	}
}

func TestApplyContinueSendsOnce(t *testing.T) {
	d := routeOnDisk(t)
	if err := writeFileNew(sentStamp(d), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyContinue(context.Background(), Deps{}, deadHerdr(t), d); err != nil {
		t.Fatalf("a sent prompt was sent again: %v", err)
	}
}

func TestHoldSkipsWhatItCannotHold(t *testing.T) {
	ctx := context.Background()
	d := routeOnDisk(t)
	if err := applyHold(ctx, Deps{}, deadHerdr(t), d); err != nil {
		t.Fatalf("an unapproved pair: %v", err)
	}
	pair := []byte(`{"adjutant":"a","orchestrator":"o"}`)
	for _, name := range []string{"peers", "peers.approved"} {
		if err := os.WriteFile(filepath.Join(d.Inbox, name), pair, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyHold(ctx, Deps{}, deadHerdr(t), d); err != nil {
		t.Fatalf("an agent that is gone: %v", err)
	}
}

func TestRetireReportsAnUnwritableInbox(t *testing.T) {
	ctx := context.Background()
	d := routeOnDisk(t)
	d.Inbox = filepath.Join(d.Inbox, "absent")
	if err := applyRetire(ctx, Deps{}, nil, d); err == nil {
		t.Fatal("retire claimed success")
	}
	file := routeOnDisk(t)
	if err := os.WriteFile(filepath.Join(file.Inbox, "peers"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(file.Inbox, "peers.approved"), 0o700); err != nil {
		t.Fatal(err)
	}
	if v, _, err := probeRetire(ctx, Deps{}, nil, file); err == nil || v != ops.Unknown {
		t.Fatalf("%v %v", v, err)
	}
}

func TestRemoveFileReportsARealFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(dir); err == nil {
		t.Fatal("a folder with content was reported removed")
	}
}

func TestRunningAgentsReportsErrors(t *testing.T) {
	ctx := context.Background()
	corrupt := &ops.Operation{Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: effectAgent, Spec: "{"}}}}
	if _, err := RunningAgents(ctx, corrupt); err == nil {
		t.Fatal("a corrupt step was read")
	}
	spec, _ := json.Marshal(stepData{Name: "orchestrator", Socket: filepath.Join(t.TempDir(), "no-socket")})
	gone := &ops.Operation{Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: effectAgent, Spec: string(spec)}}}}
	if _, err := RunningAgents(ctx, gone); err == nil {
		t.Fatal("an unreachable herdr was reported as no agents")
	}
}

func TestResolveHaltedLeavesOtherOperationsAlone(t *testing.T) {
	if err := ResolveHalted(nil, &ops.Operation{State: ops.OpDone}); err != nil {
		t.Fatal(err)
	}
	if err := ResolveHalted(nil, &ops.Operation{State: ops.OpStopped, Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: effectRecord}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestExportedReadersOfAJournal(t *testing.T) {
	if RepoOf(&ops.Operation{}) != "" {
		t.Fatal("repo of no steps")
	}
	corrupt := ops.Step{StepSpec: ops.StepSpec{Spec: "{"}}
	if RepoOf(&ops.Operation{Steps: []ops.Step{corrupt}}) != "" {
		t.Fatal("repo of a corrupt step")
	}
	if !IsControlKind(KindStop) || IsControlKind("route.up") {
		t.Fatal("control kinds")
	}
	corrupt.Effect = effectSnapshot
	if SnapshotOf(&ops.Operation{Steps: []ops.Step{corrupt, {StepSpec: ops.StepSpec{Effect: effectRecord}}}}) != "" {
		t.Fatal("snapshot of no snapshot step")
	}
	spec, _ := json.Marshal(stepData{Control: &controlData{Snapshot: "/dest"}})
	if got := SnapshotOf(&ops.Operation{Steps: []ops.Step{{StepSpec: ops.StepSpec{Effect: effectSnapshot, Spec: string(spec)}}}}); got != "/dest" {
		t.Fatal(got)
	}
}

func TestBindTargetRefusesAnIncompleteInventory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "route")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target, "kept")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.FileRecord{Path: "route/kept", Size: 3, MTimeNS: info.ModTime().UnixNano()}
	live := SnapshotTarget{Path: target, Live: target}

	if err := bindTarget(&snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record}}, live); err != nil {
		t.Fatalf("a matching inventory: %v", err)
	}
	if err := bindTarget(&snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record}}, SnapshotTarget{Path: target}); err != nil {
		t.Fatalf("a target with no bytes left: %v", err)
	}
	left := snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record}, Excluded: []snapshot.Exclusion{{Path: "route/big", Reason: snapshot.ReasonRebuildable}}}
	if err := bindTarget(&left, live); err == nil {
		t.Fatal("a snapshot that left a file out was accepted")
	}
	sock := snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record}, Excluded: []snapshot.Exclusion{{Path: "route/sock", Reason: snapshot.ReasonSpecial}}}
	if err := bindTarget(&sock, live); err != nil {
		t.Fatalf("a socket holds no data: %v", err)
	}
	stale := record
	stale.Size = 9
	if err := bindTarget(&snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{stale}}, live); err == nil {
		t.Fatal("a changed file was accepted")
	}
	vanished := snapshot.FileRecord{Path: "route/vanished", Size: 1}
	if err := bindTarget(&snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record, vanished}}, live); err == nil {
		t.Fatal("a file gone from the disk was accepted")
	}
	kept := snapshot.FileRecord{Path: "route/empty", Mode: 0o040000}
	if err := bindTarget(&snapshot.Manifest{Worktree: root, Files: []snapshot.FileRecord{record, kept}}, live); err != nil {
		t.Fatalf("a kept empty folder: %v", err)
	}
	if err := bindTarget(&snapshot.Manifest{Worktree: root}, SnapshotTarget{Path: root}); err == nil {
		t.Fatal("the snapshot's root itself was accepted as a target")
	}
}

func TestSnapshotStepsReportTheirFailures(t *testing.T) {
	ctx := context.Background()
	d := routeOnDisk(t)
	if err := applySnapshot(ctx, Deps{}, nil, d); !errors.Is(err, ErrNoRecovery) {
		t.Fatal(err)
	}
	if err := verifyCaptures(ctx, Deps{}, d); !errors.Is(err, ErrNoRecovery) {
		t.Fatal(err)
	}
	if err := applySnapshot(ctx, Deps{Recovery: &fakeRecovery{captureErr: errors.New("full")}}, nil, d); err == nil {
		t.Fatal("a failed capture was accepted")
	}
	blocked := d
	blockedControl := *d.Control
	blockedControl.Snapshot = filepath.Join(d.Inbox, "keep", "under-a-file", "dest")
	blocked.Control = &blockedControl
	if err := applySnapshot(ctx, Deps{Recovery: &fakeRecovery{}}, nil, blocked); err == nil {
		t.Fatal("a snapshot folder that cannot exist was accepted")
	}
	if err := os.MkdirAll(d.Control.Snapshot, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := Deps{Recovery: &fakeRecovery{err: errors.New("bad")}}
	if v, _, _ := probeSnapshot(ctx, bad, nil, d); v != ops.Unknown {
		t.Fatalf("a bad snapshot probed %v", v)
	}
	downs := 0
	down := func(context.Context, string, string) error { downs++; return nil }
	if err := applyDown(ctx, Deps{Recovery: bad.Recovery}, nil, d); !errors.Is(err, ErrNoRecovery) {
		t.Fatal(err)
	}
	if err := applyDown(ctx, Deps{Recovery: bad.Recovery, Down: down}, nil, d); err == nil || downs != 0 {
		t.Fatalf("down ran on a bad snapshot: %v, %d", err, downs)
	}
	if err := applyTeardown(ctx, Deps{}, nil, d); err == nil {
		t.Fatal("a teardown ran with no backend")
	}
	if v, _, _ := probeUnknown(ctx, Deps{}, nil, d); v != ops.Unknown {
		t.Fatalf("probe %v", v)
	}
}

func TestRemovalPutsEverythingBackWhenItCannotFinish(t *testing.T) {
	ctx := context.Background()

	t.Run("a target cannot be moved aside", func(t *testing.T) {
		d := routeOnDisk(t)
		blocker := stagedPath(d.Inbox) // a folder with content refuses the rename
		if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := applyRemove(ctx, Deps{Recovery: &fakeRecovery{}}, deadHerdr(t), d); err == nil {
			t.Fatal("a removal with a target it could not move went on")
		}
		if err := os.RemoveAll(blocker); err != nil {
			t.Fatal(err)
		}
		assertInPlace(t, d)
	})

	t.Run("the workspace cannot be closed", func(t *testing.T) {
		d := routeOnDisk(t)
		if err := applyRemove(ctx, Deps{Recovery: &fakeRecovery{}}, deadHerdr(t), d); err == nil {
			t.Fatal("the removal went on with the workspace open")
		}
		assertInPlace(t, d)
	})

	t.Run("a target the route does not own", func(t *testing.T) {
		d := routeOnDisk(t)
		d.Control.Targets = []string{d.Repo}
		f := &fakeRecovery{}
		if err := applyRemove(ctx, Deps{Recovery: f}, deadHerdr(t), d); err == nil || f.calls != 0 {
			t.Fatalf("%v after %d checks", err, f.calls)
		}
	})
}
