package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeGoreleaser = `#!/bin/bash
mkdir -p dist/homebrew/Casks
n=$(cat "$FAKE/count" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$FAKE/count"
if [ "${DIFFER:-}" = 1 ]; then echo "$n" > dist/a.tar.gz; else echo same > dist/a.tar.gz; fi
(cd dist && shasum -a 256 a.tar.gz > checksums.txt)
echo sig > dist/checksums.txt.sig
echo cask > dist/homebrew/Casks/herdsman.rb
echo "goreleaser $GOCACHE $*" >> "$FAKE/log"
`

const fakeLogger = `#!/bin/bash
echo "$(basename "$0") $*" >> "$FAKE/log"
`

// releaseBench is a checkout with fake goreleaser, gh and git on PATH.
type releaseBench struct {
	dir, fake, state string
	env              []string
}

func newReleaseBench(t *testing.T, extra ...string) *releaseBench {
	t.Helper()
	b := &releaseBench{dir: t.TempDir(), fake: t.TempDir(), state: t.TempDir()}
	for name, body := range map[string]string{"goreleaser": fakeGoreleaser, "gh": fakeLogger, "git": fakeLogger} {
		if err := os.WriteFile(filepath.Join(b.fake, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.env = append(os.Environ(),
		"FAKE="+b.fake, "RELEASE_STATE="+b.state,
		"GORELEASER="+filepath.Join(b.fake, "goreleaser"),
		"PATH="+b.fake+":"+os.Getenv("PATH"),
		"HOMEBREW_TAP_SSH_KEY=tap-secret",
	)
	b.env = append(b.env, extra...)
	return b
}

func (b *releaseBench) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("../../.github/scripts/release-tag.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", append([]string{script}, args...)...)
	cmd.Dir = b.dir
	cmd.Env = b.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (b *releaseBench) log(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.fake, "log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishSendsTheComparedBytesWithoutRebuilding(t *testing.T) {
	b := newReleaseBench(t)
	if out, err := b.run(t, "build"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := b.run(t, "publish", "v1.2.3-rc.1"); err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}
	log := b.log(t)
	if n := strings.Count(log, "goreleaser "); n != 2 {
		t.Fatalf("%d builds, want 2:\n%s", n, log)
	}
	wantAll(t, "log", log,
		"--skip=publish,sign,homebrew", "cache-first", "cache-second",
		"gh release create v1.2.3-rc.1", "dist/a.tar.gz", "dist/checksums.txt dist/checksums.txt.sig", "--verify-tag", "--prerelease",
		" push origin main",
	)
	if strings.Index(log, "gh release") > strings.Index(log, " push origin main") {
		t.Fatalf("the cask went out before the release:\n%s", log)
	}
}

func TestPublishFinalReleaseIsNotAPrerelease(t *testing.T) {
	b := newReleaseBench(t)
	b.run(t, "build")
	if out, err := b.run(t, "publish", "v1.2.3"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(b.log(t), "--prerelease") {
		t.Fatal("a final release was marked as a prerelease")
	}
}

func TestDifferingBuildsNeverReachPublishing(t *testing.T) {
	b := newReleaseBench(t, "DIFFER=1")
	if out, err := b.run(t, "build"); err == nil {
		t.Fatalf("two different builds passed:\n%s", out)
	}
	if _, err := b.run(t, "publish", "v1.2.3"); err == nil {
		t.Fatal("publish ran after a failed comparison")
	}
	if log := b.log(t); strings.Contains(log, "gh ") || strings.Contains(log, "git ") {
		t.Fatalf("something was published:\n%s", log)
	}
}

func TestPublishRefusesFilesChangedAfterTheComparison(t *testing.T) {
	for name, change := range map[string]func(dir string) error{
		"archive":      func(d string) error { return os.WriteFile(filepath.Join(d, "dist/a.tar.gz"), []byte("evil"), 0o644) },
		"list":         func(d string) error { return os.WriteFile(filepath.Join(d, "dist/checksums.txt"), []byte("x"), 0o644) },
		"no signature": func(d string) error { return os.Remove(filepath.Join(d, "dist/checksums.txt.sig")) },
		"no cask":      func(d string) error { return os.Remove(filepath.Join(d, "dist/homebrew/Casks/herdsman.rb")) },
	} {
		t.Run(name, func(t *testing.T) {
			b := newReleaseBench(t)
			if out, err := b.run(t, "build"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if err := change(b.dir); err != nil {
				t.Fatal(err)
			}
			if out, err := b.run(t, "publish", "v1.2.3"); err == nil {
				t.Fatalf("published:\n%s", out)
			}
			if strings.Contains(b.log(t), "gh release") {
				t.Fatal("the release was created")
			}
		})
	}
}

func TestReleaseScriptUsageAndSecrets(t *testing.T) {
	b := newReleaseBench(t)
	if _, err := b.run(t); err == nil {
		t.Error("no arguments were accepted")
	}
	b.run(t, "build")
	if _, err := b.run(t, "publish"); err == nil {
		t.Error("publish without a tag was accepted")
	}
	out, err := b.run(t, "publish", "v1.2.3")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "tap-secret") || strings.Contains(b.log(t), "tap-secret") {
		t.Fatal("the tap key was printed")
	}
}

func TestReleaseWorkflowOrder(t *testing.T) {
	const f = ".github/workflows/release.yml"
	text := repoFile(t, f)
	build, attest, publish := strings.Index(text, "release-tag.sh build"), strings.Index(text, "attest-build-provenance"), strings.Index(text, "release-tag.sh publish")
	if build < 0 || attest < build || publish < attest {
		t.Fatalf("want build, attest, publish; at %d, %d, %d", build, attest, publish)
	}
	if strings.Contains(text, "goreleaser release") {
		t.Error("the workflow still rebuilds outside the compared build")
	}
}

func TestPullRequestBuildComparesAllTargets(t *testing.T) {
	const f = ".github/workflows/release-build.yml"
	wantAll(t, f, repoFile(t, f), "release-tag.sh build --snapshot", "GORELEASER: .tools/bin/goreleaser")
}

func sbomBlock(t *testing.T) string {
	t.Helper()
	text := repoFile(t, ".goreleaser.yaml")
	start, end := strings.Index(text, "sboms:"), strings.Index(text, "signs:")
	if start < 0 || end < start {
		t.Fatal("no sboms block")
	}
	return text[start:end]
}

// GoReleaser gives an SBOM command a scrubbed environment, so the repository
// mode has to be stated there.
func TestSBOMCommandRunsOffline(t *testing.T) {
	wantAll(t, "sboms", sbomBlock(t), "env:", "GOFLAGS=-mod=vendor", "GOPROXY=off", "GOCACHE=", "--zig-version")
}

func TestSBOMZigVersionMatchesTheBuild(t *testing.T) {
	text := repoFile(t, ".github/workflows/release.yml")
	i := strings.Index(text, "setup-zig")
	rest := text[i:]
	const key = "version: "
	j := strings.Index(rest, key)
	version := strings.Fields(rest[j+len(key):])[0]
	if !strings.Contains(sbomBlock(t), "- "+version+"\n") {
		t.Fatalf("the SBOM does not name zig %s", version)
	}
}

func TestStaticLinkComponents(t *testing.T) {
	got := StaticLinkComponents("0.17.0")
	want := map[string]string{"musl": "MIT", "zig-compiler-rt": "MIT"}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for _, c := range got {
		if want[c.Name] != c.License || !strings.Contains(c.Version, "0.17.0") || c.Upstream == "" {
			t.Errorf("%+v", c)
		}
	}
}
