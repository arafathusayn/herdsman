//go:build acceptance

// Package restricted is the real-platform gate for the restricted profile:
// real git object stores, a real snapshot restored after its source is gone,
// a real child process given a secret, and Codex's real sandbox.
package restricted

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/profile"
	"github.com/arafathusayn/herdsman/internal/secrets"
	"github.com/arafathusayn/herdsman/internal/snapshot"
)

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoWithFeature has a commit reachable only from the branch "feature".
func repoWithFeature(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(tempDir(t), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "a\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first")
	git(t, repo, "switch", "-q", "-c", "feature")
	write(t, filepath.Join(repo, "b.txt"), "only on the branch\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "second")
	git(t, repo, "switch", "-q", "main")
	return repo
}

// purge is what a routine cleanup does to the source repository.
func purge(t *testing.T, repo string) {
	t.Helper()
	git(t, repo, "branch", "-D", "feature")
	git(t, repo, "reflog", "expire", "--expire=now", "--all")
	git(t, repo, "gc", "--prune=now", "--quiet")
	git(t, repo, "maintenance", "run", "--task=gc", "--task=loose-objects", "--task=incremental-repack")
	if _, err := gitTry(repo, "cat-file", "-e", "HEAD~0:b.txt"); err == nil {
		t.Fatal("the control is void: the branch's file is still in the source")
	}
}

func TestPlainReferenceBreaksControl(t *testing.T) {
	repo := repoWithFeature(t)
	head := git(t, repo, "rev-parse", "feature")
	borrower := filepath.Join(tempDir(t), "borrower")
	git(t, filepath.Dir(borrower), "clone", "-q", "--no-local", "--reference", repo, "--no-checkout", repo, borrower)
	// --no-local stops git copying the object folder, so the clone holds only
	// the link to the source: the dependency the next gate shows being removed.
	purge(t, repo)
	if _, err := gitTry(borrower, "cat-file", "-e", head); err == nil {
		t.Fatal("a borrowing clone survived the purge, so the gate proves nothing")
	}
}

func TestTaskSurvivesSourceCleanup(t *testing.T) {
	repo := repoWithFeature(t)
	head := git(t, repo, "rev-parse", "feature")
	root := tempDir(t)
	tasks := []string{filepath.Join(root, "one"), filepath.Join(root, "two")}
	for i, dest := range tasks {
		if err := profile.Clone(context.Background(), profile.CloneOptions{Source: repo, Dest: dest, Base: "feature", Branch: "task-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	purge(t, repo)
	for _, dest := range tasks {
		git(t, dest, "fsck", "--full", "--strict")
		if got := git(t, dest, "rev-parse", "HEAD"); got != head {
			t.Fatalf("head = %s", got)
		}
		write(t, filepath.Join(dest, "c.txt"), "work\n")
		git(t, dest, "add", ".")
		git(t, dest, "commit", "-q", "-m", "task work")
		if got := git(t, dest, "show", "HEAD~1:b.txt"); got != "only on the branch" {
			t.Fatalf("file = %q", got)
		}
	}
}

func TestSnapshotRestoresWithoutTheSource(t *testing.T) {
	repo := repoWithFeature(t)
	head := git(t, repo, "rev-parse", "feature")
	root := tempDir(t)
	task := filepath.Join(root, "task")
	if err := profile.Clone(context.Background(), profile.CloneOptions{Source: repo, Dest: task, Base: "feature", Branch: "work"}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(task, "new.txt"), "uncommitted\n")
	snap := filepath.Join(root, "export", "snap")
	ctx := context.Background()
	if _, err := snapshot.Take(ctx, snapshot.Options{Worktree: task, Dest: snap, Task: "task", Op: "op1"}); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{repo, task} {
		if err := os.RemoveAll(gone); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "restored")
	if err := snapshot.Restore(ctx, snap, target); err != nil {
		t.Fatal(err)
	}
	git(t, target, "fsck", "--full", "--strict")
	if got := git(t, target, "rev-parse", "HEAD"); got != head {
		t.Fatalf("head = %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(target, "new.txt")); err != nil || string(b) != "uncommitted\n" {
		t.Fatalf("new.txt = %q, %v", b, err)
	}
}

// TestSecretNeverReachesALogOrTail runs a real child whose only source of the
// value is the keychain tool; every byte it prints goes through redaction.
func TestSecretNeverReachesALogOrTail(t *testing.T) {
	const value = "acceptance-Secret-9f3b1c"
	tools := t.TempDir()
	write(t, filepath.Join(tools, secrets.ToolName), "#!/bin/sh\nprintf '%s\\n' "+value+"\n")
	if err := os.Chmod(filepath.Join(tools, secrets.ToolName), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GATE_TOKEN", "from-the-parent-shell")

	set, err := secrets.FromEnv(func(string) string { return "GATE_TOKEN=gate/account" }, secrets.System())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-c", `
v=$GATE_TOKEN
printf 'plain %s\n' "$v"
head=$(printf %s "$v" | cut -c1-9); rest=$(printf %s "$v" | cut -c10-)
printf 'color %s\033[31m%s\033[0m\n' "$head" "$rest"
printf 'wrap %s\r\n%s\n' "$head" "$rest"
printf 'partial %s' "$head"; sleep 0.05; printf '%s end\n' "$rest"
echo "$v" >&2
`)
	child.Env = set.Environ(os.Environ())
	log := filepath.Join(t.TempDir(), "pane.log")
	f, err := os.Create(log)
	if err != nil {
		t.Fatal(err)
	}
	w := set.Redactor().Writer(f)
	child.Stdout, child.Stderr = w, w
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	flat := strings.NewReplacer("\x1b[31m", "", "\x1b[0m", "", "\r", "", "\n", "").Replace(string(b))
	if strings.Contains(flat, value) || strings.Contains(flat, value[:9]) || strings.Contains(flat, "from-the-parent-shell") {
		t.Fatalf("leaked: %q", b)
	}
	if got := strings.Count(string(b), secrets.Marker); got != 5 {
		t.Fatalf("want every print replaced, got %d in %q", got, b)
	}
	if tail := set.Redactor().String(string(b)); tail != string(b) {
		t.Fatalf("a second pass found more: %q", tail)
	}
}

// TestCodexSandboxHoldsTheRoots drives the real sandbox with the configuration
// a task receives. The two exclusions are added only because this gate's
// folders live under the temporary directory, which Codex otherwise allows.
func TestCodexSandboxHoldsTheRoots(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("skipped: codex is not on the path")
	}
	repo := repoWithFeature(t)
	root := tempDir(t)
	task, sibling, config := filepath.Join(root, "one"), filepath.Join(root, "two"), filepath.Join(root, "config")
	ctx := context.Background()
	for _, dest := range []string{task, sibling} {
		if err := profile.Clone(ctx, profile.CloneOptions{Source: repo, Dest: dest, Branch: "work"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(config, 0o755); err != nil {
		t.Fatal(err)
	}
	p := policy.Policy{Profile: policy.ProfileRestricted}
	roots, err := profile.ResolveRoots(profile.RootRequest{Policy: p, Role: profile.RoleImplementer, TaskRoot: task, Protected: []string{config, repo}})
	if err != nil {
		t.Fatal(err)
	}
	overrides, err := profile.SandboxConfig(profile.HarnessCodex, roots)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"sandbox"}, overrides...)
	args = append(args, "-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true", "-c", "sandbox_workspace_write.exclude_slash_tmp=true", "--", "/bin/sh", "-c",
		`touch inside; echo inside=$?
git -c user.name=t -c user.email=t@t commit -q --allow-empty -m s; echo commit=$?
touch "$SIBLING/x" 2>/dev/null; echo sibling=$?
touch "$CONFIG/x" 2>/dev/null; echo config=$?
touch "$SOURCE/x" 2>/dev/null; echo source=$?`)
	cmd := exec.Command("codex", args...)
	cmd.Dir = roots.Primary
	cmd.Env = append(os.Environ(), "SIBLING="+sibling, "CONFIG="+config, "SOURCE="+repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex sandbox: %v\n%s", err, out)
	}
	for _, want := range []string{"inside=0", "commit=0", "sibling=1", "config=1", "source=1"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("want %s in:\n%s", want, out)
		}
	}
	for _, denied := range []string{filepath.Join(sibling, "x"), filepath.Join(config, "x"), filepath.Join(repo, "x")} {
		if _, err := os.Stat(denied); err == nil {
			t.Errorf("%s was written", denied)
		}
	}
}
