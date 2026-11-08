package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func wantAll(t *testing.T, file, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s lacks %q", file, w)
		}
	}
}

func TestGoreleaserSignsChecksumsAndWritesTheSBOM(t *testing.T) {
	const f = ".goreleaser.yaml"
	wantAll(t, f, repoFile(t, f),
		"signs:", "artifacts: checksum", `signature: "${artifact}.sig"`, "releasetool",
		"sboms:", "artifacts: any", "spdx.json",
	)
}

func TestGoreleaserArchivesAreReproducible(t *testing.T) {
	const f = ".goreleaser.yaml"
	text := repoFile(t, f)
	wantAll(t, f, text, "mod_timestamp:", "builds_info:", "docs/man/*.1", "manpages:")
	// Every copied file needs a fixed time, or the archive holds the checkout's.
	if n, m := strings.Count(text, "- src:"), strings.Count(text, "mtime:"); n == 0 || m != n+1 {
		t.Errorf("%d archive files and %d mtime lines: each file and the binary need one", n, m)
	}
}

func TestReleaseWorkflowComparesTwoBuildsBeforePublishing(t *testing.T) {
	const f = ".github/workflows/release.yml"
	text := repoFile(t, f)
	wantAll(t, f, text,
		"id-token: write", "attestations: write",
		"HERDSMAN_RELEASE_SIGNING_KEY: ${{ secrets.HERDSMAN_RELEASE_SIGNING_KEY }}",
		"actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8",
		"subject-checksums: dist/checksums.txt",
	)
}

func TestFuzzWorkflowIsNightly(t *testing.T) {
	const f = ".github/workflows/fuzz.yml"
	wantAll(t, f, repoFile(t, f), "schedule:", "cron:", "fuzz-nightly.sh", "testdata/fuzz")
}

var fuzzFunc = regexp.MustCompile(`(?m)^func (Fuzz\w+)\(`)

// Parsers of hostile input carry a committed corpus, so each nightly run
// starts from what the earlier ones found.
func TestHostileParsersHaveCommittedCorpora(t *testing.T) {
	for _, dir := range []string{
		"sdk/internal/encoding/toml", "sdk/internal/encoding/yaml", "sdk/internal/encoding/frontmatter",
		"sdk/internal/ws", "internal/suggest",
	} {
		tests, err := filepath.Glob(filepath.Join("../..", dir, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, file := range tests {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range fuzzFunc.FindAllSubmatch(data, -1) {
				found++
				ents, err := os.ReadDir(filepath.Join("../..", dir, "testdata/fuzz", string(m[1])))
				if err != nil || len(ents) == 0 {
					t.Errorf("%s %s has no committed corpus", dir, m[1])
				}
			}
		}
		if found == 0 {
			t.Errorf("%s has no fuzz target", dir)
		}
	}
}

func fuzzModule(t *testing.T, body string) string {
	t.Helper()
	return writeTree(t, map[string]string{
		"go.mod":      "module example.test/f\n\ngo 1.24\n",
		"p/p_test.go": body,
	})
}

func runFuzzScript(t *testing.T, module string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("../../.github/scripts/fuzz-nightly.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", script, "1s", ".")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestFuzzScript(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the fuzzer")
	}
	const head = "package p\n\nimport \"testing\"\n\n"
	out, err := runFuzzScript(t, fuzzModule(t, head+"func FuzzQuiet(f *testing.F) { f.Fuzz(func(t *testing.T, b []byte) {}) }\n"))
	if err != nil || !strings.Contains(out, "FuzzQuiet") {
		t.Fatalf("a quiet target failed: %v\n%s", err, out)
	}
	out, err = runFuzzScript(t, fuzzModule(t, head+"func FuzzBoom(f *testing.F) { f.Add([]byte{1}); f.Fuzz(func(t *testing.T, b []byte) { t.Fatal(\"boom\") }) }\n"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(out, "FuzzBoom") {
		t.Fatalf("a crashing target passed: %v\n%s", err, out)
	}
	out, err = runFuzzScript(t, writeTree(t, map[string]string{"go.mod": "module example.test/f\n\ngo 1.24\n", "p/p.go": "package p\n"}))
	if !errors.As(err, &exit) {
		t.Fatalf("a run with no target passed: %v\n%s", err, out)
	}
}
