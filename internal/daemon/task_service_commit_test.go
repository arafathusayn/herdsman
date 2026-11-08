package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/gitrun"
)

// afterReset cancels the call's context as soon as git has moved the branch.
type afterReset struct{ cancel context.CancelFunc }

func (a afterReset) Trace(entry gitrun.Entry) {
	if entry.Args[0] == "reset" {
		a.cancel()
	}
}

func commitFixture(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "chore: base")
	base = run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a")
	run("commit", "-q", "-m", "chore: a")
	return dir, base, run("rev-parse", "HEAD")
}

func TestSquashPutsTheBranchBackWhenCancelledAfterTheReset(t *testing.T) {
	dir, base, head := commitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	git := &gitrun.Runner{Tracer: afterReset{cancel}}
	if _, err := record(ctx, git, dir, head, base, true, "feat: x"); err == nil {
		t.Fatal("the commit ran on a cancelled context")
	}
	if got := headCommit(context.Background(), &gitrun.Runner{}, dir); got != head {
		t.Fatalf("branch is at %s, want %s", got, head)
	}
}

func TestRestoreBranchNamesTheCommitWhenItCannotMoveBack(t *testing.T) {
	dir, _, head := commitFixture(t)
	missing := strings.Repeat("0", len(head)-1) + "1"
	err := restoreBranch(context.Background(), &gitrun.Runner{}, dir, missing)
	if err == nil || !strings.Contains(err.Error(), "git reset --soft "+missing) {
		t.Fatal(err)
	}
}

// A reset that reports success without moving HEAD must still be noticed.
func TestRestoreBranchNoticesAResetThatDidNotMove(t *testing.T) {
	dir, base, _ := commitFixture(t)
	script := filepath.Join(t.TempDir(), "git")
	body := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = reset ] && exit 0; done\nexec git \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &gitrun.Runner{Git: script}
	if err := restoreBranch(context.Background(), git, dir, base); err == nil || !strings.Contains(err.Error(), "git reset --soft "+base) {
		t.Fatal(err)
	}
}
