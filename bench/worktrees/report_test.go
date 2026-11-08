package main

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeanIntervalUsesStudentT(t *testing.T) {
	got, err := meanInterval([]float64{10, 12, 14, 16, 18})
	if err != nil {
		t.Fatal(err)
	}
	// Mean 14, s = sqrt(10), t(4) = 2.776.
	half := 2.776 * math.Sqrt(10) / math.Sqrt(5)
	if math.Abs(got.Low-(14-half)) > 1e-9 || math.Abs(got.High-(14+half)) > 1e-9 {
		t.Fatalf("got %+v, want 14 +/- %f", got, half)
	}
}

func TestMeanIntervalNeedsTwoSamples(t *testing.T) {
	if _, err := meanInterval([]float64{1}); err == nil {
		t.Fatal("one sample has no interval")
	}
}

func TestStudentTNeverNarrowsTheInterval(t *testing.T) {
	if got := studentT(30); got != 2.042 {
		t.Fatalf("the table's last exact value, got %v", got)
	}
	// The default 20 rounds pool 40 samples: 39 degrees of freedom.
	for df, exact := range map[int]float64{31: 2.039513, 39: 2.022691, 40: 2.021075, 59: 2.000995, 60: 2.000298, 119: 1.980100, 120: 1.979930, 1000: 1.962339} {
		if got := studentT(df); got < exact {
			t.Errorf("t(%d) is %v, got %v", df, exact, got)
		}
	}
}

func TestJudgeVersus(t *testing.T) {
	cases := []struct {
		name     string
		h, w, aa Interval
		want     string
	}{
		{"clear win", Interval{1, 2}, Interval{5, 6}, Interval{-0.1, 0.1}, VerdictWin},
		{"overlap loses", Interval{1, 5}, Interval{4, 6}, Interval{-0.1, 0.1}, VerdictLost},
		{"slower loses", Interval{7, 8}, Interval{4, 6}, Interval{-0.1, 0.1}, VerdictLost},
		{"gap inside the noise band loses", Interval{1, 2}, Interval{2.5, 3}, Interval{-1, 1}, VerdictLost},
	}
	for _, c := range cases {
		if got := judgeVersus(c.h, c.w, c.aa); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestJudgeBudget(t *testing.T) {
	if got := judgeBudget(Interval{100, 150}, Interval{-5, 5}, 200); got != VerdictWithin {
		t.Fatalf("got %s", got)
	}
	if got := judgeBudget(Interval{100, 198}, Interval{-5, 5}, 200); got != VerdictOver {
		t.Fatalf("the noise band counts against the budget, got %s", got)
	}
}

func completeResults(commit string) Results {
	r := Results{
		Schema: SchemaVersion, Date: "2026-11-08T10:00:00Z", Commit: commit,
		Machine: "test machine", Git: "2.50", Go: "go1.27", Wt: "v0.81.0", Load: "1.0 1.0 1.0",
		Rounds: gateRounds, Small: gateSmall, Large: gateLarge, IgnoredMB: gateIgnoredMB,
	}
	for _, id := range requiredVersus {
		r.Cases = append(r.Cases, CaseResult{ID: id, Kind: KindVersus, Verdict: VerdictWin,
			Herdsman: &Interval{1, 2}, Wt: &Interval{3, 4}, AA: &Interval{-0.1, 0.1}})
	}
	for _, id := range requiredBudgets {
		r.Cases = append(r.Cases, CaseResult{ID: id, Kind: KindBudget, Verdict: VerdictWithin,
			Herdsman: &Interval{1, 2}, AA: &Interval{-0.1, 0.1}, BudgetMS: requiredBudgetMS[id]})
	}
	return r
}

func TestResultsRoundTripKeepsUnknownKeys(t *testing.T) {
	want := completeResults("abc123")
	want.Cases[0].Reason = `said "no", twice`
	text := want.Render() + ""
	text = strings.Replace(text, "machine:", "later_field: kept value\nmachine:", 1)
	got, err := ParseResults(text)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != "abc123" || len(got.Cases) != len(want.Cases) {
		t.Fatalf("got %+v", got)
	}
	if got.Cases[0].Reason != `said "no", twice` {
		t.Fatalf("reason %q", got.Cases[0].Reason)
	}
	if *got.Cases[0].Herdsman != (Interval{1, 2}) {
		t.Fatalf("interval %+v", got.Cases[0].Herdsman)
	}
	if got.Extra["later_field"] != "kept value" {
		t.Fatalf("extra %+v", got.Extra)
	}
	if !strings.Contains(got.Render(), "later_field: kept value") {
		t.Fatal("an unknown key must survive a rewrite")
	}
}

func TestUnknownCaseTokensSurviveARewrite(t *testing.T) {
	text := strings.Replace(completeResults("abc").Render(), "kind=versus", "later=1 kind=versus", 1)
	got, err := ParseResults(text)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Render(), "later=1") {
		t.Fatal("an unknown case token must survive a rewrite")
	}
}

func TestRenderShowsIntervalsOnly(t *testing.T) {
	r := completeResults("abc123")
	text := strings.ToLower(r.Render())
	for _, banned := range []string{"median", "p50", "p95", "percentile", "average"} {
		if strings.Contains(text, banned) {
			t.Fatalf("report mentions %s", banned)
		}
	}
}

func TestParseResultsRejectsMalformed(t *testing.T) {
	for name, text := range map[string]string{
		"no front matter": "hello\n",
		"unterminated":    "---\nschema: 1.0.0\n",
		"no commit":       "---\nschema: 1.0.0\ndate: x\n---\n",
		"bad interval":    "---\nschema: 1.0.0\ndate: x\ncommit: a\ncase: id=a kind=versus verdict=win herdsman=oops\n---\n",
		"bad token":       "---\nschema: 1.0.0\ndate: x\ncommit: a\ncase: id\n---\n",
		"bad budget":      "---\nschema: 1.0.0\ndate: x\ncommit: a\ncase: id=a budget=x\n---\n",
		"bad reason":      "---\nschema: 1.0.0\ndate: x\ncommit: a\ncase: id=a reason=\"x\n---\n",
		"bad line":        "---\nschema: 1.0.0\nnope\n---\n",
		"bad rounds":      "---\nschema: 1.0.0\ndate: x\ncommit: a\nrounds: x\n---\n",
	} {
		if _, err := ParseResults(text); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// gitRepo returns a repository with one commit under internal/wtree.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitFile(t, dir, "internal/wtree/a.go", "one")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	cmd.Env = cleanEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "change "+name)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

func writeResults(t *testing.T, r Results) string {
	t.Helper()
	dir := t.TempDir()
	saveResults(t, dir, "2026-11-08-v0.81.0.md", r)
	return dir
}

func saveResults(t *testing.T, dir, name string, r Results) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(r.Render()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wantCheckError(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), part) {
		t.Fatalf("got %v, want an error with %q", err, part)
	}
}

func TestCheckPassesOnCurrentResults(t *testing.T) {
	repo := gitRepo(t)
	head := gitIn(t, repo, "rev-parse", "HEAD")
	if err := Check(repo, writeResults(t, completeResults(head))); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFailsWhenCommitIsOlderThanMeasuredPackages(t *testing.T) {
	for _, pkg := range []string{"internal/wtree", "internal/cow", "internal/ledger"} {
		repo := gitRepo(t)
		old := gitIn(t, repo, "rev-parse", "HEAD")
		commitFile(t, repo, pkg+"/b.go", "two")
		wantCheckError(t, Check(repo, writeResults(t, completeResults(old))), "older")
	}
}

func TestCheckIgnoresCommitsToOtherPackages(t *testing.T) {
	repo := gitRepo(t)
	old := gitIn(t, repo, "rev-parse", "HEAD")
	commitFile(t, repo, "internal/other/b.go", "two")
	if err := Check(repo, writeResults(t, completeResults(old))); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFailsOnUnknownCommit(t *testing.T) {
	repo := gitRepo(t)
	wantCheckError(t, Check(repo, writeResults(t, completeResults(strings.Repeat("a", 40)))), "not in this history")
}

func TestCheckFailsOnCommitOffTheBranch(t *testing.T) {
	repo := gitRepo(t)
	gitIn(t, repo, "checkout", "-q", "-b", "side")
	side := commitFile(t, repo, "docs/a.md", "x")
	gitIn(t, repo, "checkout", "-q", "main")
	wantCheckError(t, Check(repo, writeResults(t, completeResults(side))), "not in this history")
}

func TestCheckFailsOnLostCase(t *testing.T) {
	repo := gitRepo(t)
	r := completeResults(gitIn(t, repo, "rev-parse", "HEAD"))
	r.Cases[0].Verdict = VerdictLost
	wantCheckError(t, Check(repo, writeResults(t, r)), "lost")
}

func TestCheckFailsOnOverBudgetAndUnavailable(t *testing.T) {
	for _, verdict := range []string{VerdictOver, VerdictUnavailable} {
		repo := gitRepo(t)
		r := completeResults(gitIn(t, repo, "rev-parse", "HEAD"))
		r.Cases[len(r.Cases)-1].Verdict = verdict
		wantCheckError(t, Check(repo, writeResults(t, r)), verdict)
	}
}

func TestCheckRejectsResultsTheEvidenceDoesNotSupport(t *testing.T) {
	nan := math.NaN()
	first := func(r *Results) *CaseResult { return &r.Cases[0] }
	last := func(r *Results) *CaseResult { return &r.Cases[len(r.Cases)-1] }
	for name, change := range map[string]func(*Results){
		"slower but labelled win":  func(r *Results) { first(r).Herdsman, first(r).Wt = &Interval{50, 60}, &Interval{1, 2} },
		"win inside the noise":     func(r *Results) { first(r).AA = &Interval{-5, 5} },
		"no herdsman interval":     func(r *Results) { first(r).Herdsman = nil },
		"no wt interval":           func(r *Results) { first(r).Wt = nil },
		"no A/A interval":          func(r *Results) { first(r).AA = nil },
		"inverted interval":        func(r *Results) { first(r).Herdsman = &Interval{2, 1} },
		"NaN interval":             func(r *Results) { first(r).Wt = &Interval{nan, nan} },
		"infinite interval":        func(r *Results) { first(r).Wt = &Interval{1, math.Inf(1)} },
		"wrong kind":               func(r *Results) { first(r).Kind = KindBudget },
		"unknown kind":             func(r *Results) { first(r).Kind = "later" },
		"budget raised":            func(r *Results) { last(r).BudgetMS = 999999 },
		"over but labelled within": func(r *Results) { last(r).Herdsman = &Interval{5000, 6000} },
		"budget without interval":  func(r *Results) { last(r).Herdsman = nil },
		"budget without A/A":       func(r *Results) { last(r).AA = nil },
		"no schema":                func(r *Results) { r.Schema = "" },
		"no machine":               func(r *Results) { r.Machine = "" },
		"no git":                   func(r *Results) { r.Git = "" },
		"no go":                    func(r *Results) { r.Go = "" },
		"no wt version":            func(r *Results) { r.Wt = "" },
		"no load":                  func(r *Results) { r.Load = "" },
		"no date":                  func(r *Results) { r.Date = "" },
		"too few rounds":           func(r *Results) { r.Rounds = 0 },
		"small fixture shrunk":     func(r *Results) { r.Small = 8 },
		"large fixture shrunk":     func(r *Results) { r.Large = 9 },
		"ignored tree shrunk":      func(r *Results) { r.IgnoredMB = 32 },
	} {
		t.Run(name, func(t *testing.T) {
			repo := gitRepo(t)
			r := completeResults(gitIn(t, repo, "rev-parse", "HEAD"))
			change(&r)
			if err := Check(repo, writeResults(t, r)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestFixtureSizesRoundTrip(t *testing.T) {
	got, err := ParseResults(completeResults("abc").Render())
	if err != nil {
		t.Fatal(err)
	}
	if got.Small != gateSmall || got.Large != gateLarge || got.IgnoredMB != gateIgnoredMB {
		t.Fatalf("got %+v", got)
	}
	bad := strings.Replace(completeResults("abc").Render(), "ignored_mb: 2048", "ignored_mb: x", 1)
	if _, err := ParseResults(bad); err == nil {
		t.Fatal("want an error for a bad size")
	}
}

func TestCheckRequiresLedgerScanAndTeardownBudgets(t *testing.T) {
	for _, id := range []string{CaseLedgerScan, CaseTeardown} {
		repo := gitRepo(t)
		r := completeResults(gitIn(t, repo, "rev-parse", "HEAD"))
		kept := r.Cases[:0]
		for _, c := range r.Cases {
			if c.ID != id {
				kept = append(kept, c)
			}
		}
		r.Cases = kept
		wantCheckError(t, Check(repo, writeResults(t, r)), id)
	}
}

func TestCheckReadsTheNewestFileByDate(t *testing.T) {
	repo := gitRepo(t)
	head := gitIn(t, repo, "rev-parse", "HEAD")
	dir := t.TempDir()
	old := completeResults(head)
	old.Date = "2026-01-01T00:00:00Z"
	old.Cases[0].Verdict = VerdictLost
	saveResults(t, dir, "2026-01-01-v0.81.0.md", old)
	saveResults(t, dir, "2026-11-08-v0.81.0.md", completeResults(head))
	if err := Check(repo, dir); err != nil {
		t.Fatal(err)
	}
}

func TestCheckFailsWithoutResults(t *testing.T) {
	wantCheckError(t, Check(gitRepo(t), t.TempDir()), "no results")
}

func TestCheckFailsOnAnyUnreadableFile(t *testing.T) {
	repo := gitRepo(t)
	dir := writeResults(t, completeResults(gitIn(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(dir, "2025-01-01-old.md"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantCheckError(t, Check(repo, dir), "2025-01-01-old.md")
}

func TestCheckPassesWhenNothingMeasuredWasCommitted(t *testing.T) {
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	head := commitFile(t, repo, "docs/a.md", "x")
	if err := Check(repo, writeResults(t, completeResults(head))); err != nil {
		t.Fatal(err)
	}
}

func TestResultsFileName(t *testing.T) {
	r := Results{Date: "2026-11-08T10:00:00Z", Wt: "v0.81.0"}
	if got := r.FileName(); got != "2026-11-08-v0.81.0.md" {
		t.Fatal(got)
	}
}

func TestCleanEnvDropsGitAndHerdrVariables(t *testing.T) {
	got := cleanEnv([]string{"GIT_DIR=/x", "HERDR_ENV=1", "HERDSMAN_ROUTE=r", "PATH=/bin"})
	if len(got) != 1 || got[0] != "PATH=/bin" {
		t.Fatalf("got %v", got)
	}
}
