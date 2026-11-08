// Package guard holds repository rules that are checked as tests. It has no
// non-test code: nothing here ships in the binary.
package guard

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const rootModule = "github.com/arafathusayn/herdsman"

// repoRoot is the repository root seen from this package's directory.
const repoRoot = "../.."

// serverPackages implement operations: a client file must not reach them, even
// through another package. The list may only grow; a new package that opens the
// store, runs git, signals a process or writes under the state directory is
// added by the task that creates it.
var serverPackages = []string{
	"internal/daemon",
	"internal/daemon/store",
	"internal/ops",
	"internal/wtree",
	"internal/gitrun",
	"internal/gitrefs",
	"internal/cow",
	"internal/ledger",
	"internal/snapshot",
	"internal/landing",
	"internal/policy",
	"internal/project",
	"internal/route",
	"internal/profile",
	"internal/plugin",
	"internal/context",
	"internal/automations",
	"internal/web",
}

// serverPackagesFloor makes shrinking serverPackages a visible change: raise it
// when the list grows, never lower it.
const serverPackagesFloor = 18

// wiringFiles run the daemon in-process, so they stay allowed.
var wiringFiles = []string{
	"cmd/herdsman/main.go",
	"cmd/herdsman/cmd_daemon.go",
}

// unmovedVerbFiles have not moved onto the SDK yet. The list may only shrink:
// remove a file when it moves; the guard fails on an entry that no longer needs
// its place.
var unmovedVerbFiles = []string{
	"cmd/herdsman/cmd_message.go",
	"cmd/herdsman/cmd_route.go",
}

// unmovedVerbFilesCeiling makes growing unmovedVerbFiles a visible change: lower
// it when the list shrinks, never raise it.
const unmovedVerbFilesCeiling = 2

// xSysModule may be imported only by the packages in xSysImporters.
const xSysModule = "golang.org/x/sys"

var xSysImporters = []string{"internal/sys", "internal/daemon", "internal/inbox"}

// guardConfig says what one run of the import guard checks. The repository and
// the fixtures differ only in these values.
type guardConfig struct {
	root      string
	env       []string
	servers   []string
	allowlist []string
}

// violation is a checked file whose import closure holds a server package.
type violation struct {
	file   string
	server string
}

// skippedFile is an allowlisted file; reachesServer says whether it would have
// failed without its place on the list.
type skippedFile struct {
	file          string
	reachesServer bool
}

type guardResult struct {
	passed     []string
	violations []violation
	skipped    []skippedFile
}

func (r guardResult) skippedNames() []string {
	names := make([]string, 0, len(r.skipped))
	for _, s := range r.skipped {
		names = append(names, s.file)
	}
	return names
}

// checkedFiles lists the client files under root, as slash-separated relative
// paths: the verb and form files, every Go file under sdk/, and the allowlist.
// Test files of cmd/herdsman are not client code, so they are left out.
func checkedFiles(root string, allowlist []string) ([]string, error) {
	var files []string
	for _, pattern := range []string{"cmd_*.go", "form_*.go"} {
		matches, err := filepath.Glob(filepath.Join(root, "cmd", "herdsman", pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if !strings.HasSuffix(match, "_test.go") {
				files = append(files, path.Join("cmd/herdsman", filepath.Base(match)))
			}
		}
	}
	sdkRoot := filepath.Join(root, "sdk")
	err := filepath.WalkDir(sdkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == sdkRoot && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, file := range allowlist {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			return nil, fmt.Errorf("allowlisted file: %w", err)
		}
		files = append(files, file)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

// fileImports returns the import paths of one Go file, without the cgo
// pseudo-package, which go list cannot resolve.
func fileImports(root, file string) ([]string, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file)), nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	imports := make([]string, 0, len(parsed.Imports))
	for _, spec := range parsed.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		if importPath != "C" {
			imports = append(imports, importPath)
		}
	}
	return imports, nil
}

// importClosure runs go list -deps on exactly the given import paths. It fails
// on an import go list cannot resolve, so a broken file cannot pass unchecked.
func importClosure(cfg guardConfig, imports []string) ([]string, error) {
	if len(imports) == 0 {
		return nil, nil
	}
	cmd := exec.Command("go", append([]string{"list", "-deps", "-f", "{{.ImportPath}}"}, imports...)...)
	cmd.Dir = cfg.root
	cmd.Env = cfg.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps %v: %w: %s", imports, err, stderr.String())
	}
	return strings.Fields(string(out)), nil
}

// analyze is the import guard. The unit is the file, because client files share
// package main with wiring files that may reach the daemon.
func analyze(cfg guardConfig) (guardResult, error) {
	var result guardResult
	files, err := checkedFiles(cfg.root, cfg.allowlist)
	if err != nil {
		return result, err
	}
	for _, file := range files {
		imports, err := fileImports(cfg.root, file)
		if err != nil {
			return result, err
		}
		closure, err := importClosure(cfg, imports)
		if err != nil {
			return result, fmt.Errorf("%s: %w", file, err)
		}
		server := ""
		for _, dep := range closure {
			if slices.Contains(cfg.servers, dep) {
				server = dep
				break
			}
		}
		switch {
		case slices.Contains(cfg.allowlist, file):
			result.skipped = append(result.skipped, skippedFile{file: file, reachesServer: server != ""})
		case server != "":
			result.violations = append(result.violations, violation{file: file, server: server})
		default:
			result.passed = append(result.passed, file)
		}
	}
	return result, nil
}

// fixtureConfig points the guard at one fixture module, whose server/ package
// stands in for the server list.
func fixtureConfig(name string, allowlist ...string) guardConfig {
	return guardConfig{
		root:      filepath.Join("testdata", name),
		env:       append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off"),
		servers:   []string{"example.com/" + name + "/server"},
		allowlist: allowlist,
	}
}

func TestGuardPassesAllowedFixture(t *testing.T) {
	result, err := analyze(fixtureConfig("allowed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.violations) != 0 || len(result.skipped) != 0 {
		t.Fatalf("violations %v, skipped %v; want none", result.violations, result.skipped)
	}
	want := []string{"cmd/herdsman/cmd_verb.go", "cmd/herdsman/form_verb.go", "sdk/version/version.go"}
	if !slices.Equal(result.passed, want) {
		t.Fatalf("passed %v, want %v", result.passed, want)
	}
}

func TestGuardRejectsRejectedFixture(t *testing.T) {
	result, err := analyze(fixtureConfig("rejected"))
	if err != nil {
		t.Fatal(err)
	}
	server := "example.com/rejected/server"
	want := []violation{
		{file: "cmd/herdsman/cmd_direct.go", server: server},
		{file: "cmd/herdsman/cmd_indirect.go", server: server},
	}
	if !slices.Equal(result.violations, want) {
		t.Fatalf("violations %v, want %v", result.violations, want)
	}
}

func TestGuardMixedFixtureChecksEachFile(t *testing.T) {
	result, err := analyze(fixtureConfig("mixed", "cmd/herdsman/cmd_wiring.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.violations) != 0 {
		t.Fatalf("violations %v, want none", result.violations)
	}
	if want := []string{"cmd/herdsman/cmd_thin.go"}; !slices.Equal(result.passed, want) {
		t.Fatalf("passed %v, want %v", result.passed, want)
	}
	if want := []string{"cmd/herdsman/cmd_wiring.go"}; !slices.Equal(result.skippedNames(), want) {
		t.Fatalf("skipped %v, want %v", result.skippedNames(), want)
	}
}

// An import nothing can resolve must stop the guard, not pass the file.
func TestGuardFailsOnUnresolvedImport(t *testing.T) {
	cfg := fixtureConfig("allowed")
	if _, err := importClosure(cfg, []string{"example.com/allowed/does-not-exist"}); err == nil {
		t.Fatal("importClosure resolved a package that does not exist")
	}
}

func TestGuardFailsOnMissingAllowlistedFile(t *testing.T) {
	if _, err := analyze(fixtureConfig("allowed", "cmd/herdsman/cmd_gone.go")); err == nil {
		t.Fatal("analyze accepted an allowlist entry for a file that does not exist")
	}
}

func TestCheckedFilesSkipsTestsAndTestdata(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{
		"cmd/herdsman/cmd_verb.go",
		"cmd/herdsman/cmd_verb_test.go",
		"cmd/herdsman/form_verb.go",
		"cmd/herdsman/help.go",
		"sdk/client/client.go",
		"sdk/client/testdata/x.go",
		"sdk/vendor/y/y.go",
	} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(file)), "package x\n")
	}
	got, err := checkedFiles(root, []string{"cmd/herdsman/help.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/herdsman/cmd_verb.go", "cmd/herdsman/form_verb.go", "cmd/herdsman/help.go", "sdk/client/client.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("checked %v, want %v", got, want)
	}
}

func TestCheckedFilesWithoutSDK(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cmd", "herdsman", "cmd_verb.go"), "package x\n")
	got, err := checkedFiles(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cmd/herdsman/cmd_verb.go"}; !slices.Equal(got, want) {
		t.Fatalf("checked %v, want %v", got, want)
	}
}

func TestFileImportsLeavesOutCgo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "package a\n\nimport (\n\t\"C\"\n\t\"fmt\"\n)\n")
	got, err := fileImports(root, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fmt"}; !slices.Equal(got, want) {
		t.Fatalf("imports %v, want %v", got, want)
	}
}

func TestFileImportsReportsParseError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bad.go"), "not go\n")
	if _, err := fileImports(root, "bad.go"); err == nil {
		t.Fatal("fileImports accepted a file that does not parse")
	}
}

// TestImportGuard is the rule on the repository itself. It logs every
// allowlisted file it skipped; check-root runs it with -v so the list is
// visible in each run.
func TestImportGuard(t *testing.T) {
	if len(serverPackages) < serverPackagesFloor {
		t.Errorf("serverPackages has %d entries, below the floor of %d: the list may only grow", len(serverPackages), serverPackagesFloor)
	}
	if len(unmovedVerbFiles) > unmovedVerbFilesCeiling {
		t.Errorf("unmovedVerbFiles has %d entries, above the ceiling of %d: the list may only shrink", len(unmovedVerbFiles), unmovedVerbFilesCeiling)
	}
	servers := make([]string, 0, len(serverPackages))
	for _, pkg := range serverPackages {
		servers = append(servers, rootModule+"/"+pkg)
	}
	result, err := analyze(guardConfig{
		root:      repoRoot,
		env:       os.Environ(),
		servers:   servers,
		allowlist: slices.Concat(wiringFiles, unmovedVerbFiles),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range result.violations {
		t.Errorf("%s reaches server package %s: use the SDK, or a local presentation package", v.file, v.server)
	}
	for _, s := range result.skipped {
		t.Logf("allowlisted, skipped: %s", s.file)
		if slices.Contains(unmovedVerbFiles, s.file) && !s.reachesServer {
			t.Errorf("%s no longer reaches a server package: remove it from unmovedVerbFiles", s.file)
		}
	}
	for _, file := range wiringFiles {
		if !slices.Contains(result.skippedNames(), file) {
			t.Errorf("%s did not pass by the allowlist", file)
		}
	}
}

// xSysViolations lists the Go files under root that import xSysModule from a
// package outside the allowed directories.
func xSysViolations(root string, allowed []string) ([]string, error) {
	var bad []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "web", "vendor", "third_party", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		file := filepath.ToSlash(rel)
		imports, err := fileImports(root, file)
		if err != nil {
			return err
		}
		for _, imported := range imports {
			if (imported == xSysModule || strings.HasPrefix(imported, xSysModule+"/")) && !slices.Contains(allowed, path.Dir(file)) {
				bad = append(bad, file)
				break
			}
		}
		return nil
	})
	return bad, err
}

func TestXSysOnlyInAllowedPackages(t *testing.T) {
	bad, err := xSysViolations(repoRoot, xSysImporters)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("%s imported outside %v by %v", xSysModule, xSysImporters, bad)
	}
}

func TestXSysRuleCatchesAPlantedImport(t *testing.T) {
	root := t.TempDir()
	source := "package p\n\nimport _ \"golang.org/x/sys/unix\"\n"
	writeFile(t, filepath.Join(root, "internal", "daemon", "ok.go"), source)
	writeFile(t, filepath.Join(root, "internal", "other", "bad.go"), source)
	writeFile(t, filepath.Join(root, "web", "vendor", "skipped.go"), source)
	writeFile(t, filepath.Join(root, "internal", "other", "clean.go"), "package p\n\nimport _ \"fmt\"\n")
	writeFile(t, filepath.Join(root, "internal", "other", "notes.txt"), "not go\n")
	bad, err := xSysViolations(root, xSysImporters)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/other/bad.go"}; !slices.Equal(bad, want) {
		t.Fatalf("violations %v, want %v", bad, want)
	}
}

func TestXSysViolationsReportsParseError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bad.go"), "not go\n")
	if _, err := xSysViolations(root, nil); err == nil {
		t.Fatal("xSysViolations accepted a file that does not parse")
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
