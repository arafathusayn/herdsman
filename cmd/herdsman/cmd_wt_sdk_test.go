package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

type fakeTasks struct {
	daemonv1.TaskService
	newErr  error
	goErr   error
	newSeen *daemonv1.TaskServiceNewRequest
	goSeen  *daemonv1.TaskServiceGoRequest
}

func (f *fakeTasks) New(_ context.Context, in *daemonv1.TaskServiceNewRequest) (*daemonv1.TaskServiceNewResponse, error) {
	f.newSeen = in
	if f.newErr != nil {
		return nil, f.newErr
	}
	return &daemonv1.TaskServiceNewResponse{SchemaVersion: "1.0.0", Task: in.GetTask(), Branch: "feat/x", Path: "/w/x", Port: 4000}, nil
}

func (f *fakeTasks) Go(_ context.Context, in *daemonv1.TaskServiceGoRequest) (*daemonv1.TaskServiceGoResponse, error) {
	f.goSeen = in
	if f.goErr != nil {
		return nil, f.goErr
	}
	return &daemonv1.TaskServiceGoResponse{SchemaVersion: "1.0.0", Task: in.GetSelector(), Path: "/w/x"}, nil
}

func useTasks(t *testing.T, daemon, local daemonv1.TaskService, dialErr error) {
	t.Helper()
	oldDial, oldLocal := dialTask, localTask
	t.Cleanup(func() { dialTask, localTask = oldDial, oldLocal })
	dialTask = func() (daemonv1.TaskService, func(), error) { return daemon, func() {}, dialErr }
	localTask = local
}

func TestNewAndGoCallTheDaemonThroughTheSDK(t *testing.T) {
	worktreeRepo(t)
	daemon, local := &fakeTasks{}, &fakeTasks{}
	useTasks(t, daemon, local, nil)
	if out, stderr, code := call(t, "new", "x", "--no-setup", "--json"); code != 0 || !strings.Contains(out, `"schema":{"version":"1.0.0"}`) || !strings.Contains(out, `"branch":"feat/x"`) {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	if out, _, code := call(t, "go", "x", "--no-cd"); code != 0 || out != "/w/x\n" {
		t.Fatalf("%d %q", code, out)
	}
	if daemon.newSeen == nil || daemon.goSeen == nil || local.newSeen != nil || local.goSeen != nil {
		t.Fatalf("%+v %+v", daemon, local)
	}
	if len(daemon.newSeen.GetEnviron()) == 0 {
		t.Fatal("the caller's environment was not sent")
	}
}

func TestNewRunsHereWhenTheDaemonLacksTheCall(t *testing.T) {
	worktreeRepo(t)
	local := &fakeTasks{}
	useTasks(t, &fakeTasks{newErr: rpc.Errorf(rpc.Unimplemented, "no such call")}, local, nil)
	if _, stderr, code := call(t, "new", "x", "--no-setup"); code != 0 || local.newSeen == nil {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestADaemonRefusalIsNotRetriedHere(t *testing.T) {
	worktreeRepo(t)
	local := &fakeTasks{}
	useTasks(t, &fakeTasks{newErr: rpc.Errorf(rpc.FailedPrecondition, "refused")}, local, nil)
	if _, stderr, code := call(t, "new", "x", "--no-setup"); code != 1 || local.newSeen != nil || !strings.Contains(stderr, "refused") {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestNewRunsHereWhenTheClientCannotBeMade(t *testing.T) {
	worktreeRepo(t)
	local := &fakeTasks{}
	useTasks(t, nil, local, errors.New("no home"))
	if _, stderr, code := call(t, "new", "x", "--no-setup"); code != 0 || local.newSeen == nil {
		t.Fatalf("%d %q", code, stderr)
	}
}

func TestVerbsWorkWithNoDaemonListening(t *testing.T) {
	worktreeRepo(t)
	if out, stderr, code := call(t, "new", "one", "--no-setup"); code != 0 || !strings.Contains(out, "feat/one") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
}

func TestFullTableShowsTheCellsAndASummary(t *testing.T) {
	worktreeRepo(t)
	call(t, "new", "one", "--no-setup")
	out, _, code := call(t, "ls", "--full")
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"REMOTE", "AGENT", "PR", "CI", "REVIEW", "PORTS", "CONTAINERS", "DISK", "AGE", "2 worktrees"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in %q", want, out)
		}
	}
}

func TestTerminalListPaintsTheSkeletonThenReplacesIt(t *testing.T) {
	worktreeRepo(t)
	original := stdoutIsTerminal
	defer func() { stdoutIsTerminal = original }()
	stdoutIsTerminal = func(any) bool { return true }
	out, _, code := call(t, "ls")
	if code != 0 || strings.Count(out, "TASK") != 2 || !strings.Contains(out, "\x1b[2A\x1b[J") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestPipedListPrintsOneTable(t *testing.T) {
	worktreeRepo(t)
	if out, _, _ := call(t, "ls"); strings.Count(out, "TASK") != 1 || strings.Contains(out, "\x1b[") {
		t.Fatalf("%q", out)
	}
}
