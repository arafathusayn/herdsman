package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/version"
)

// manifestRow matches one module row of MODULES.md: name, path, kind, target.
var manifestRow = regexp.MustCompile("(?m)^\\| ([a-z-]+) \\| `([^`]+)` \\| ([A-Za-z]+) \\| `make (check-[a-z-]+)` \\|")

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManifestListsEveryModule(t *testing.T) {
	rows := manifestRow.FindAllStringSubmatch(readRepoFile(t, "MODULES.md"), -1)
	var paths, targets, goPaths []string
	for _, row := range rows {
		paths = append(paths, row[2])
		targets = append(targets, row[4])
		if row[3] == "Go" {
			goPaths = append(goPaths, row[2])
		}
	}
	wantPaths := []string{".", "sdk", "sdk/conformance", "sdk/ts", "web"}
	if !slices.Equal(paths, wantPaths) {
		t.Fatalf("MODULES.md paths %v, want %v", paths, wantPaths)
	}
	wantTargets := []string{"check-root", "check-sdk", "check-conformance", "check-sdk-ts", "check-web"}
	if !slices.Equal(targets, wantTargets) {
		t.Fatalf("MODULES.md targets %v, want %v", targets, wantTargets)
	}

	makefile := readRepoFile(t, "Makefile")
	workflow := readRepoFile(t, ".github/workflows/tests.yml")
	for _, target := range targets {
		if !strings.Contains(makefile, "\n"+target+":") {
			t.Errorf("Makefile has no %s target", target)
		}
		if !strings.Contains(workflow, "make "+target) {
			t.Errorf("tests.yml has no job running make %s", target)
		}
	}

	work := readRepoFile(t, "go.work")
	for _, goPath := range goPaths {
		use := "./" + goPath
		if goPath == "." {
			use = "."
		}
		if !slices.Contains(strings.Fields(work), use) {
			t.Errorf("go.work does not use %s", use)
		}
	}
}

// The root module reaches the SDK through its replace line. This import is also
// what keeps that require line when go mod tidy runs.
func TestRootResolvesSDK(t *testing.T) {
	if version.Version == "" {
		t.Fatal("sdk/version.Version is empty")
	}
}
