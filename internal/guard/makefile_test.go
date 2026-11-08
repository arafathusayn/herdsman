package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runMakeTarget runs one target of the repository's Makefile in a throwaway
// tree holding only the Makefile, the manifest and the given files.
func runMakeTarget(t *testing.T, target string, files map[string]string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Makefile"), readRepoFile(t, "Makefile"))
	writeFile(t, filepath.Join(dir, "MODULES.md"), readRepoFile(t, "MODULES.md"))
	for name, content := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	}
	cmd := exec.Command("make", "--no-print-directory", target)
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MAKEFLAGS=") && !strings.HasPrefix(entry, "MFLAGS=") && !strings.HasPrefix(entry, "MAKELEVEL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const failingBunTest = "import { expect, test } from \"bun:test\";\n\ntest(\"planted\", () => {\n  expect(1).toBe(2);\n});\n"

// Every file name bun test discovers must reach bun, or a failing suite passes.
func TestCheckSDKTSRunsEveryBunTestName(t *testing.T) {
	for _, name := range []string{"a.test.ts", "a_test.ts", "a.spec.ts", "a_spec.ts"} {
		t.Run(name, func(t *testing.T) {
			out, err := runMakeTarget(t, "check-sdk-ts", map[string]string{"sdk/ts/" + name: failingBunTest})
			if err == nil {
				t.Fatalf("check-sdk-ts passed with a failing %s:\n%s", name, out)
			}
			if strings.Contains(out, "no test files yet") {
				t.Fatalf("check-sdk-ts did not see %s:\n%s", name, out)
			}
		})
	}
}

func TestCheckSDKTSPassesWithoutTests(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no directory": nil,
		"source only":  {"sdk/ts/index.ts": "export const x = 1;\n"},
		"dependency":   {"sdk/ts/node_modules/dep/a.test.ts": failingBunTest},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runMakeTarget(t, "check-sdk-ts", files)
			if err != nil {
				t.Fatalf("check-sdk-ts failed: %v\n%s", err, out)
			}
			if !strings.Contains(out, "no test files yet") {
				t.Fatalf("check-sdk-ts output:\n%s", out)
			}
		})
	}
}

func TestGoModuleCheckNamesMisformattedFiles(t *testing.T) {
	out, err := runMakeTarget(t, "check-sdk", map[string]string{"sdk/bad.go": "package   bad\n"})
	if err == nil {
		t.Fatalf("check-sdk passed on a misformatted file:\n%s", out)
	}
	if !strings.Contains(out, "bad.go") || strings.Contains(out, "No such file") {
		t.Fatalf("check-sdk did not list the file:\n%s", out)
	}
}
