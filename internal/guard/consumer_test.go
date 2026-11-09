package guard

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const consumerScript = ".github/scripts/sdk-consumer.sh"

const fakeCommit = "0123456789012345678901234567890123456789"

// consumerEnv is the environment without GOFLAGS or GOPROXY, whatever mode the
// tests themselves run in, plus the given overrides.
func consumerEnv(overrides ...string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GOFLAGS=") && !strings.HasPrefix(entry, "GOPROXY=") {
			env = append(env, entry)
		}
	}
	return append(env, overrides...)
}

func runConsumerScript(t *testing.T, commit string, env []string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(repoRoot, filepath.FromSlash(consumerScript)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, commit)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestConsumerScriptFailsWhenGOFLAGSReachesIt(t *testing.T) {
	out, err := runConsumerScript(t, fakeCommit, consumerEnv("GOFLAGS=-mod=vendor"))
	if err == nil {
		t.Fatalf("script passed with GOFLAGS set:\n%s", out)
	}
	if !strings.Contains(out, "GOFLAGS must be empty") {
		t.Fatalf("script failed for another reason:\n%s", out)
	}
}

func TestConsumerScriptRejectsAShortCommit(t *testing.T) {
	out, err := runConsumerScript(t, "abc123", consumerEnv())
	if err == nil {
		t.Fatalf("script passed with a short commit id:\n%s", out)
	}
	if !strings.Contains(out, "full commit id") {
		t.Fatalf("script failed for another reason:\n%s", out)
	}
}

// git runs one git command in dir with the test's own global config.
func git(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// consumerRepo commits a copy of the SDK to a local repository and returns the
// commit and an environment in which the module's GitHub URL resolves to it, so
// the external-consumer build runs with no network.
func consumerRepo(t *testing.T, sdkVersionSource string, clientSource ...string) (string, []string) {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "herdsman")
	writeFile(t, filepath.Join(repo, "sdk", "go.mod"), readRepoFile(t, "sdk/go.mod"))
	writeFile(t, filepath.Join(repo, "sdk", "version", "version.go"), sdkVersionSource)
	// A stand-in that imports nothing, so that the build needs no network.
	stub := "package client\n\ntype Options struct{}\n"
	if len(clientSource) > 0 {
		stub = clientSource[0]
	}
	writeFile(t, filepath.Join(repo, "sdk", "client", "client.go"), stub)

	gitConfig := filepath.Join(base, "gitconfig")
	writeFile(t, gitConfig, "[url \"file://"+repo+"\"]\n\tinsteadOf = https://github.com/arafathusayn/herdsman\n[user]\n\tname = fixture\n\temail = fixture@example.com\n")
	modCache := filepath.Join(base, "modcache")
	t.Cleanup(func() { makeWritable(t, modCache) })
	env := consumerEnv(
		"GIT_CONFIG_GLOBAL="+gitConfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ALLOW_PROTOCOL=file",
		"GOMODCACHE="+modCache,
		"GOTOOLCHAIN=local",
		"TMPDIR="+base,
	)
	git(t, repo, env, "init", "--quiet")
	git(t, repo, env, "add", ".")
	git(t, repo, env, "commit", "--quiet", "-m", "sdk")
	return git(t, repo, env, "rev-parse", "HEAD"), env
}

// makeWritable lets the temporary directory's cleanup remove a module cache,
// whose files Go creates read-only.
func makeWritable(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return os.Chmod(p, 0o755)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestConsumerScriptBuildsAgainstTheCommit(t *testing.T) {
	commit, env := consumerRepo(t, readRepoFile(t, "sdk/version/version.go"))
	out, err := runConsumerScript(t, commit, env)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sdk consumer ok at "+commit) {
		t.Fatalf("script did not confirm the commit:\n%s", out)
	}
}

func TestConsumerScriptFailsOnAnEmptyVersion(t *testing.T) {
	commit, env := consumerRepo(t, "package version\n\nconst Version = \"\"\n")
	out, err := runConsumerScript(t, commit, env)
	if err == nil {
		t.Fatalf("script passed with an empty version:\n%s", out)
	}
	if !strings.Contains(out, "printed no version") {
		t.Fatalf("script failed for another reason:\n%s", out)
	}
}

// A go wrapper reports another commit for the download, as a resolver that
// landed on the wrong revision would.
func TestConsumerScriptFailsWhenTheResolvedCommitDiffers(t *testing.T) {
	commit, env := consumerRepo(t, readRepoFile(t, "sdk/version/version.go"))
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "go"), "#!/bin/sh\nif [ \"$1\" = mod ] && [ \"$2\" = download ]; then\n  echo '{\"Origin\": {\"Hash\": \"other\"}}'\n  exit 0\nfi\nexec \""+realGo+"\" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runConsumerScript(t, commit, append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")))
	if err == nil {
		t.Fatalf("script passed with a different commit:\n%s", out)
	}
	if !strings.Contains(out, "is not commit") {
		t.Fatalf("script failed for another reason:\n%s", out)
	}
}

func TestConsumerScriptFailsWhenTheSDKDoesNotCompile(t *testing.T) {
	commit, env := consumerRepo(t, "package version\n\nconst Other = \"x\"\n")
	out, err := runConsumerScript(t, commit, env)
	if err == nil {
		t.Fatalf("script passed against an SDK without Version:\n%s", out)
	}
}

// The consumer has to build the client, not only the version constant.
func TestConsumerScriptFailsWhenTheClientCannotBuild(t *testing.T) {
	commit, env := consumerRepo(t, readRepoFile(t, "sdk/version/version.go"), "package client\n\nvar broken = missing\n")
	out, err := runConsumerScript(t, commit, env)
	if err == nil {
		t.Fatalf("script passed with a broken client:\n%s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Fatalf("script failed for another reason:\n%s", out)
	}
}
