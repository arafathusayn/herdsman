package wtree_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
)

func forkBody(number int, ref, sha, owner string) string {
	return fmt.Sprintf(`{"number":%d,"base":{"ref":"main"},"head":{"ref":%q,"sha":%q,"repo":{"full_name":"%s/repo","clone_url":"https://example.test/%s/repo.git","owner":{"login":%q}}}}`,
		number, ref, sha, owner, owner, owner)
}

// forkCommit publishes a commit only under the pull request's ref, as a fork's
// head reaches the base repository.
func forkCommit(t *testing.T, w *world, number int) string {
	t.Helper()
	seed := filepath.Join(w.root, "seed")
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "fork work")
	sha := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "-q", "origin", fmt.Sprintf("HEAD:refs/pull/%d/head", number))
	git(t, seed, "reset", "-q", "--hard", "HEAD~1")
	return sha
}

func goTo(w *world, selector string) (daemon.WorktreeGoResponse, error) {
	return w.service.Go(context.Background(), daemon.WorktreeGoRequest{Dir: w.repo, Selector: selector})
}

func TestForkHeadNamedMainDoesNotSelectTheLocalMain(t *testing.T) {
	w := newWorld(t)
	sha := forkCommit(t, w, 9)
	mainHead := git(t, w.repo, "rev-parse", "main")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(9, "main", sha, "fork"))

	got, err := goTo(w, "pr:9")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created || got.Path == w.repo || got.Branch == "main" || got.PushRemote != "fork" {
		t.Fatalf("%+v", got)
	}
	if head := git(t, got.Path, "rev-parse", "HEAD"); head != sha {
		t.Fatalf("worktree at %s, want %s", head, sha)
	}
	if head := git(t, w.repo, "rev-parse", "main"); head != mainHead {
		t.Fatalf("the local main moved to %s", head)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "main") {
		t.Fatalf("the renamed branch was not said: %v", got.Warnings)
	}
	again, err := goTo(w, "pr:9")
	if err != nil || again.Created || again.Path != got.Path {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestForkHeadNamedLikeAnotherTaskBranchIsNotThatTask(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "mine")
	sha := forkCommit(t, w, 11)
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(11, made.Branch, sha, "fork"))
	got, err := goTo(w, "pr:11")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path == made.Path || got.Branch == made.Branch || git(t, got.Path, "rev-parse", "HEAD") != sha {
		t.Fatalf("%+v", got)
	}
}

func TestSameRepositoryPullRequestStillFindsItsBranchWorktree(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "mine")
	head := git(t, made.Path, "rev-parse", "HEAD")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, fmt.Sprintf(`{"base":{"ref":"main"},"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/repo","clone_url":"https://example.test/acme/repo.git","owner":{"login":"acme"}}}}`, made.Branch, head))
	got, err := goTo(w, "pr:12")
	if err != nil || got.Path != made.Path || got.Created {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestATaskNamedLikeTheRequestIsNotTheRequest(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "pr-13")
	sha := forkCommit(t, w, 13)
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(13, "patch", sha, "fork"))
	if got, err := goTo(w, "pr:13"); err == nil {
		t.Fatalf("task %s was taken for the request: %+v", made.Task, got)
	}
}

func TestFetchedHeadMustBeTheRequestsCommit(t *testing.T) {
	w := newWorld(t)
	forkCommit(t, w, 14)
	other := git(t, w.repo, "rev-parse", "main")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(14, "patch", other, "fork"))
	if got, err := goTo(w, "pr:14"); err == nil {
		t.Fatalf("a different commit was accepted: %+v", got)
	}
	if out := git(t, w.repo, "branch", "--list", "patch"); out != "" {
		t.Fatalf("branch left behind: %q", out)
	}
}

func TestNewFromAForkHeadNamedMainKeepsTheLocalMain(t *testing.T) {
	w := newWorld(t)
	sha := forkCommit(t, w, 15)
	mainHead := git(t, w.repo, "rev-parse", "main")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(15, "main", sha, "fork"))
	got, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "review-it", From: "pr:15"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch == "main" || git(t, got.Path, "rev-parse", "HEAD") != sha || git(t, w.repo, "rev-parse", "main") != mainHead {
		t.Fatalf("%+v", got)
	}
}

func TestExistingRemoteForAnotherRepositoryIsNotUsedForTheFork(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 10, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(10, "patch", sha, "someone"))
	unrelated := "https://example.test/unrelated/project.git"
	git(t, w.repo, "remote", "add", "someone", unrelated)

	got, err := goTo(w, "pr:10")
	if err != nil {
		t.Fatal(err)
	}
	if got.PushRemote == "someone" || got.PushRemote == "" {
		t.Fatalf("%+v", got)
	}
	if url := git(t, w.repo, "remote", "get-url", "--push", got.PushRemote); url != "https://example.test/someone/repo.git" {
		t.Fatal(url)
	}
	if url := git(t, w.repo, "remote", "get-url", "someone"); url != unrelated {
		t.Fatalf("the user's remote was rewritten: %s", url)
	}
}

func TestExistingRemoteOfTheForkIsReusedInAnyAddressForm(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 16, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(16, "patch", sha, "someone"))
	git(t, w.repo, "remote", "add", "someone", "git@example.test:Someone/repo.git")
	got, err := goTo(w, "pr:16")
	if err != nil || got.PushRemote != "someone" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestARemoteWhosePushAddressDiffersIsNotUsed(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 17, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(17, "patch", sha, "someone"))
	git(t, w.repo, "remote", "add", "someone", "https://example.test/someone/repo.git")
	git(t, w.repo, "remote", "set-url", "--push", "someone", "https://example.test/unrelated/project.git")
	got, err := goTo(w, "pr:17")
	if err != nil || got.PushRemote == "someone" || got.PushRemote == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNewFromARequestLeavesACheckedOutBranchAlone(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "mine")
	before := git(t, made.Path, "rev-parse", "HEAD")
	seed := filepath.Join(w.root, "seed")
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "ahead")
	sha := git(t, seed, "rev-parse", "HEAD")
	git(t, seed, "push", "-q", "origin", "HEAD:refs/pull/30/head")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, fmt.Sprintf(`{"base":{"ref":"main"},"head":{"ref":%q,"sha":%q,"repo":{"full_name":"acme/repo"}}}`, made.Branch, sha))
	if got, err := w.service.New(context.Background(), daemon.WorktreeNewRequest{Dir: w.repo, Task: "other", From: "pr:30"}); err == nil {
		t.Fatalf("%+v", got)
	}
	if head := git(t, made.Path, "rev-parse", "HEAD"); head != before {
		t.Fatalf("the checked-out branch moved to %s", head)
	}
	if status := git(t, made.Path, "status", "--porcelain"); status != "" {
		t.Fatalf("%q", status)
	}
}

func TestUnmarkedTaskWithoutTheRequestsCommitIsNotAdopted(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "pr-35")
	sha := forkCommit(t, w, 35)
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(35, made.Branch, sha, "fork"))
	if got, err := goTo(w, "pr:35"); err == nil {
		t.Fatalf("%+v", got)
	}
}

func TestUnmarkedTaskHoldingTheRequestsCommitIsAdopted(t *testing.T) {
	w := newWorld(t)
	made := w.create(t, "pr-36")
	sha := git(t, made.Path, "rev-parse", "HEAD")
	git(t, made.Path, "commit", "-q", "--allow-empty", "-m", "local")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(36, made.Branch, sha, "fork"))
	got, err := goTo(w, "pr:36")
	if err != nil || got.Path != made.Path || got.Created {
		t.Fatalf("%+v %v", got, err)
	}
	if mark := git(t, w.repo, "config", "branch."+made.Branch+".herdsman-pr"); mark != "acme/repo#36" {
		t.Fatal(mark)
	}
}

func TestARemoteOnAnotherPortIsNotTheFork(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 37, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(37, "patch", sha, "someone"))
	git(t, w.repo, "remote", "add", "someone", "https://example.test:8443/someone/repo.git")
	got, err := goTo(w, "pr:37")
	if err != nil || got.PushRemote == "someone" || got.PushRemote == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestADefaultPortIsTheSameRemote(t *testing.T) {
	w := newWorld(t)
	sha := pullRequest(t, w, 38, "patch")
	w.service.Repo = "acme/repo"
	w.service.GH = fakeGH(t, w, forkBody(38, "patch", sha, "someone"))
	git(t, w.repo, "remote", "add", "someone", "https://example.test:443/someone/repo.git")
	got, err := goTo(w, "pr:38")
	if err != nil || got.PushRemote != "someone" {
		t.Fatalf("%+v %v", got, err)
	}
}
