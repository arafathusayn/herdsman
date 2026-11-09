package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

func testFixtures(t *testing.T) *Fixtures {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := BuildFixtures(root, 8, 9, 2)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFixturesHoldEveryIntegrationState(t *testing.T) {
	f := testFixtures(t)
	counts := func(branch string) string {
		out, err := f.git.run(f.Repo20, "rev-list", "--left-right", "--count", "main..."+branch)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(out), " ")
	}
	if got := counts(branchAhead); !strings.HasPrefix(got, "0 ") || strings.HasSuffix(got, " 0") {
		t.Errorf("ahead must be ahead only: %s", got)
	}
	if got := counts(branchBehind); strings.HasPrefix(got, "0 ") || !strings.HasSuffix(got, " 0") {
		t.Errorf("behind: %s", got)
	}
	if got := counts(branchDiverged); strings.HasPrefix(got, "0 ") || strings.HasSuffix(got, " 0") {
		t.Errorf("diverged: %s", got)
	}
	if got := counts(branchIntegrated); !strings.HasSuffix(got, " 0") {
		t.Errorf("integrated: %s", got)
	}
	if out, err := f.git.run(f.Repo20, "merge-tree", "--write-tree", "main", branchConflicting); err == nil {
		t.Errorf("conflicting merges cleanly: %s", out)
	}
}

func TestFixturesHaveTheRequestedWorktreesAndIgnoredTree(t *testing.T) {
	f := testFixtures(t)
	out, err := f.git.run(f.Repo50, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "worktree "); got != 10 {
		t.Fatalf("the main checkout and nine linked: got %d", got)
	}
	info, err := os.Stat(filepath.Join(f.Repo20, "node_modules", cacheMarker))
	if err != nil || info.Size() != 1<<20 {
		t.Fatalf("marker: %v %v", info, err)
	}
	status, err := f.git.run(f.Repo20, "status", "--porcelain")
	if err != nil || status != "" {
		t.Fatalf("the ignored tree must not show: %q %v", status, err)
	}
}

func TestBuildRepositoryRefusesTooFewWorktrees(t *testing.T) {
	if _, err := buildRepository(t.TempDir(), fixtureGit{home: t.TempDir()}, 1, 0); err == nil {
		t.Fatal("want an error")
	}
}

func TestFakeGHAnswersBothLookups(t *testing.T) {
	f := testFixtures(t)
	env := toolEnv(f)
	gh := filepath.Join(f.Bin, "gh")
	repo, err := execStep(env, Step{Dir: f.Root, Argv: []string{gh, "repo", "set-default", "--view"}})
	if err != nil || strings.TrimSpace(repo) != remoteName {
		t.Fatalf("%q %v", repo, err)
	}
	pr, err := execStep(env, Step{Dir: f.Root, Argv: []string{gh, "api", "repos/acme/repo/pulls/7"}, Must: branchRequest})
	if err != nil {
		t.Fatalf("%q %v", pr, err)
	}
	list, err := execStep(env, Step{Dir: f.Root, Argv: []string{gh, "pr", "list"}})
	if err != nil || strings.TrimSpace(list) != "[]" {
		t.Fatalf("%q %v", list, err)
	}
}

func TestExecStepJudgesExitAndOutput(t *testing.T) {
	sh := func(script string) []string { return []string{"/bin/sh", "-c", script} }
	env := os.Environ()
	dir := t.TempDir()
	if _, err := execStep(env, Step{Dir: dir, Argv: sh("exit 2")}); err == nil {
		t.Error("exit 2 is a failure by default")
	}
	if _, err := execStep(env, Step{Dir: dir, Argv: sh("echo ok; exit 2"), Accept: []int{2}, Must: "ok"}); err != nil {
		t.Error(err)
	}
	if _, err := execStep(env, Step{Dir: dir, Argv: sh("echo usage; exit 2"), Accept: []int{2}, Must: `"lines"`}); err == nil {
		t.Error("an accepted exit code without the required output must fail")
	}
}

func TestCasesCoverEveryRequiredIDOnce(t *testing.T) {
	b := &Bench{Fixtures: &Fixtures{}, Tools: Tools{Herdsman: "h", Wt: "w"}}
	var got []string
	for _, c := range b.Cases() {
		got = append(got, c.ID)
	}
	want := append(append([]string{}, requiredVersus...), requiredBudgets...)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBudgetCasesCarryTheirBudgets(t *testing.T) {
	b := &Bench{Fixtures: &Fixtures{}, Tools: Tools{Herdsman: "h", Wt: "w"}}
	for _, c := range b.Cases() {
		switch c.ID {
		case CaseLedgerScan:
			if c.Kind != KindBudget || c.BudgetMS != 200 {
				t.Errorf("%+v", c)
			}
		case CaseTeardown:
			if c.Kind != KindBudget || c.BudgetMS != 3000 {
				t.Errorf("%+v", c)
			}
		}
	}
}

func findCase(t *testing.T, b *Bench, id string) Case {
	t.Helper()
	for _, c := range b.Cases() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no case %s", id)
	return Case{}
}

// A stand-in for both tools: it records its arguments and exits by rule.
const standIn = `#!/bin/sh
case "$*" in
  --version) echo "wt %s" ;;
  *teardown*--json*) echo '{"lines":[]}' ;;
esac
exit 0
`

func standInTools(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(standIn, version)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWritesAResultsFileAndMarksWhatCouldNotRun(t *testing.T) {
	repo := gitRepo(t)
	tool := standInTools(t, strings.TrimSpace(pinnedWt))
	out := t.TempDir()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	results, err := Run(Options{Herdsman: tool, Wt: tool, Repo: repo, Out: out, Work: work, Rounds: 2, IgnoredMB: 1, Small: 8, Large: 9}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	data, err := os.ReadFile(filepath.Join(out, results.FileName()))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseResults(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Commit != gitIn(t, repo, "rev-parse", "HEAD") || parsed.Wt != strings.TrimSpace(pinnedWt) || parsed.Git == "" || parsed.Go == "" || parsed.Machine == "" || parsed.Load == "" {
		t.Fatalf("versions and source must be recorded: %+v", parsed)
	}
	verdicts := map[string]string{}
	for _, c := range parsed.Cases {
		verdicts[c.ID] = c.Verdict
	}
	// The stand-in creates no worktree and stops no process, so these two
	// must not pass as measurements.
	if verdicts["new-shared-cache"] != VerdictUnavailable || verdicts[CaseTeardown] != VerdictUnavailable {
		t.Fatalf("got %v", verdicts)
	}
	if verdicts[CaseLedgerScan] != VerdictWithin {
		t.Fatalf("got %v", verdicts)
	}
}

func TestRunRefusesUncommittedMeasuredChanges(t *testing.T) {
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "internal/wtree/a.go"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := standInTools(t, strings.TrimSpace(pinnedWt))
	if _, err := Run(Options{Herdsman: tool, Wt: tool, Repo: repo, Out: t.TempDir(), Work: t.TempDir()}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("got %v", err)
	}
}

func TestRunRefusesAnotherWtVersion(t *testing.T) {
	repo := gitRepo(t)
	tool := standInTools(t, "v0.0.1")
	if _, err := Run(Options{Herdsman: tool, Wt: tool, Repo: repo, Out: t.TempDir(), Work: t.TempDir()}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "pins") {
		t.Fatalf("got %v", err)
	}
}

func TestDispatch(t *testing.T) {
	repo := gitRepo(t)
	head := gitIn(t, repo, "rev-parse", "HEAD")
	results := writeResults(t, completeResults(head))
	var log bytes.Buffer
	if got := dispatch([]string{"check", "--repo", repo, "--results", results}, &log); got != 0 {
		t.Fatalf("check: %d %s", got, log.String())
	}
	if got := dispatch([]string{"check", "--repo", repo, "--results", t.TempDir()}, &log); got != 1 {
		t.Fatalf("empty: %d", got)
	}
	if got := dispatch([]string{"check", "--nope"}, &log); got != 2 {
		t.Fatalf("bad flag: %d", got)
	}
	if got := dispatch(nil, &log); got != 2 {
		t.Fatalf("no verb: %d", got)
	}
	if got := dispatch([]string{"run"}, &log); got != 2 {
		t.Fatalf("run without tools: %d", got)
	}
	if got := dispatch([]string{"run", "--nope"}, &log); got != 2 {
		t.Fatalf("run bad flag: %d", got)
	}
}

func TestFakeGHDescribesBothRepositories(t *testing.T) {
	var reply struct {
		Base, Head struct {
			Ref  string
			Repo struct {
				Name  string
				Owner struct{ Login string }
			}
		}
	}
	script := fakeGHScript("abc")
	_, rest, _ := strings.Cut(script, "printf '%s\\n' '")
	body, _, _ := strings.Cut(rest, "' ;;")
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("%v: %s", err, script)
	}
	for _, side := range []string{reply.Base.Repo.Name + reply.Base.Repo.Owner.Login, reply.Head.Repo.Name + reply.Head.Repo.Owner.Login} {
		if side != "repoacme" {
			t.Fatalf("got %q", side)
		}
	}
}

func TestNewCaseChecksTheIgnoredTreeArrived(t *testing.T) {
	for _, copied := range []bool{true, false} {
		f := testFixtures(t)
		b := &Bench{Fixtures: f, Tools: Tools{Herdsman: "h", Wt: "w"}}
		c := findCase(t, b, "new-shared-cache")
		s := Sample{Tag: "x"}
		path := filepath.Join(f.Root, "created")
		if _, err := f.git.run(f.Repo20, "worktree", "add", "-q", "-b", "feat/new-x", path, "main"); err != nil {
			t.Fatal(err)
		}
		if copied {
			if err := writeFile(filepath.Join(path, "node_modules", cacheMarker), "x", 0o644); err != nil {
				t.Fatal(err)
			}
		}
		err := c.After(s)
		if copied && err != nil {
			t.Fatal(err)
		}
		if !copied && (err == nil || !strings.Contains(err.Error(), "ignored tree")) {
			t.Fatalf("got %v", err)
		}
		if err := c.Cleanup(s); err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(path); statErr == nil {
			t.Fatal("the sample's worktree must be removed either way")
		}
	}
}

func TestNewCaseTimesTheCopyForWt(t *testing.T) {
	b := &Bench{Fixtures: &Fixtures{}, Tools: Tools{Herdsman: "h", Wt: "w"}}
	c := findCase(t, b, "new-shared-cache")
	if got := len(c.Steps(Sample{Tag: "x", Wt: true})); got != 2 {
		t.Fatalf("wt creates and copies in two commands, got %d", got)
	}
	if got := len(c.Steps(Sample{Tag: "x"})); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestNewCaseCleanupToleratesAWorktreeNeverCreated(t *testing.T) {
	b := &Bench{Fixtures: testFixtures(t), Tools: Tools{Herdsman: "h", Wt: "w"}}
	if err := findCase(t, b, "new-shared-cache").Cleanup(Sample{Tag: "absent"}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRunsOnEveryExitAfterPreparation(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		before, step, after error
		cleaned             bool
	}{
		"success":        {cleaned: true},
		"failed command": {step: boom, cleaned: true},
		"failed check":   {after: boom, cleaned: true},
		"failed prepare": {before: boom},
	} {
		t.Run(name, func(t *testing.T) {
			cleaned := false
			c := Case{
				Steps:   func(Sample) []Step { return []Step{{}} },
				Before:  func(Sample) error { return tc.before },
				After:   func(Sample) error { return tc.after },
				Cleanup: func(Sample) error { cleaned = true; return nil },
			}
			b := &Bench{run: func(Step) (string, error) { return "", tc.step }}
			_, err := b.measureWith()(slotFirst, c, 0)
			if (tc.before != nil || tc.step != nil || tc.after != nil) != (err != nil) {
				t.Fatalf("got %v", err)
			}
			if cleaned != tc.cleaned {
				t.Fatalf("cleaned = %v", cleaned)
			}
		})
	}
}

func TestACleanupFailureFailsTheSample(t *testing.T) {
	c := Case{Steps: func(Sample) []Step { return nil }, Cleanup: func(Sample) error { return errors.New("left behind") }}
	b := &Bench{run: func(Step) (string, error) { return "", nil }}
	if _, err := b.measureWith()(slotFirst, c, 0); err == nil || !strings.Contains(err.Error(), "left behind") {
		t.Fatalf("got %v", err)
	}
}

// taskBench answers `new` the way the verb does: it makes the task's worktree.
func taskBench(t *testing.T, log *[][]string) *Bench {
	t.Helper()
	f := testFixtures(t)
	b := &Bench{Fixtures: f, Tools: Tools{Herdsman: "h", Wt: "w"}}
	b.run = func(s Step) (string, error) {
		*log = append(*log, s.Argv)
		if len(s.Argv) > 2 && s.Argv[1] == "new" {
			path := filepath.Join(f.Root, "made."+s.Argv[2])
			return "", f.git.script(s.Dir, []string{"worktree", "add", "-q", "-b", "feat/" + s.Argv[2], path, "main"})
		}
		return "", nil
	}
	return b
}

func verbCount(log [][]string, verb string) int {
	count := 0
	for _, argv := range log {
		if len(argv) > 1 && argv[1] == verb {
			count++
		}
	}
	return count
}

func TestFixtureMakesNewShareTheIgnoredTree(t *testing.T) {
	f := testFixtures(t)
	plan, err := wtree.Detect(f.Repo20)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Share, "node_modules") {
		t.Fatalf("got %+v", plan)
	}
	// The install the lock file implies must not reach for the network.
	if _, err := execStep(toolEnv(f), Step{Dir: f.Repo20, Argv: []string{filepath.Join(f.Bin, "bun"), "install", "--frozen-lockfile"}}); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command(filepath.Join(f.Bin, "bun"), "--version").CombinedOutput(); len(out) != 0 {
		t.Fatalf("the stand-in must do nothing: %s", out)
	}
}

func TestToolsDoNotInheritTheCheckWrappersCoreLimit(t *testing.T) {
	t.Setenv("GOMAXPROCS", "1")
	f := &Fixtures{Bin: "/bin-dir", git: fixtureGit{home: "/home-dir"}}
	for _, entry := range toolEnv(f) {
		if strings.HasPrefix(entry, "GOMAXPROCS=") {
			t.Fatalf("a limit only the Go tool obeys biases the comparison: %s", entry)
		}
	}
}

func TestBudgetCasesRunOnATaskTheVerbCreated(t *testing.T) {
	var log [][]string
	b := taskBench(t, &log)
	if slices.Contains(fixedBranches, teardownTask) {
		t.Fatal("the teardown task must not be a plain git worktree: only new records a task")
	}
	scan := findCase(t, b, CaseLedgerScan)
	for range 2 {
		if err := scan.Before(Sample{Tag: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if verbCount(log, "new") != 1 {
		t.Fatalf("the task is made once: %v", log)
	}
	step := scan.Steps(Sample{})[0]
	if len(step.Accept) != 0 || step.Must != `"lines"` {
		t.Fatalf("with nothing to stop the scan exits 0 and prints its plan: %+v", step)
	}
}

func TestTeardownRecordsItsServerUnderAFreshExecution(t *testing.T) {
	var log [][]string
	b := taskBench(t, &log)
	c := findCase(t, b, CaseTeardown)
	var seen []string
	for _, tag := range []string{"a", "b"} {
		s := Sample{Tag: tag}
		before := time.Now().Add(-time.Second)
		if err := c.Before(s); err != nil {
			t.Fatal(err)
		}
		last := log[len(log)-1]
		flags := map[string]string{}
		for i := 3; i+1 < len(last); i += 2 {
			flags[last[i]] = last[i+1]
		}
		if last[1] != "resource" || last[2] != "add" || flags["--kind"] != ledger.KindProcess || flags["--task"] != teardownTask {
			t.Fatalf("got %v", last)
		}
		if _, err := strconv.Atoi(flags["--id"]); err != nil {
			t.Fatalf("the record names the server's pid: %v", last)
		}
		minted := ledger.ExecutionTime(flags["--execution"])
		if minted.Before(before) || minted.After(time.Now()) {
			t.Fatalf("the execution must be newer than the last and older than the server: %v", last)
		}
		seen = append(seen, flags["--execution"])
		if err := c.Cleanup(s); err != nil {
			t.Fatal(err)
		}
	}
	if seen[0] == seen[1] {
		t.Fatal("each sample needs its own execution")
	}
}

func TestTeardownCleanupStopsTheServerAfterAFailedCommand(t *testing.T) {
	var log [][]string
	b := taskBench(t, &log)
	c := findCase(t, b, CaseTeardown)
	s := Sample{Tag: "x"}
	if err := c.Before(s); err != nil {
		t.Fatal(err)
	}
	if err := c.After(s); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("got %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Cleanup(s) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup did not stop the server")
	}
	if err := c.Cleanup(s); err != nil {
		t.Fatalf("a second cleanup must be harmless: %v", err)
	}
}

func TestRunCommandWritesTheFileAndFailsOnUnavailableCases(t *testing.T) {
	repo := gitRepo(t)
	tool := standInTools(t, strings.TrimSpace(pinnedWt))
	out := t.TempDir()
	var log bytes.Buffer
	args := []string{"run", "--herdsman", tool, "--wt", tool, "--repo", repo, "--out", out, "--rounds", "2", "--ignored-mb", "1", "--small", "8", "--large", "9"}
	if got := dispatch(args, &log); got != 1 {
		t.Fatalf("got %d: %s", got, log.String())
	}
	files, err := filepath.Glob(filepath.Join(out, "*.md"))
	if err != nil || len(files) != 1 {
		t.Fatalf("%v %v: %s", files, err, log.String())
	}
}

func TestRunCommandReportsAnUnusableCheckout(t *testing.T) {
	tool := standInTools(t, strings.TrimSpace(pinnedWt))
	var log bytes.Buffer
	if got := dispatch([]string{"run", "--herdsman", tool, "--wt", tool, "--repo", t.TempDir(), "--out", t.TempDir()}, &log); got != 1 {
		t.Fatalf("got %d: %s", got, log.String())
	}
}
