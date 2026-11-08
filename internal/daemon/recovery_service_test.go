package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
)

func recoveryRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(shortDir(t), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExportThroughTheDaemonImportsIntoAnEmptyState(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	defer stop()
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	if _, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op); err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("export ended %s: %s", op.State, op.Reason)
	}
	state := filepath.Join(t.TempDir(), "imported")
	if err := ops.Import(context.Background(), dest, state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "daemon", "account.db")); err != nil {
		t.Fatal(err)
	}
}

func TestUndoOfAnUnknownSnapshotIsNotFound(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), false)
	defer stop()
	var res ops.UndoResult
	_, err := c.Do(context.Background(), "POST", "/v1/undo", ops.UndoRequest{Snapshot: "nope"}, &res)
	var e *APIError
	if !errors.As(err, &e) || e.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestExportOverAnExistingFolderIsRefused(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), false)
	defer stop()
	var op ops.Operation
	_, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: t.TempDir()}, &op)
	var e *APIError
	if !errors.As(err, &e) || e.Code != "failed_precondition" {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectRefusesUndoAndExport(t *testing.T) {
	c, stop := serving(t, recoveryRoot(t), true)
	defer stop()
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	_, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op)
	var e *APIError
	if !errors.As(err, &e) || !strings.Contains(e.Message, "inspect mode") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(dest); !os.IsNotExist(serr) {
		t.Fatalf("inspect exported: %v", serr)
	}
}

func TestRecoveryCallImportsInThisProcess(t *testing.T) {
	root := recoveryRoot(t)
	c, stop := serving(t, root, false)
	dest := filepath.Join(t.TempDir(), "export")
	var op ops.Operation
	if _, err := c.Do(context.Background(), "POST", "/v1/export", ExportRequest{Dest: dest}, &op); err != nil {
		t.Fatal(err)
	}
	stop()
	state := filepath.Join(t.TempDir(), "imported")
	raw, _ := json.Marshal(ImportRequest{Source: dest, StateDir: state})
	out, err := RecoveryCaller("test")(context.Background(), "Import", raw)
	if err != nil {
		t.Fatal(err)
	}
	var got ImportResponse
	if err := json.Unmarshal(out, &got); err != nil || got.StateDir != state {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := RecoveryCaller("test")(context.Background(), "Nope", raw); err == nil {
		t.Fatal("an unknown method was accepted")
	}
}
