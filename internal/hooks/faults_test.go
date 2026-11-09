package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/wtree"
)

// blocked returns a path whose parent is a file, so nothing can be made there.
func blocked(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(file, "below")
}

func TestApprovalsReportAnUnreadableFile(t *testing.T) {
	store := &Approvals{Path: t.TempDir()} // A folder cannot be read as a file.
	if _, err := store.Approved("p", "x"); err == nil {
		t.Fatal("no error from Approved")
	}
	if _, err := store.Unapproved("p", planOf(t, "true")); err == nil {
		t.Fatal("no error from Unapproved")
	}
	if err := store.Approve("p", []string{"x"}); err == nil {
		t.Fatal("no error from Approve")
	}
}

func TestApprovalsAcceptAnEmptyDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.toml")
	os.WriteFile(path, nil, 0o600)
	if ok, err := (&Approvals{Path: path}).Approved("p", "x"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestApproveReportsWhereItCannotWrite(t *testing.T) {
	// A dangling link reads as missing on every platform, and cannot be made
	// into a folder. A path through a file fails at the read on Linux instead.
	base := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "gone"), link); err != nil {
		t.Fatal(err)
	}
	if err := (&Approvals{Path: filepath.Join(link, "a.toml")}).Approve("p", []string{"x"}); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)
	if err := (&Approvals{Path: filepath.Join(dir, "a.toml")}).Approve("p", []string{"x"}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatal(err)
	}
}

func TestParseRefusesACommandOfTheWrongType(t *testing.T) {
	if _, err := Parse(map[string]any{"pre-start": map[string]any{"a": 3}}); err == nil {
		t.Fatal("accepted")
	}
}

func TestPlanOfAConfigWithoutValuesStillRenders(t *testing.T) {
	config, _ := Parse(map[string]any{"pre-start": "echo {{ hook_type }}"})
	plan, err := config.Plan("pre-start", wtree.Vars{})
	if err != nil || plan[0][0].Rendered != "echo pre-start" {
		t.Fatalf("%v %v", plan, err)
	}
}

func TestRunAndStartReportAFolderTheyCannotMake(t *testing.T) {
	r := &Runner{LogDir: blocked(t), Dir: t.TempDir()}
	if _, err := r.Run(context.Background(), "pre-start", planOf(t, "true")); err == nil {
		t.Fatal("Run")
	}
	if _, err := r.Start("pre-start", planOf(t, "true")); err == nil {
		t.Fatal("Start")
	}
}

func TestEmptyPlansDoNothing(t *testing.T) {
	r, _ := runner(t)
	if result, err := r.Run(context.Background(), "pre-start", nil); err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
	if logs, err := r.Start("pre-start", nil); err != nil || logs != nil {
		t.Fatalf("%v %v", logs, err)
	}
}

func TestACommandWhoseLogCannotOpenFails(t *testing.T) {
	r, dir := runner(t)
	result := r.runOne(context.Background(), "pre-start", planOf(t, "true")[0][0], filepath.Join(dir, "no", "such", "log"))
	if result.Exit != -1 {
		t.Fatalf("%+v", result)
	}
}

func TestACommandThatCannotStartIsLogged(t *testing.T) {
	r, dir := runner(t)
	r.Dir = filepath.Join(dir, "gone")
	logPath := filepath.Join(dir, "log")
	if result := r.runOne(context.Background(), "pre-start", planOf(t, "true")[0][0], logPath); result.Exit != -1 {
		t.Fatalf("%+v", result)
	}
	if data, _ := os.ReadFile(logPath); !strings.Contains(string(data), "did not start") {
		t.Fatalf("%q", data)
	}
}

func TestAResultThatCannotBeWrittenIsReported(t *testing.T) {
	if err := writeResult(&Result{File: blocked(t)}); err == nil {
		t.Fatal("no error")
	}
}

func TestSavePlanReportsWhereItCannotWrite(t *testing.T) {
	r, _ := runner(t)
	if err := r.savePlan(blocked(t), "pre-start", "f", nil); err == nil {
		t.Fatal("no error")
	}
}

func TestStartReportsADirectoryItCannotEnter(t *testing.T) {
	r, dir := runner(t)
	r.Dir = filepath.Join(dir, "gone")
	if _, err := r.Start("pre-start", planOf(t, "true")); err == nil {
		t.Fatal("no error")
	}
}
