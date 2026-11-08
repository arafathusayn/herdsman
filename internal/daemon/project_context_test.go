package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	contextrepo "github.com/arafathusayn/herdsman/internal/context"
	"github.com/arafathusayn/herdsman/internal/project"
	"github.com/arafathusayn/herdsman/sdk/client"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

const bindingText = "[project]\nid = \"01KB2A00000000000000000001\"\nname = \"sample\"\n"

// boundFolder makes a git folder bound to a project, with a subfolder a
// command can run in.
func boundFolder(t *testing.T) (root, inside string) {
	t.Helper()
	root = t.TempDir()
	inside = filepath.Join(root, "repo", "src")
	for _, dir := range []string{filepath.Join(root, ".git"), inside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte(bindingText), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, inside
}

func contextClient(t *testing.T) *client.Client {
	t.Helper()
	return dialHarness(t, newHarness(t))
}

func wantCode(t *testing.T, err error, code rpc.Code) {
	t.Helper()
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error %v, want code %v", err, code)
	}
}

func TestContextVerbsOverTheSDK(t *testing.T) {
	sdk := contextClient(t)
	ctx := context.Background()
	root, inside := boundFolder(t)
	scope := &daemonv1.Scope{Folder: inside}

	init, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if init.GetFolder() != root {
		t.Fatalf("folder %q, want the bound folder %q", init.GetFolder(), root)
	}

	note, err := sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{
		Scope: scope, Actor: "someone", Task: "alpha", Repos: []string{"core"}, Body: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := note.GetEvent()
	if event.GetKind() != contextrepo.KindNote || event.GetActor() != "someone" || event.GetAt() == 0 || event.GetBody() != "first\n" {
		t.Fatalf("event %+v", event)
	}
	if _, err := os.Stat(filepath.Join(root, contextrepo.EventsDir, event.GetId()+".md")); err != nil {
		t.Fatal(err)
	}

	second, err := sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{
		Scope: &daemonv1.Scope{Project: "@", Folder: inside}, Actor: "someone", Parents: []string{event.GetId()}, Body: "second",
	})
	if err != nil {
		t.Fatal(err)
	}

	check, err := sdk.Project.ContextCheck(ctx, &daemonv1.ProjectServiceContextCheckRequest{Scope: scope})
	if err != nil || check.GetEvents() != 2 || len(check.GetProblems()) != 0 {
		t.Fatalf("check %+v, %v", check, err)
	}

	log, err := sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope, Task: "alpha"})
	if err != nil || len(log.GetEvents()) != 1 || log.GetEvents()[0].GetId() != event.GetId() {
		t.Fatalf("log %+v, %v", log, err)
	}
	if len(log.GetHeads()) != 1 || log.GetHeads()[0] != second.GetEvent().GetId() {
		t.Fatalf("heads %v", log.GetHeads())
	}
}

func TestContextCheckReportsProblemsAsAnAnswer(t *testing.T) {
	sdk := contextClient(t)
	ctx := context.Background()
	root, inside := boundFolder(t)
	scope := &daemonv1.Scope{Folder: inside}
	if _, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope}); err != nil {
		t.Fatal(err)
	}
	name := "01KB1A00000000000000000001.md"
	if err := os.WriteFile(filepath.Join(root, contextrepo.EventsDir, name), []byte("not an event"), 0o644); err != nil {
		t.Fatal(err)
	}
	check, err := sdk.Project.ContextCheck(ctx, &daemonv1.ProjectServiceContextCheckRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(check.GetProblems()) != 1 || check.GetProblems()[0].GetFile() != name ||
		check.GetProblems()[0].GetCode() != contextrepo.CodeMalformed || check.GetProblems()[0].GetDetail() == "" {
		t.Fatalf("problems %+v", check.GetProblems())
	}
	_, err = sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope})
	wantCode(t, err, rpc.FailedPrecondition)
	_, err = sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{Scope: scope, Actor: "a"})
	wantCode(t, err, rpc.FailedPrecondition)
}

func TestProjectLogGoldenOverTheSDK(t *testing.T) {
	sdk := contextClient(t)
	ctx := context.Background()
	root, inside := boundFolder(t)
	if err := os.CopyFS(root, os.DirFS("../context/testdata/forked")); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../context/testdata/forked.log.txt")
	if err != nil {
		t.Fatal(err)
	}
	scope := &daemonv1.Scope{Folder: inside}
	log, err := sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if log.GetText() != string(want) {
		t.Fatalf("text:\n%s", log.GetText())
	}
	last := log.GetEvents()[len(log.GetEvents())-1]
	if last.GetCommits()["core"] != "abc1234" || last.GetExtra()["reviewer"] != "kim" || len(last.GetParents()) != 2 {
		t.Fatalf("event %+v", last)
	}
	since := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC).UnixMilli()
	log, err = sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope, Since: since})
	if err != nil || len(log.GetEvents()) != 1 {
		t.Fatalf("since: %+v, %v", log, err)
	}
}

func TestContextRefusals(t *testing.T) {
	sdk := contextClient(t)
	ctx := context.Background()
	_, inside := boundFolder(t)
	scope := &daemonv1.Scope{Folder: inside}

	check := func(s *daemonv1.Scope) error {
		_, err := sdk.Project.ContextCheck(ctx, &daemonv1.ProjectServiceContextCheckRequest{Scope: s})
		return err
	}
	wantCode(t, check(&daemonv1.Scope{Project: "sample", Folder: inside}), rpc.FailedPrecondition)
	wantCode(t, check(&daemonv1.Scope{Folder: t.TempDir()}), rpc.FailedPrecondition)
	wantCode(t, check(&daemonv1.Scope{Folder: "relative/path"}), rpc.InvalidArgument)
	wantCode(t, check(nil), rpc.InvalidArgument)
	wantCode(t, check(scope), rpc.FailedPrecondition)

	noGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(noGit, project.ConfigFile), []byte(bindingText), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: &daemonv1.Scope{Folder: noGit}})
	wantCode(t, err, rpc.FailedPrecondition)

	if _, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope}); err != nil {
		t.Fatal(err)
	}
	_, err = sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{Scope: scope})
	wantCode(t, err, rpc.InvalidArgument)

	_, err = sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: &daemonv1.Scope{Project: "sample"}})
	wantCode(t, err, rpc.FailedPrecondition)
	_, err = sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: &daemonv1.Scope{Project: "sample"}})
	wantCode(t, err, rpc.FailedPrecondition)
	_, err = sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{Scope: &daemonv1.Scope{Project: "sample"}, Actor: "a"})
	wantCode(t, err, rpc.FailedPrecondition)
}

func TestContextFailureKeepsAnUnexpectedErrorInternal(t *testing.T) {
	var failure *rpc.Error
	if err := contextFailure(errors.New("disk on fire")); !errors.As(err, &failure) || failure.Code != rpc.Internal {
		t.Fatalf("error %v", err)
	}
}
