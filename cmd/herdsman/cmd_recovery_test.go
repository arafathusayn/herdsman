package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// fakeOps answers as the daemon would and records what it was asked.
type fakeOps struct {
	daemonv1.OpsService
	undo   *daemonv1.UndoRequest
	export *daemonv1.ExportRequest
	fail   error
}

func (f *fakeOps) Undo(_ context.Context, q *daemonv1.UndoRequest) (*daemonv1.UndoResponse, error) {
	f.undo = q
	return &daemonv1.UndoResponse{OperationId: "OP1", Target: "/x/tree", Original: true}, f.fail
}

func (f *fakeOps) Export(_ context.Context, q *daemonv1.ExportRequest) (*daemonv1.ExportResponse, error) {
	f.export = q
	return &daemonv1.ExportResponse{OperationId: "OP2", Dest: q.GetDest()}, f.fail
}

func withFakeOps(t *testing.T, fail error) *fakeOps {
	t.Helper()
	fake := &fakeOps{fail: fail}
	saved := dialOps
	dialOps = func() (daemonv1.OpsService, func(), error) { return fake, func() {}, nil }
	t.Cleanup(func() { dialOps = saved })
	return fake
}

func withFakeImport(t *testing.T, fail error) *[2]string {
	t.Helper()
	got := &[2]string{}
	saved := importCall
	importCall = func(_ context.Context, source, state string) (string, error) {
		got[0], got[1] = source, state
		return "/x/state", fail
	}
	t.Cleanup(func() { importCall = saved })
	return got
}

func TestUndoVerb(t *testing.T) {
	fake := withFakeOps(t, nil)
	out, errOut, code := call(t, "undo", "OP1", "--to", "/x/tree")
	if code != 0 || !strings.Contains(out, "/x/tree") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if fake.undo.GetSnapshot() != "OP1" || fake.undo.GetTarget() != "/x/tree" {
		t.Fatalf("%v", fake.undo)
	}
	out, _, code = call(t, "undo", "OP1", "--json")
	var parsed map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &parsed) != nil || parsed["target"] != "/x/tree" {
		t.Fatalf("%d %q", code, out)
	}
}

func TestExportVerbMakesTheDestinationAbsolute(t *testing.T) {
	fake := withFakeOps(t, nil)
	t.Chdir(t.TempDir())
	out, errOut, code := call(t, "export", "backup")
	if code != 0 || !strings.Contains(out, "OP2") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if dest := fake.export.GetDest(); !filepath.IsAbs(dest) || filepath.Base(dest) != "backup" {
		t.Fatalf("dest = %q", dest)
	}
}

func TestImportVerb(t *testing.T) {
	got := withFakeImport(t, nil)
	t.Chdir(t.TempDir())
	out, errOut, code := call(t, "import", "backup", "--state-dir", "/x/state")
	if code != 0 || !strings.Contains(out, "/x/state") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if !filepath.IsAbs(got[0]) || got[1] != "/x/state" {
		t.Fatalf("%v", got)
	}
}

func TestRecoveryVerbsReportUsageAndFailure(t *testing.T) {
	failure := errors.New("the cap is full")
	withFakeOps(t, failure)
	withFakeImport(t, failure)
	for _, args := range [][]string{{"undo"}, {"export"}, {"import"}, {"undo", "a", "b"}, {"export", "--nope", "x"}} {
		if _, _, code := call(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	for _, args := range [][]string{{"undo", "a"}, {"export", "/x"}, {"import", "/x"}} {
		_, errOut, code := call(t, args...)
		if code != 1 || !strings.Contains(errOut, "the cap is full") {
			t.Errorf("%v: exit %d, %q", args, code, errOut)
		}
	}
}

func TestVerbsReachTheDaemonThroughTheSDK(t *testing.T) {
	t.Setenv("HERDSMAN_STATE_DIR", t.TempDir())
	// No daemon is listening: the call reaches the socket and fails there.
	if _, errOut, code := call(t, "undo", "a"); code != 1 || errOut == "" {
		t.Fatalf("%d %q", code, errOut)
	}
	if _, errOut, code := call(t, "export", "/x"); code != 1 || errOut == "" {
		t.Fatalf("%d %q", code, errOut)
	}
}
