package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitRepo is a repository whose commits need no user configuration and
// whose PATH holds git and nothing a generator could be found in.
func commitRepo(t *testing.T) string {
	t.Helper()
	repo := worktreeRepo(t)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return repo
}

func configure(t *testing.T, repo, content string) {
	t.Helper()
	write(t, filepath.Join(filepath.Dir(repo), "herdsman.config.json"), content)
}

func generator(t *testing.T, repo, body string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "gen")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(script)
	configure(t, repo, `{"commit":{"generation":{"command":`+string(encoded)+`,"template_append":"Say hello."}}}`)
}

func taskTree(t *testing.T, repo string) string {
	t.Helper()
	if out, errOut, code := call(t, "new", "one", "--no-setup"); code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	tree := filepath.Join(filepath.Dir(repo), "repo.worktrees", "one")
	t.Chdir(tree)
	return tree
}

func subject(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "log", "-1", "--format=%s"))
}

func TestCommitWithNoHarnessUsesAValidFallbackAndSaysSo(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	out, errOut, code := call(t, "commit")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	if got := subject(t, tree); got != "feat(one): update a.txt" {
		t.Fatal(got)
	}
	if !strings.Contains(errOut, "no commit message generator") {
		t.Fatal(errOut)
	}
	if !strings.Contains(out, "feat(one): update a.txt") {
		t.Fatal(out)
	}
}

func TestCommitUsesTheGeneratorsMessageAndPrompt(t *testing.T) {
	repo := commitRepo(t)
	seen := filepath.Join(t.TempDir(), "seen")
	generator(t, repo, "cat > "+seen+"\necho 'fix: repair the thing'\necho\necho 'because it broke'\n")
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "hello content\n")
	write(t, filepath.Join(tree, "bun.lock"), "LOCKFILE BODY\n")
	if out, errOut, code := call(t, "commit", "--scope", "api"); code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	if got := gitOut(t, tree, "log", "-1", "--format=%B"); got != "fix(api): repair the thing\n\nbecause it broke\n\n" {
		t.Fatalf("%q", got)
	}
	data, _ := os.ReadFile(seen)
	prompt := string(data)
	for _, want := range []string{"hello content", "Say hello.", "feat/one", "first", "a.txt"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("no %q in\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "LOCKFILE BODY") {
		t.Error("the lock file reached the prompt")
	}
}

func TestCommitRefusesAnAnswerThatNeverParses(t *testing.T) {
	repo := commitRepo(t)
	generator(t, repo, "cat > /dev/null\necho 'garbage'\n")
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	before := gitOut(t, tree, "rev-parse", "HEAD")
	_, errOut, code := call(t, "commit")
	if code != 1 || !strings.Contains(errOut, "pass -m") {
		t.Fatalf("%d %s", code, errOut)
	}
	if gitOut(t, tree, "rev-parse", "HEAD") != before {
		t.Fatal("a commit was made")
	}
}

func TestCommitFallsBackWhenTheGeneratorFails(t *testing.T) {
	repo := commitRepo(t)
	generator(t, repo, "exit 9\n")
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	_, errOut, code := call(t, "commit", "--type", "docs")
	if code != 0 || !strings.Contains(errOut, "the generator failed") {
		t.Fatalf("%d %s", code, errOut)
	}
	if got := subject(t, tree); got != "docs(one): update a.txt" {
		t.Fatal(got)
	}
}

func TestCommitGenerationOff(t *testing.T) {
	repo := commitRepo(t)
	configure(t, repo, `{"commit":{"generation":{"enabled":false}}}`)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	_, errOut, code := call(t, "commit")
	if code != 0 || !strings.Contains(errOut, "generation is off") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestCommitMessageFlag(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	if _, errOut, code := call(t, "commit", "-m", "Not conventional"); code != 1 || !strings.Contains(errOut, "not a conventional") {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, errOut, code := call(t, "commit", "-m", "feat: x", "--type", "fix"); code != 1 || !strings.Contains(errOut, "drop --type") {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, errOut, code := call(t, "commit", "--type", "nope"); code != 1 || !strings.Contains(errOut, "not one of") {
		t.Fatalf("%d %s", code, errOut)
	}
	out, errOut, code := call(t, "commit", "-m", "feat: given", "--json")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	if !strings.Contains(out, `"source":"given"`) || subject(t, tree) != "feat: given" {
		t.Fatal(out)
	}
	if _, errOut, code := call(t, "commit", "-m", "feat: again"); code != 1 || !strings.Contains(errOut, "nothing to commit") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestCommitStagePolicies(t *testing.T) {
	repo := commitRepo(t)
	configure(t, repo, `{"commit":{"stage":"tracked"}}`)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "kept.txt"), "1\n")
	gitOut(t, tree, "add", "kept.txt")
	gitOut(t, tree, "commit", "-q", "-m", "chore: seed")
	write(t, filepath.Join(tree, "kept.txt"), "2\n")
	write(t, filepath.Join(tree, "new.txt"), "n\n")
	if _, errOut, code := call(t, "commit", "-m", "fix: tracked only"); code != 0 {
		t.Fatal(errOut)
	}
	if got := strings.TrimSpace(gitOut(t, tree, "show", "--name-only", "--format=", "HEAD")); got != "kept.txt" {
		t.Fatal(got)
	}

	configure(t, repo, `{"commit":{"stage":"none"}}`)
	if _, errOut, code := call(t, "commit", "-m", "fix: none"); code != 1 || !strings.Contains(errOut, "nothing to commit") {
		t.Fatalf("%d %s", code, errOut)
	}
	configure(t, repo, `{"commit":{"stage":"sometimes"}}`)
	if _, errOut, code := call(t, "commit", "-m", "fix: bad"); code != 1 || !strings.Contains(errOut, "commit.stage") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestCommitDryRunChangesNothing(t *testing.T) {
	repo := commitRepo(t)
	generator(t, repo, "cat > /dev/null\necho 'feat: preview'\n")
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a.txt"), "a\n")
	before := gitOut(t, tree, "status", "--porcelain=v1") + gitOut(t, tree, "rev-parse", "HEAD")
	out, errOut, code := call(t, "commit", "--dry-run")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	for _, want := range []string{"--- prompt", "--- command", "--- message (generated)", "feat: preview"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %s", want, out)
		}
	}
	if after := gitOut(t, tree, "status", "--porcelain=v1") + gitOut(t, tree, "rev-parse", "HEAD"); after != before {
		t.Fatalf("%q != %q", after, before)
	}
}

func TestCommitSquashFoldsTheBranch(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	for _, name := range []string{"a", "b"} {
		write(t, filepath.Join(tree, name), name+"\n")
		gitOut(t, tree, "add", name)
		gitOut(t, tree, "commit", "-q", "-m", "chore: "+name)
	}
	write(t, filepath.Join(tree, "c"), "c\n")
	if _, errOut, code := call(t, "commit", "--squash", "main", "-m", "feat: all of it"); code != 0 {
		t.Fatal(errOut)
	}
	if got := strings.TrimSpace(gitOut(t, tree, "rev-list", "--count", "main..HEAD")); got != "1" {
		t.Fatal(got)
	}
	if got := strings.Fields(gitOut(t, tree, "show", "--name-only", "--format=", "HEAD")); strings.Join(got, " ") != "a b c" {
		t.Fatal(got)
	}
}

func TestCommitSquashRestoresTheBranchWhenGitRefuses(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a"), "a\n")
	gitOut(t, tree, "add", "a")
	gitOut(t, tree, "commit", "-q", "-m", "chore: a")
	head := gitOut(t, tree, "rev-parse", "HEAD")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "b"), "b\n")
	if _, _, code := call(t, "commit", "--squash", "main", "-m", "feat: x"); code != 1 {
		t.Fatal(code)
	}
	if got := gitOut(t, tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("%s != %s", got, head)
	}
}

func TestCommitSquashRefusals(t *testing.T) {
	repo := commitRepo(t)
	write(t, filepath.Join(repo, "a"), "a\n")
	if _, errOut, code := call(t, "commit", "--squash", "main", "-m", "feat: x"); code != 1 || !strings.Contains(errOut, "default branch") {
		t.Fatalf("%d %s", code, errOut)
	}
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a"), "a\n")
	if _, errOut, code := call(t, "commit", "--squash", "--bad", "-m", "feat: x"); code != 1 || !strings.Contains(errOut, "not a commit") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestCommitHooksRunAroundTheCommit(t *testing.T) {
	repo := commitRepo(t)
	configure(t, repo, `{"hooks":{"pre-commit":"touch from-hook"}}`)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a"), "a\n")
	if _, errOut, code := call(t, "commit", "-m", "feat: x"); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, errOut, code := call(t, "hook", "pre-commit", "--approve"); code != 0 {
		t.Fatal(errOut)
	}
	if _, errOut, code := call(t, "commit", "-m", "feat: x"); code != 0 {
		t.Fatal(errOut)
	}
	if got := gitOut(t, tree, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(got, "from-hook") {
		t.Fatal(got)
	}
}

func TestCommitRefusesAnUnapprovedPostHookBeforeCommitting(t *testing.T) {
	repo := commitRepo(t)
	configure(t, repo, `{"hooks":{"post-commit":"true"}}`)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "a"), "a\n")
	before := gitOut(t, tree, "rev-parse", "HEAD")
	if _, errOut, code := call(t, "commit", "-m", "feat: x"); code != 1 || !strings.Contains(errOut, "not approved") {
		t.Fatalf("%d %s", code, errOut)
	}
	if gitOut(t, tree, "rev-parse", "HEAD") != before {
		t.Fatal("committed before refusing")
	}
}

func TestCommitRejectsArgumentsAndBadFlags(t *testing.T) {
	commitRepo(t)
	if _, _, code := call(t, "commit", "extra"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "commit", "--nope"); code != 2 {
		t.Fatal(code)
	}
}

// dirtyTask leaves one commit on the branch, a staged, an unstaged and an
// untracked change, and non-UTF-8 bytes so that any re-encoding would show.
func dirtyTask(t *testing.T, repo string) string {
	t.Helper()
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "committed.txt"), "c\n")
	write(t, filepath.Join(tree, "unstaged.txt"), "u\xff\xfe\n")
	gitOut(t, tree, "add", "-A")
	gitOut(t, tree, "commit", "-q", "-m", "chore: base work")
	write(t, filepath.Join(tree, "staged.txt"), "s\n")
	gitOut(t, tree, "add", "staged.txt")
	write(t, filepath.Join(tree, "unstaged.txt"), "changed\xff\n")
	write(t, filepath.Join(tree, "untracked.txt"), "t\n")
	return tree
}

func TestDiffCommittedIsByteEqualToGit(t *testing.T) {
	repo := commitRepo(t)
	tree := dirtyTask(t, repo)
	out, errOut, code := call(t, "diff", "--committed")
	if code != 0 {
		t.Fatal(errOut)
	}
	if want := gitOut(t, tree, "diff", "main...HEAD"); out != want || want == "" {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestDiffWorkingAndDefaultAreByteEqualToGit(t *testing.T) {
	repo := commitRepo(t)
	tree := dirtyTask(t, repo)
	gotWorking, errOut, code := call(t, "diff", "--working")
	if code != 0 {
		t.Fatal(errOut)
	}
	gotAll, errOut, code := call(t, "diff")
	if code != 0 {
		t.Fatal(errOut)
	}
	if strings.Contains(gitOut(t, tree, "status", "--porcelain=v1"), "A  untracked") {
		t.Fatal("reading the diff staged a file")
	}
	gitOut(t, tree, "add", "-A")
	if want := gitOut(t, tree, "diff", "--cached", "HEAD"); gotWorking != want || want == "" {
		t.Fatalf("%q != %q", gotWorking, want)
	}
	if want := gitOut(t, tree, "diff", "--cached", "main"); gotAll != want || !strings.Contains(want, "committed.txt") {
		t.Fatalf("%q != %q", gotAll, want)
	}
}

func TestDiffByTaskNameWithArgsFromAnotherWorktree(t *testing.T) {
	repo := commitRepo(t)
	tree := dirtyTask(t, repo)
	t.Chdir(repo)
	out, errOut, code := call(t, "diff", "one", "--committed", "--", "--stat", "--", "committed.txt")
	if code != 0 {
		t.Fatal(errOut)
	}
	if want := gitOut(t, tree, "diff", "main...HEAD", "--stat", "--", "committed.txt"); out != want || strings.Contains(out, "unstaged") {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestDiffRemoteComparesWithTheUpstream(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	if _, errOut, code := call(t, "diff", "--remote"); code != 1 || !strings.Contains(errOut, "no upstream") {
		t.Fatalf("%d %s", code, errOut)
	}
	gitOut(t, tree, "update-ref", "refs/remotes/origin/feat/one", "HEAD")
	gitOut(t, tree, "config", "branch.feat/one.remote", "origin")
	gitOut(t, tree, "config", "branch.feat/one.merge", "refs/heads/feat/one")
	gitOut(t, tree, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	write(t, filepath.Join(tree, "ahead"), "x\n")
	gitOut(t, tree, "add", "ahead")
	gitOut(t, tree, "commit", "-q", "-m", "chore: ahead")
	out, errOut, code := call(t, "diff", "--remote")
	if code != 0 {
		t.Fatal(errOut)
	}
	if want := gitOut(t, tree, "diff", "@{upstream}...HEAD"); out != want || !strings.Contains(out, "ahead") {
		t.Fatalf("%q != %q", out, want)
	}
}

func TestDiffPrefersTheRemoteCopyOfTheDefaultBranch(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	write(t, filepath.Join(tree, "mine"), "m\n")
	gitOut(t, tree, "add", "mine")
	gitOut(t, tree, "commit", "-q", "-m", "chore: mine")
	gitOut(t, repo, "update-ref", "refs/remotes/origin/main", gitRevision(t, tree, "HEAD~1"))
	gitOut(t, repo, "update-ref", "refs/heads/main", gitRevision(t, tree, "HEAD"))
	out, errOut, code := call(t, "diff", "--committed")
	if code != 0 || !strings.Contains(out, "mine") {
		t.Fatalf("%q %s", out, errOut)
	}
}

func gitRevision(t *testing.T, dir, name string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "rev-parse", name))
}

func TestDiffExitCodeJSONAndUsage(t *testing.T) {
	repo := commitRepo(t)
	dirtyTask(t, repo)
	if _, _, code := call(t, "diff", "--committed", "--", "--exit-code"); code != 1 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "diff", "--working", "--committed"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "diff", "a", "b"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "diff", "--nope"); code != 2 {
		t.Fatal(code)
	}
	if _, errOut, code := call(t, "diff", "missing"); code != 1 || errOut == "" {
		t.Fatalf("%d %s", code, errOut)
	}
	out, _, code := call(t, "diff", "--json")
	var result struct {
		Output []byte `json:"output"`
		Base   string `json:"base"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || code != 0 || len(result.Output) == 0 || result.Base == "" {
		t.Fatalf("%d %v %s", code, err, out)
	}
}

func TestDiffBeforeTheFirstCommit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "work", "empty")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "init", "-q", "-b", "main")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Chdir(repo)
	write(t, filepath.Join(repo, "a"), "a\n")
	out, errOut, code := call(t, "diff")
	if code != 0 || !strings.Contains(out, "+a") {
		t.Fatalf("%d %q %s", code, out, errOut)
	}
	if _, errOut, code := call(t, "diff", "--committed"); code != 1 || !strings.Contains(errOut, "no commits") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestEachRunsEverywhereAndContinuesPastFailures(t *testing.T) {
	repo := commitRepo(t)
	tree := taskTree(t, repo)
	script := `echo "$HERDSMAN_TASK|$HERDSMAN_BRANCH|$HERDSMAN_WORKTREE_PATH|$PWD|$GIT_DIR"; test "$HERDSMAN_BRANCH" != main`
	out, errOut, code := call(t, "each", "--", "sh", "-c", script)
	if code != 1 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if want := "feat/one|" + tree + "|" + tree + "|\n"; !strings.Contains(out, want) {
		t.Fatalf("no %q in %q", want, out)
	}
	if !strings.Contains(out, "main|"+repo+"|"+repo+"|\n") {
		t.Fatal(out)
	}
	if !strings.Contains(errOut, "2 worktrees: 1 ok, 1 failed") || !strings.Contains(errOut, "==> one") {
		t.Fatal(errOut)
	}
}

func TestEachClearsGitRedirection(t *testing.T) {
	repo := commitRepo(t)
	t.Setenv("GIT_DIR", "/elsewhere")
	entries := eachEnvironment(listRow{Task: "t", Branch: "b", Path: repo})
	for _, entry := range entries {
		if strings.HasPrefix(entry, "GIT_DIR=") {
			t.Fatal(entry)
		}
	}
}

func TestEachJSONAndAllGreen(t *testing.T) {
	repo := commitRepo(t)
	taskTree(t, repo)
	out, errOut, code := call(t, "each", "--json", "--", "true")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	var result struct {
		Results []eachResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Results) != 2 {
		t.Fatalf("%v %s", err, out)
	}
	if !strings.Contains(errOut, "2 worktrees: 2 ok, 0 failed") {
		t.Fatal(errOut)
	}
}

func TestEachReportsACommandThatCannotStart(t *testing.T) {
	commitRepo(t)
	out, errOut, code := call(t, "each", "--json", "--", "/no/such/program")
	if code != 1 || !strings.Contains(out, `"exit":-1`) {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

func TestEachUsage(t *testing.T) {
	commitRepo(t)
	if _, _, code := call(t, "each"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "each", "ls"); code != 2 {
		t.Fatal(code)
	}
	if _, _, code := call(t, "each", "--nope"); code != 2 {
		t.Fatal(code)
	}
}
