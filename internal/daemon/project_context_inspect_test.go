package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	contextrepo "github.com/arafathusayn/herdsman/internal/context"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// An inspect daemon reads a project's history and changes nothing in it.
func TestInspectServesContextReadsOnly(t *testing.T) {
	h := newHarness(t)
	sdk := dialHarness(t, h)
	ctx := context.Background()
	root, inside := boundFolder(t)
	if err := os.CopyFS(root, os.DirFS("../context/testdata/forked")); err != nil {
		t.Fatal(err)
	}
	h.cfg.Inspect = true
	h.restart()
	scope := &daemonv1.Scope{Folder: inside}

	before, err := os.ReadDir(filepath.Join(root, contextrepo.EventsDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.Project.ContextCheck(ctx, &daemonv1.ProjectServiceContextCheckRequest{Scope: scope}); err != nil {
		t.Errorf("check: %v", err)
	}
	if _, err := sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope}); err != nil {
		t.Errorf("log: %v", err)
	}
	_, err = sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope})
	wantCode(t, err, rpc.FailedPrecondition)
	_, err = sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{Scope: scope, Actor: "someone", Body: "refused"})
	wantCode(t, err, rpc.FailedPrecondition)

	after, err := os.ReadDir(filepath.Join(root, contextrepo.EventsDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("%d files, had %d", len(after), len(before))
	}
}
