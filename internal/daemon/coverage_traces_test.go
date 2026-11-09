package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/project"
	"github.com/arafathusayn/herdsman/internal/suggest"
	"github.com/arafathusayn/herdsman/internal/trace"
	"github.com/arafathusayn/herdsman/sdk/config"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	tracev1 "github.com/arafathusayn/herdsman/sdk/trace/v1"
)

// The service's own guard, apart from the daemon's guard in front of it.
func TestSuggestRPCInInspectRefusesEveryChange(t *testing.T) {
	r, ctx := &suggestRPC{s: &SuggestService{}, inspect: true}, context.Background()
	calls := map[string]func() error{
		"Add":           func() error { _, err := r.Add(ctx, &suggestv1.AddRequest{}); return err },
		"SourceAdd":     func() error { _, err := r.SourceAdd(ctx, &suggestv1.SourceAddRequest{}); return err },
		"SourceRemove":  func() error { _, err := r.SourceRemove(ctx, &suggestv1.SourceRemoveRequest{}); return err },
		"SourcePreview": func() error { _, err := r.SourcePreview(ctx, &suggestv1.SourcePreviewRequest{}); return err },
		"SourcePin":     func() error { _, err := r.SourcePin(ctx, &suggestv1.SourcePinRequest{}); return err },
	}
	for name, call := range calls {
		if err := call(); failureCode(t, err) != rpc.FailedPrecondition {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTraceRPCInInspectRefusesEveryChange(t *testing.T) {
	r, ctx := NewTraceService(true), context.Background()
	calls := map[string]func() error{
		"Export":     func() error { _, err := r.Export(ctx, &tracev1.ExportRequest{}); return err },
		"AddSpan":    func() error { _, err := r.AddSpan(ctx, &tracev1.AddSpanRequest{}); return err },
		"RecordRun":  func() error { _, err := r.RecordRun(ctx, &tracev1.RecordRunRequest{}); return err },
		"AddFinding": func() error { _, err := r.AddFinding(ctx, &tracev1.AddFindingRequest{}); return err },
		"SetOutcome": func() error { _, err := r.SetOutcome(ctx, &tracev1.SetOutcomeRequest{}); return err },
	}
	for name, call := range calls {
		if err := call(); failureCode(t, err) != rpc.FailedPrecondition {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRecordRunReportsAFolderThatIsNotThere(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	run := &tracev1.Run{Task: "t", Arm: "a", DurationMs: 1}
	_, err := NewTraceService(false).RecordRun(context.Background(), &tracev1.RecordRunRequest{Suite: missing, Run: run})
	if failureCode(t, err) != rpc.NotFound {
		t.Fatalf("%v", err)
	}
}

func TestExportReportsARouteThatIsNotThere(t *testing.T) {
	_, err := NewTraceService(false).Export(context.Background(), &tracev1.ExportRequest{Route: filepath.Join(t.TempDir(), "gone"), Endpoint: "http://127.0.0.1:1"})
	if failureCode(t, err) != rpc.NotFound {
		t.Fatalf("%v", err)
	}
}

func TestATurnThatCannotBeWrittenIsOnlyLogged(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	folder := routeFolderFixture(t, filepath.Join(root, "repo"), "r-2026-01-02")
	h := newHarness(t)
	h.route("r")
	if err := os.WriteFile(filepath.Join(folder, "trace"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.d.recordTurn("r", "orch", "t")
}

func TestLabelsAreEmptyWhenTheStoreCannotAnswer(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(root, project.ConfigFile), boundConfig)
	folder := filepath.Join(root, "db")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(folder, "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := projectLabels(db, root); got != nil {
		t.Fatalf("%v", got)
	}
}

func brokenConfig(t *testing.T, s *SuggestService) {
	t.Helper()
	writeProjectFile(t, filepath.Join(s.configDir(), "config.toml"), "[broken\n")
}

func TestAConfigurationThatCannotBeParsedIsReported(t *testing.T) {
	s := newSuggestService(t)
	brokenConfig(t, s)
	if _, err := s.pins(""); err == nil {
		t.Fatal("reading pins must report a broken configuration")
	}
	if _, err := s.changeablePins(); err == nil {
		t.Fatal("changing pins must report a broken configuration")
	}
}

func TestAConfigurationThatChangesWhileReadIsReported(t *testing.T) {
	s := newSuggestService(t)
	writeProjectFile(t, filepath.Join(s.configDir(), "config.toml"), "")
	previous := readUserConfig
	readUserConfig = func(string) (*config.Result, error) { return nil, errors.New("gone") }
	t.Cleanup(func() { readUserConfig = previous })
	if _, err := s.changeablePins(); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("%v", err)
	}
}

func TestAnOlderFileThatCannotBeReadStopsAChange(t *testing.T) {
	s := newSuggestService(t)
	if err := os.MkdirAll(s.sourcesPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.changeablePins(); err == nil {
		t.Fatal("an unreadable older file must be an error")
	}
}

func TestStorePinsReportsAConfigFileItCannotCreate(t *testing.T) {
	s := newSuggestService(t)
	if err := os.MkdirAll(filepath.Join(s.configDir(), "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.storePins(nil); err == nil {
		t.Fatal("a folder in the file's place must be an error")
	}
}

func TestAnOlderFileThatCannotBeRemovedIsReported(t *testing.T) {
	s := newSuggestService(t)
	if err := os.MkdirAll(s.sourcesPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(s.sourcesPath(), "keep"), "")
	if err := s.storePins(nil); err == nil {
		t.Fatal("an older file that stays must be reported")
	}
}

func TestTraceFailureMapsARouteFile(t *testing.T) {
	route := filepath.Join(t.TempDir(), "file")
	writeProjectFile(t, route, "")
	if _, err := trace.Collect(trace.Sources{Route: route}); err == nil {
		t.Fatal("a file is not a route")
	}
}

func TestStorePinsReportsAFolderItCannotCreate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	s := newSuggestService(t)
	if err := os.MkdirAll(s.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.ConfigDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.ConfigDir, 0o700) })
	if err := s.storePins(nil); err == nil {
		t.Fatal("a folder that cannot be created must be an error")
	}
}

func TestStorePinsReportsAConfigurationItCannotReplace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	s := newSuggestService(t)
	writeProjectFile(t, filepath.Join(s.configDir(), "config.toml"), "")
	if err := os.Chmod(s.configDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.configDir(), 0o700) })
	pin := suggest.Pin{URL: "https://example.com/a", Commit: strings.Repeat("a", 40)}
	if err := s.storePins([]suggest.Pin{pin}); err == nil {
		t.Fatal("a configuration that cannot be replaced must be an error")
	}
}
