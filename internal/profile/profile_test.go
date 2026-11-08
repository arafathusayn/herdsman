package profile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/policy"
)

var restricted = policy.Policy{Profile: policy.ProfileRestricted}

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Exit(m.Run())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func sourceRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(tempDir(t), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first")
	return repo, git(t, repo, "rev-parse", "HEAD")
}

func TestShapeFollowsProfile(t *testing.T) {
	if got := ShapeFor(policy.Policy{}); got != ShapeLinkedWorktree {
		t.Errorf("default shape = %s", got)
	}
	if got := ShapeFor(restricted); got != ShapeOwnClone {
		t.Errorf("restricted shape = %s", got)
	}
}

func TestCheckPair(t *testing.T) {
	if err := CheckPair(restricted, "auto"); err == nil {
		t.Error("restricted accepted an automatic pair")
	}
	if err := CheckPair(restricted, PairManual); err != nil {
		t.Error(err)
	}
	if err := CheckPair(policy.Policy{}, "auto"); err != nil {
		t.Error(err)
	}
}

func TestAdmit(t *testing.T) {
	if err := Admit(restricted, "claude"); err == nil {
		t.Error("restricted admitted a harness with no folder sandbox")
	}
	if err := Admit(restricted, HarnessCodex); err != nil {
		t.Error(err)
	}
	if err := Admit(policy.Policy{}, "claude"); err != nil {
		t.Error(err)
	}
}

func TestResolveRootsImplementerGetsOnlyItsOwn(t *testing.T) {
	dir := tempDir(t)
	task, shared := filepath.Join(dir, "task"), filepath.Join(dir, "shared")
	mkdirs(t, task, shared)
	p := restricted
	p.Roots = []string{shared}
	got, err := ResolveRoots(RootRequest{Policy: p, Role: RoleImplementer, TaskRoot: task})
	if err != nil {
		t.Fatal(err)
	}
	if got.Primary != task || len(got.Extra) != 0 {
		t.Fatalf("roots = %+v", got)
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveRootsIntegratorGetsNamedRoots(t *testing.T) {
	dir := tempDir(t)
	task, a, b := filepath.Join(dir, "task"), filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mkdirs(t, task, a, b)
	if err := os.Symlink(a, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	p := restricted
	p.Roots = []string{b, filepath.Join(dir, "link"), a, task}
	got, err := ResolveRoots(RootRequest{Policy: p, Role: RoleIntegrator, TaskRoot: task})
	if err != nil {
		t.Fatal(err)
	}
	if got.Primary != task || !slices.Equal(got.Extra, []string{a, b}) {
		t.Fatalf("roots = %+v", got)
	}
}

func TestResolveRootsNoRootsGrantsNothingExtra(t *testing.T) {
	task := tempDir(t)
	p := restricted
	p.Roots, p.NoRoots = []string{tempDir(t)}, true
	got, err := ResolveRoots(RootRequest{Policy: p, Role: RoleIntegrator, TaskRoot: task})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Extra) != 0 {
		t.Fatalf("extra = %v", got.Extra)
	}
}

func TestResolveRootsRefusals(t *testing.T) {
	dir := tempDir(t)
	task, config, inside := filepath.Join(dir, "w", "task"), filepath.Join(dir, "config"), filepath.Join(dir, "config", "sub")
	mkdirs(t, task, inside)
	cases := map[string]RootRequest{
		"relative task":  {Policy: restricted, Role: RoleImplementer, TaskRoot: "task"},
		"missing task":   {Policy: restricted, Role: RoleImplementer, TaskRoot: filepath.Join(dir, "gone")},
		"relative extra": {Policy: withRoots("rel"), Role: RoleIntegrator, TaskRoot: task},
		"missing extra":  {Policy: withRoots(filepath.Join(dir, "gone")), Role: RoleIntegrator, TaskRoot: task},
		"holds config":   {Policy: withRoots(dir), Role: RoleIntegrator, TaskRoot: task, Protected: []string{config}},
		"inside config":  {Policy: withRoots(inside), Role: RoleIntegrator, TaskRoot: task, Protected: []string{config}},
		"task is config": {Policy: restricted, Role: RoleImplementer, TaskRoot: config, Protected: []string{config}},
		"bad protected":  {Policy: restricted, Role: RoleImplementer, TaskRoot: task, Protected: []string{"rel"}},
	}
	for name, req := range cases {
		if _, err := ResolveRoots(req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProtectedPathThatDoesNotExistYetStillProtects(t *testing.T) {
	dir := tempDir(t)
	task := filepath.Join(dir, "task")
	mkdirs(t, task)
	_, err := ResolveRoots(RootRequest{Policy: restricted, Role: RoleImplementer, TaskRoot: task, Protected: []string{filepath.Join(task, "future", "config.toml")}})
	if err == nil {
		t.Fatal("a root holding a not yet created protected path was accepted")
	}
}

func withRoots(r ...string) policy.Policy {
	p := restricted
	p.Roots = r
	return p
}

func TestCodexConfigListsGitFolders(t *testing.T) {
	dir := tempDir(t)
	task, extra, plain := filepath.Join(dir, "task"), filepath.Join(dir, "extra"), filepath.Join(dir, "plain")
	mkdirs(t, filepath.Join(task, ".git"), filepath.Join(extra, ".git"), plain)
	got, err := SandboxConfig(HarnessCodex, Roots{Primary: task, Extra: []string{extra, plain}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `sandbox_mode="workspace-write"`) {
		t.Errorf("config = %q", joined)
	}
	want := `sandbox_workspace_write.writable_roots=["` + filepath.Join(task, ".git") + `","` + extra + `","` + filepath.Join(extra, ".git") + `","` + plain + `"]`
	if !slices.Contains(got, want) {
		t.Errorf("config = %q, want %q", got, want)
	}
	args, err := SandboxArgs(HarnessCodex, Roots{Primary: task})
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "--cd" || args[1] != task {
		t.Errorf("args = %q", args)
	}
}

func TestSandboxRefusesOtherHarnessAndControlCharacters(t *testing.T) {
	if _, err := SandboxArgs("claude", Roots{Primary: "/a"}); err == nil {
		t.Error("args for a harness with no folder sandbox")
	}
	if _, err := SandboxConfig("claude", Roots{Primary: "/a"}); err == nil {
		t.Error("config for a harness with no folder sandbox")
	}
	if _, err := SandboxConfig(HarnessCodex, Roots{Primary: "/a", Extra: []string{"/b\n"}}); err == nil {
		t.Error("a control character reached the sandbox configuration")
	}
	if _, err := SandboxArgs(HarnessCodex, Roots{Primary: "/a\n"}); err == nil {
		t.Error("a control character reached the arguments")
	}
}

func TestCloneOwnsEveryObject(t *testing.T) {
	repo, head := sourceRepo(t)
	dest := filepath.Join(tempDir(t), "tasks", "one")
	if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest, Base: "main", Branch: "task/one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git", "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alternates: %v", err)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != head {
		t.Errorf("head = %s", got)
	}
	if got := git(t, dest, "branch", "--show-current"); got != "task/one" {
		t.Errorf("branch = %s", got)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	git(t, dest, "fsck", "--full", "--strict")
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil || len(entries) != 1 {
		t.Fatalf("left beside the clone: %v %v", entries, err)
	}
}

func TestCloneDefaultsToSourceHeadDetached(t *testing.T) {
	repo, head := sourceRepo(t)
	dest := filepath.Join(tempDir(t), "one")
	if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != head {
		t.Errorf("head = %s", got)
	}
	if got := git(t, dest, "branch", "--show-current"); got != "" {
		t.Errorf("branch = %s", got)
	}
}

func TestCloneIgnoresInheritedGitLocation(t *testing.T) {
	repo, head := sourceRepo(t)
	other, _ := sourceRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(other, ".git", "objects"))
	dest := filepath.Join(tempDir(t), "one")
	if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", "")
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_ALTERNATE_OBJECT_DIRECTORIES")
	if got := git(t, dest, "rev-parse", "HEAD"); got != head {
		t.Errorf("head = %s", got)
	}
}

func TestCloneRefusals(t *testing.T) {
	repo, _ := sourceRepo(t)
	dir := tempDir(t)
	taken := filepath.Join(dir, "taken")
	mkdirs(t, taken)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]CloneOptions{
		"relative source": {Source: "repo", Dest: filepath.Join(dir, "a")},
		"relative dest":   {Source: repo, Dest: "a"},
		"taken":           {Source: repo, Dest: taken},
		"bad branch":      {Source: repo, Dest: filepath.Join(dir, "b"), Branch: "a..b"},
		"unknown base":    {Source: repo, Dest: filepath.Join(dir, "c"), Base: "nope"},
		"not a repo":      {Source: dir, Dest: filepath.Join(dir, "d")},
		"parent is file":  {Source: repo, Dest: filepath.Join(file, "e")},
	}
	for name, opts := range cases {
		if err := Clone(context.Background(), opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name != "taken" && name != "relative source" && name != "relative dest" {
			if _, err := os.Stat(opts.Dest); err == nil {
				t.Errorf("%s: left the destination", name)
			}
		}
	}
}

func TestCloneRefusesWhenCloneStillBorrows(t *testing.T) {
	repo, _ := sourceRepo(t)
	dest := filepath.Join(tempDir(t), "one")
	old := afterClone
	t.Cleanup(func() { afterClone = old })
	afterClone = func(dir string) error {
		return os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), []byte("/x\n"), 0o644)
	}
	if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err == nil {
		t.Fatal("a clone with alternates was accepted")
	}
	afterClone = func(string) error { return errors.New("stop") }
	if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err == nil {
		t.Fatal("the hook's error was dropped")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dest: %v", err)
	}
}

// gitFailing puts a git first on PATH that refuses one subcommand.
func gitFailing(t *testing.T, sub string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = " + sub + " ]; then exit 9; fi\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCloneReportsGitFailures(t *testing.T) {
	for _, sub := range []string{"clone", "switch"} {
		t.Run(sub, func(t *testing.T) {
			repo, _ := sourceRepo(t)
			gitFailing(t, sub)
			dest := filepath.Join(tempDir(t), "one")
			if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dest: %v", err)
			}
		})
	}
}

func TestCloneReportsAnUnwritableParent(t *testing.T) {
	repo, _ := sourceRepo(t)
	locked := tempDir(t)
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	for _, dest := range []string{filepath.Join(locked, "one"), filepath.Join(locked, "sub", "one")} {
		if err := Clone(context.Background(), CloneOptions{Source: repo, Dest: dest}); err == nil {
			t.Errorf("%s: accepted", dest)
		}
	}
}

func TestProtectedPathThroughAFileIsRefused(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoots(RootRequest{Policy: restricted, Role: RoleImplementer, TaskRoot: dir, Protected: []string{filepath.Join(file, "x")}}); err == nil {
		t.Fatal("accepted")
	}
}
