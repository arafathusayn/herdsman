package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/snapshot"
)

func (f *cleanFixture) config(text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.main), "herdsman.config.json"), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cleanFixture) cleanTask(req WorktreeCleanRequest) WorktreeCleanReport {
	f.t.Helper()
	req.Dir = f.main
	resp, err := f.svc.Clean(context.Background(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.Reports[0]
}

func TestSnapshotSettingsDefaultWithoutAConfigFile(t *testing.T) {
	got, err := loadSnapshotSettings(t.TempDir())
	if err != nil || !slices.Equal(got.Rebuildable, snapshot.DefaultRebuildable()) || got.FileCap != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSnapshotSettingsReadTheSection(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "herdsman.config.json")
	if err := os.WriteFile(path, []byte(`{"snapshot":{"rebuildable":["cache"],"file_cap":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSnapshotSettings(folder)
	if err != nil || !slices.Equal(got.Rebuildable, []string{"cache"}) || got.FileCap != 5 {
		t.Fatalf("%+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"snapshot":{"rebuildable":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err = loadSnapshotSettings(folder); err != nil || got.Rebuildable == nil || len(got.Rebuildable) != 0 {
		t.Fatalf("an explicit empty list means nothing is rebuildable: %+v %v", got, err)
	}
}

func TestSnapshotSettingsRefuseWhatTheyCannotUse(t *testing.T) {
	for _, text := range []string{`not json`, `{"snapshot":{"file_cap":-1}}`} {
		folder := t.TempDir()
		if err := os.WriteFile(filepath.Join(folder, "herdsman.config.json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSnapshotSettings(folder); err == nil {
			t.Errorf("%s accepted", text)
		}
	}
	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, "herdsman.config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshotSettings(unreadable); err == nil {
		t.Error("a folder as the file")
	}
}

func TestAConfiguredPatternDecidesWhatIsRebuildable(t *testing.T) {
	f := newCleanFixture(t, `[]`)
	tree, _ := f.addTask("a", true, time.Unix(1000, 0))
	f.config(`{"snapshot":{"rebuildable":["node_modules"]}}`)
	if err := os.WriteFile(filepath.Join(tree, ".gitignore"), []byte("node_modules\nbuild\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, tree, "add", ".")
	fixtureGit(t, tree, "commit", "-qm", "ignore")
	fixtureGit(t, f.main, "merge", "-q", "--no-edit", "feat/a")
	for _, name := range []string{"node_modules/x", "build/y"} {
		path := filepath.Join(tree, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := f.cleanTask(WorktreeCleanRequest{Tasks: []string{"a"}, PullRequestHeads: map[string]string{}})
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if !slices.Equal(report.Rebuildable, []string{"node_modules"}) {
		t.Fatalf("rebuildable %v: build is not a configured pattern and was captured", report.Rebuildable)
	}
}

func TestABadSnapshotSectionStopsPlanAndClean(t *testing.T) {
	f := newCleanFixture(t, `[]`)
	f.addTask("a", true, time.Unix(1000, 0))
	f.config(`{"snapshot":{"file_cap":-1}}`)
	if _, err := f.svc.CleanPlan(context.Background(), WorktreeCleanPlanRequest{Dir: f.main, Selector: "a"}); err == nil {
		t.Error("plan")
	}
	if _, err := f.svc.Clean(context.Background(), WorktreeCleanRequest{Dir: f.main, Tasks: []string{"a"}, PullRequestHeads: map[string]string{}}); err == nil {
		t.Error("clean")
	}
}

func TestTheFirstCleanOfARepositoryShowsTheRebuildableSetOnce(t *testing.T) {
	f := newCleanFixture(t, `[]`)
	f.addTask("a", true, time.Unix(1000, 0))
	f.addTask("b", true, time.Unix(1000, 0))
	first := f.plan(WorktreeCleanPlanRequest{Selector: "a"})
	if !slices.Equal(first.FirstRebuildable, snapshot.DefaultRebuildable()) {
		t.Fatalf("first %v", first.FirstRebuildable)
	}
	if again := f.plan(WorktreeCleanPlanRequest{Selector: "a"}); len(again.FirstRebuildable) == 0 {
		t.Fatal("a plan alone changed nothing, so the set is still unseen")
	}
	if report := f.cleanTask(WorktreeCleanRequest{Tasks: []string{"a"}, PullRequestHeads: map[string]string{}}); report.Error != "" {
		t.Fatal(report.Error)
	}
	if later := f.plan(WorktreeCleanPlanRequest{Selector: "b"}); len(later.FirstRebuildable) != 0 {
		t.Fatalf("later %v", later.FirstRebuildable)
	}
}

func TestArchiveOnlyPassesThroughAndKeepsTheTree(t *testing.T) {
	f := newCleanFixture(t, `[]`)
	tree, _ := f.addTask("a", false, time.Unix(1000, 0))
	report := f.cleanTask(WorktreeCleanRequest{Tasks: []string{"a"}, ArchiveOnly: true, PullRequestHeads: map[string]string{}})
	if report.Error != "" || !report.ArchiveOnly || report.Snapshot == "" {
		t.Fatalf("%+v", report)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatal(err)
	}
}

func TestThePlanSaysAnEquivalentBranchGoesOnlyWithTheFlag(t *testing.T) {
	f := newCleanFixture(t, `[]`)
	_, head := f.addTask("a", false, time.Unix(1000, 0))
	if err := os.WriteFile(filepath.Join(f.main, "other.txt"), []byte("main moved on"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f.main, "add", ".")
	fixtureGit(t, f.main, "commit", "-qm", "other")
	fixtureGit(t, f.main, "cherry-pick", head)
	if got := f.plan(WorktreeCleanPlanRequest{Selector: "a"}).Plans[0].BranchAction; got != "keep" {
		t.Fatalf("without the flag: %s", got)
	}
	if got := f.plan(WorktreeCleanPlanRequest{Selector: "a", DeleteBranch: true}).Plans[0].BranchAction; got != "delete" {
		t.Fatalf("with the flag: %s", got)
	}
}
