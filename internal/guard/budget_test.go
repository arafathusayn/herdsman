package guard

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// upstream is one codebase the repository carries a pinned copy of. copyDir is
// set for a copy under third_party/ imported through a replace; empty means the
// module lives only under vendor/.
type upstream struct {
	module  string
	copyDir string
}

// vendorFile is where the upstream's VENDOR.md lives.
func (u upstream) vendorFile() string {
	if u.copyDir != "" {
		return u.copyDir + "/VENDOR.md"
	}
	return "vendor/" + u.module + "/VENDOR.md"
}

// dependencyBudget names each outside project the repository carries a copy
// of. A new entry is a decision, not a side effect of a build.
var dependencyBudget = []upstream{
	{module: "github.com/mattn/go-sqlite3", copyDir: "third_party/go-sqlite3"},
	{module: "golang.org/x/sys"},
	{module: "google.golang.org/protobuf"},
	{module: "rsc.io/qr", copyDir: "third_party/qr"},
}

// firstPartyModules are the repository's own modules, by go.work directory.
// Only these are exempt from the budget; any other workspace module is rejected.
var firstPartyModules = map[string]string{
	".":                 rootModule,
	"./sdk":             rootModule + "/sdk",
	"./sdk/conformance": rootModule + "/sdk/conformance",
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// workspaceDirs returns the module directories a go.work lists.
func workspaceDirs(goWork string) []string {
	var dirs []string
	inUse := false
	for line := range strings.Lines(goWork) {
		line = strings.TrimSpace(line)
		switch {
		case line == "use (":
			inUse = true
		case inUse && line == ")":
			inUse = false
		case inUse && line != "":
			dirs = append(dirs, line)
		case strings.HasPrefix(line, "use "):
			dirs = append(dirs, strings.TrimSpace(strings.TrimPrefix(line, "use ")))
		}
	}
	return dirs
}

// goModFacts is what the budget reads from one go.mod.
type goModFacts struct {
	module   string
	requires map[string]string
	replaces []goReplace
}

// goReplace is one replace directive; version is set when it names one.
type goReplace struct {
	module, version, target string
}

// parseGoMod reads the directives the budget needs. The repository takes no
// dependency for this: go.mod's line grammar is small and stable.
func parseGoMod(text string) goModFacts {
	facts := goModFacts{requires: map[string]string{}}
	block := ""
	for line := range strings.Lines(text) {
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
		case block != "" && fields[0] == ")":
			block = ""
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
		case block != "":
			facts.record(block, fields)
		default:
			facts.record(fields[0], fields[1:])
		}
	}
	return facts
}

func (f *goModFacts) record(directive string, fields []string) {
	switch {
	case directive == "module" && len(fields) == 1:
		f.module = fields[0]
	case directive == "require" && len(fields) == 2:
		f.requires[fields[0]] = fields[1]
	case directive == "replace":
		if arrow := slices.Index(fields, "=>"); arrow >= 1 && arrow < len(fields)-1 {
			entry := goReplace{module: fields[0], target: strings.Join(fields[arrow+1:], " ")}
			if arrow == 2 {
				entry.version = fields[1]
			}
			f.replaces = append(f.replaces, entry)
		}
	}
}

// vendoredModule is one "# module version => replacement" line of modules.txt.
type vendoredModule struct {
	module, version, replacement string
}

func parseModulesTxt(text string) []vendoredModule {
	var modules []vendoredModule
	for line := range strings.Lines(text) {
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		left, replacement, _ := strings.Cut(strings.TrimSpace(line[2:]), " => ")
		fields := strings.Fields(left)
		entry := vendoredModule{module: fields[0], replacement: strings.TrimSpace(replacement)}
		if len(fields) > 1 {
			entry.version = fields[1]
		}
		modules = append(modules, entry)
	}
	return modules
}

// vendorFields reads the "- Key: value" lines of a VENDOR.md.
func vendorFields(text string) map[string]string {
	fields := map[string]string{}
	for line := range strings.Lines(text) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
			if key, value, ok := strings.Cut(rest, ": "); ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
	}
	return fields
}

// treeSum is Go's h1 directory hash of a module copy, leaving out VENDOR.md,
// the one file the repository adds, so a copy is checked against the go.sum
// line of its upstream release with no network.
func treeSum(dir, module, version string) (string, error) {
	var names []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel != "VENDOR.md" {
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	type entry struct{ name, rel string }
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, entry{name: module + "@" + version + "/" + name, rel: name})
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	summary := sha256.New()
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(e.rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(summary, "%x  %s\n", sha256.Sum256(data), e.name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(summary.Sum(nil)), nil
}

// checkVendorFile returns what is wrong with one upstream's VENDOR.md and copy.
func checkVendorFile(root string, u upstream, version string) []string {
	name := u.vendorFile()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return []string{fmt.Sprintf("%s: no %s (run make vendor to restore it)", u.module, name)}
	}
	text := string(data)
	fields := vendorFields(text)
	var problems []string
	for _, key := range []string{"Upstream", "License"} {
		if fields[key] == "" {
			problems = append(problems, fmt.Sprintf("%s: %s has no %s", u.module, name, key))
		}
	}
	if fields["Module"] != u.module {
		problems = append(problems, fmt.Sprintf("%s: %s names module %q", u.module, name, fields["Module"]))
	}
	if fields["Version"] != version {
		problems = append(problems, fmt.Sprintf("%s: %s pins %q, vendor/modules.txt selects %q", u.module, name, fields["Version"], version))
	}
	if !commitID.MatchString(fields["Commit"]) {
		problems = append(problems, fmt.Sprintf("%s: %s has no full upstream commit", u.module, name))
	}
	if !strings.Contains(text, "## Re-vendor") {
		problems = append(problems, fmt.Sprintf("%s: %s has no re-vendor steps", u.module, name))
	}
	hashed := u.copyDir
	if hashed == "" {
		hashed = "vendor/" + u.module
	}
	sum, err := treeSum(filepath.Join(root, filepath.FromSlash(hashed)), u.module, version)
	if err != nil {
		return append(problems, fmt.Sprintf("%s: %v", u.module, err))
	}
	if sum != fields["Sum"] {
		problems = append(problems, fmt.Sprintf("%s: %s hashes to %s, %s records %q", u.module, hashed, sum, name, fields["Sum"]))
	}
	if u.copyDir != "" {
		problems = append(problems, compiledCopyProblems(root, u)...)
	}
	return problems
}

// compiledCopyProblems compares what the compiler reads, under vendor/, with
// the checksummed copy under third_party/: every vendored file must be there,
// byte for byte.
func compiledCopyProblems(root string, u upstream) []string {
	vendored := filepath.Join(root, "vendor", filepath.FromSlash(u.module))
	var problems []string
	err := filepath.WalkDir(vendored, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(vendored, p)
		if err != nil {
			return err
		}
		compiled, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(u.copyDir), rel))
		if err != nil || string(source) != string(compiled) {
			name := filepath.ToSlash(rel)
			problems = append(problems, fmt.Sprintf("vendor/%s/%s differs from %s/%s", u.module, name, u.copyDir, name))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", u.module, err))
	}
	return problems
}

// replaceProblems rejects a replace that points anywhere but the module's
// approved source: its third_party copy, or a declared first-party directory.
// A module that lives only under vendor/ has no approved replacement.
func replaceProblems(file, dir string, replaces []goReplace, approved map[string]string) []string {
	var problems []string
	for _, r := range replaces {
		if want, ok := approved[r.module]; !ok || !strings.HasPrefix(r.target, ".") || path.Join(dir, r.target) != want {
			problems = append(problems, fmt.Sprintf("%s replaces %s with %s", file, r.module, r.target))
		}
	}
	return problems
}

// budgetProblems checks every module's go.mod and vendor/modules.txt against
// the budget: every require maps to an entry, every entry is required, and
// modules.txt resolves nothing else.
func budgetProblems(root string, budget []upstream, declared map[string]string) ([]string, error) {
	goWork, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		return nil, err
	}
	var problems []string
	modules := map[string]goModFacts{}
	dirs := workspaceDirs(string(goWork))
	for _, dir := range dirs {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
		if err != nil {
			return nil, err
		}
		facts := parseGoMod(string(data))
		modules[dir] = facts
		want, ok := declared[dir]
		if !ok {
			problems = append(problems, fmt.Sprintf("workspace module %s is not declared", dir))
		} else if facts.module != want {
			problems = append(problems, fmt.Sprintf("%s declares module %q, want %q", dir, facts.module, want))
		}
	}

	// Only the declared modules are first-party, whatever go.work says.
	own := map[string]bool{}
	approved := map[string]string{}
	for dir, module := range declared {
		own[module] = true
		approved[module] = path.Clean(dir)
		if !slices.Contains(dirs, dir) {
			problems = append(problems, "go.work does not use "+dir)
		}
	}
	listed := map[string]upstream{}
	for _, u := range budget {
		listed[u.module] = u
		if u.copyDir != "" {
			approved[u.module] = u.copyDir
		}
	}

	problems = append(problems, replaceProblems("go.work", ".", parseGoMod(string(goWork)).replaces, approved)...)

	txt, err := os.ReadFile(filepath.Join(root, "vendor", "modules.txt"))
	if err != nil {
		return nil, err
	}
	selected := map[string]string{}
	for _, entry := range parseModulesTxt(string(txt)) {
		if own[entry.module] {
			continue
		}
		u, ok := listed[entry.module]
		if !ok {
			problems = append(problems, fmt.Sprintf("vendor/modules.txt resolves %s, which is not in the budget", entry.module))
			continue
		}
		want := ""
		if u.copyDir != "" {
			want = "./" + u.copyDir
		}
		if entry.replacement != want {
			problems = append(problems, fmt.Sprintf("vendor/modules.txt resolves %s from %s", entry.module, entry.replacement))
		}
		if entry.version != "" {
			selected[entry.module] = entry.version
		}
	}

	required := map[string]bool{}
	for _, dir := range dirs {
		facts := modules[dir]
		problems = append(problems, replaceProblems(dir+"/go.mod", dir, facts.replaces, approved)...)
		for module, version := range facts.requires {
			if own[module] {
				continue
			}
			u, ok := listed[module]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s/go.mod requires %s, which is not in the budget", dir, module))
				continue
			}
			required[module] = true
			if chosen, ok := selected[module]; ok && chosen != version {
				problems = append(problems, fmt.Sprintf("%s/go.mod requires %s %s, vendor/modules.txt selects %s", dir, module, version, chosen))
			}
			if u.copyDir != "" && !slices.ContainsFunc(facts.replaces, func(r goReplace) bool {
				return r.module == module && path.Join(dir, r.target) == u.copyDir
			}) {
				problems = append(problems, fmt.Sprintf("%s/go.mod must replace %s with ./%s", dir, module, u.copyDir))
			}
		}
	}

	for _, u := range budget {
		version, ok := selected[u.module]
		switch {
		case !required[u.module]:
			problems = append(problems, fmt.Sprintf("%s is in the budget and no module requires it", u.module))
		case !ok:
			problems = append(problems, "vendor/modules.txt selects no version of "+u.module)
		default:
			problems = append(problems, checkVendorFile(root, u, version)...)
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// TestDependencyBudget is the real gate; make guard-report runs it with -v so
// the upstream codebases are listed by name in every run.
func TestDependencyBudget(t *testing.T) {
	for _, u := range dependencyBudget {
		t.Logf("upstream: %s (%s)", u.module, u.vendorFile())
	}
	problems, err := budgetProblems(repoRoot, dependencyBudget, firstPartyModules)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

const budgetFixtureCommit = "0123456789012345678901234567890123456789"

func fixtureVendorMD(module, version, sum string) string {
	text := "# " + module + "\n\n- Module: " + module + "\n- Upstream: https://example.test/x\n- Version: " + version + "\n- Commit: " + budgetFixtureCommit + "\n- License: MIT\n"
	if sum != "" {
		text += "- Sum: " + sum + "\n"
	}
	return text + "\n## Re-vendor\n\nSteps.\n"
}

var budgetFixtureList = []upstream{
	{module: "example.test/copied", copyDir: "third_party/copied"},
	{module: "example.test/kept"},
}

var budgetFixtureDeclared = map[string]string{".": "example.test/root", "./sdk": "example.test/root/sdk"}

// budgetFixture writes a tree that passes the budget and returns its root.
func budgetFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.work":                      "go 1.26.0\n\nuse (\n\t.\n\t./sdk\n)\n",
		"go.mod":                       "module example.test/root\n\ngo 1.26.0\n\nrequire (\n\texample.test/copied v1.0.0 // the copy\n\texample.test/kept v1.2.0\n)\n\nrequire example.test/root/sdk v0.0.0\n\nreplace example.test/copied => ./third_party/copied\n\nreplace example.test/root/sdk => ./sdk\n",
		"sdk/go.mod":                   "module example.test/root/sdk\n\ngo 1.26.0\n",
		"third_party/copied/a":         "copied\n",
		"vendor/modules.txt":           "## workspace\n# example.test/copied v1.0.0 => ./third_party/copied\n## explicit\nexample.test/copied\n# example.test/kept v1.2.0\n## explicit\nexample.test/kept\n# example.test/root/sdk v0.0.0 => ./sdk\n",
		"vendor/example.test/copied/a": "copied\n",
		"vendor/example.test/kept/k":   "kept\n",
	} {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	sum, err := treeSum(filepath.Join(root, "third_party/copied"), "example.test/copied", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "third_party/copied/VENDOR.md"), fixtureVendorMD("example.test/copied", "v1.0.0", sum))
	keptSum, err := treeSum(filepath.Join(root, "vendor/example.test/kept"), "example.test/kept", "v1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "vendor/example.test/kept/VENDOR.md"), fixtureVendorMD("example.test/kept", "v1.2.0", keptSum))
	return root
}

func TestBudgetFixturePasses(t *testing.T) {
	problems, err := budgetProblems(budgetFixture(t), budgetFixtureList, budgetFixtureDeclared)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("clean fixture has problems: %v", problems)
	}
}

// Each planted break must produce the problem that names it.
func TestBudgetFailsWhenARuleIsBroken(t *testing.T) {
	replaceIn := func(name, old, new string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			t.Helper()
			file := filepath.Join(root, filepath.FromSlash(name))
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), old) {
				t.Fatalf("%s does not contain %q", name, old)
			}
			writeFile(t, file, strings.ReplaceAll(string(data), old, new))
		}
	}
	remove := func(name string) func(*testing.T, string) {
		return func(t *testing.T, root string) {
			t.Helper()
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
	}
	const keptMD = "vendor/example.test/kept/VENDOR.md"
	const copiedMD = "third_party/copied/VENDOR.md"
	for _, tc := range []struct {
		name  string
		plant func(*testing.T, string)
		want  string
	}{
		{"unlisted require in the root", replaceIn("go.mod", "require example.test/root/sdk v0.0.0", "require example.test/new v1.0.0 // indirect"), "go.mod requires example.test/new"},
		{"unlisted require in a nested module", replaceIn("sdk/go.mod", "go 1.26.0", "go 1.26.0\n\nrequire example.test/nested v1.0.0"), "sdk/go.mod requires example.test/nested"},
		{"modules.txt resolves another module", replaceIn("vendor/modules.txt", "## workspace\n", "## workspace\n# example.test/extra v1.0.0\n"), "modules.txt resolves example.test/extra"},
		{"a copy without its replace", replaceIn("go.mod", "replace example.test/copied => ./third_party/copied\n", ""), "must replace example.test/copied"},
		{"a budget entry nobody requires", replaceIn("go.mod", "\texample.test/kept v1.2.0\n", ""), "example.test/kept is in the budget and no module requires it"},
		{"a missing VENDOR.md", remove(keptMD), "no " + keptMD},
		{"no upstream", replaceIn(keptMD, "- Upstream: https://example.test/x\n", ""), "has no Upstream"},
		{"no license", replaceIn(keptMD, "- License: MIT\n", ""), "has no License"},
		{"the wrong module", replaceIn(keptMD, "- Module: example.test/kept", "- Module: example.test/other"), "names module"},
		{"a stale version", replaceIn(keptMD, "- Version: v1.2.0", "- Version: v1.1.0"), `pins "v1.1.0", vendor/modules.txt selects "v1.2.0"`},
		{"a short commit", replaceIn(keptMD, "- Commit: "+budgetFixtureCommit, "- Commit: abc123"), "no full upstream commit"},
		{"no re-vendor steps", replaceIn(keptMD, "## Re-vendor", "## Notes"), "no re-vendor steps"},
		{"an edited copy", replaceIn("third_party/copied/a", "copied", "edited"), "hashes to"},
		{"a tampered sum", replaceIn(copiedMD, "- Sum: h1:", "- Sum: h1:x"), "hashes to"},
		// D1: the source the compiler uses, however it is substituted.
		{"go.work replaces a copy", replaceIn("go.work", "use (", "replace example.test/copied => ./override\n\nuse ("), "go.work replaces example.test/copied with ./override"},
		{"go.work replaces a vendor-only module", replaceIn("go.work", "use (", "replace example.test/kept => ./override\n\nuse ("), "go.work replaces example.test/kept with ./override"},
		{"go.work replaces an unlisted module", replaceIn("go.work", "use (", "replace example.test/other => ./override\n\nuse ("), "go.work replaces example.test/other with ./override"},
		{"a version-specific replace", replaceIn("go.mod", "replace example.test/root/sdk", "replace example.test/copied v1.0.0 => ./override\n\nreplace example.test/root/sdk"), "go.mod replaces example.test/copied with ./override"},
		{"a replace of a vendor-only module", replaceIn("go.mod", "replace example.test/root/sdk", "replace example.test/kept => ./elsewhere\n\nreplace example.test/root/sdk"), "go.mod replaces example.test/kept with ./elsewhere"},
		{"a first-party replace elsewhere", replaceIn("go.mod", "replace example.test/root/sdk => ./sdk", "replace example.test/root/sdk => ./fork"), "go.mod replaces example.test/root/sdk with ./fork"},
		{"modules.txt resolves a copy elsewhere", replaceIn("vendor/modules.txt", "=> ./third_party/copied", "=> ./override"), "vendor/modules.txt resolves example.test/copied from ./override"},
		{"modules.txt replaces a vendor-only module", replaceIn("vendor/modules.txt", "# example.test/kept v1.2.0", "# example.test/kept v1.2.0 => ./override"), "vendor/modules.txt resolves example.test/kept from ./override"},
		{"compiled copy edited", replaceIn("vendor/example.test/copied/a", "copied", "edited"), "vendor/example.test/copied/a differs from third_party/copied/a"},
		{"compiled copy gained a file", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "vendor/example.test/copied/extra.go"), "package copied\n")
		}, "vendor/example.test/copied/extra.go differs from third_party/copied/extra.go"},
		{"compiled vendor-only module edited", replaceIn("vendor/example.test/kept/k", "kept", "edited"), "vendor/example.test/kept hashes to"},
		// D2: only the declared repository modules are first-party.
		{"an undeclared workspace module", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "third_party/unlisted/go.mod"), "module example.test/unlisted\n")
			replaceIn("go.work", "\t./sdk\n", "\t./sdk\n\t./third_party/unlisted\n")(t, root)
			replaceIn("sdk/go.mod", "go 1.26.0", "go 1.26.0\n\nrequire example.test/unlisted v1.0.0")(t, root)
		}, "workspace module ./third_party/unlisted is not declared"},
		{"an undeclared workspace module is not exempt", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "third_party/unlisted/go.mod"), "module example.test/unlisted\n")
			replaceIn("go.work", "\t./sdk\n", "\t./sdk\n\t./third_party/unlisted\n")(t, root)
			replaceIn("sdk/go.mod", "go 1.26.0", "go 1.26.0\n\nrequire example.test/unlisted v1.0.0")(t, root)
		}, "sdk/go.mod requires example.test/unlisted"},
		{"a declared directory with another module", replaceIn("sdk/go.mod", "module example.test/root/sdk", "module example.test/renamed"), `./sdk declares module "example.test/renamed", want "example.test/root/sdk"`},
		{"a declared module left out of go.work", replaceIn("go.work", "\t./sdk\n", ""), "go.work does not use ./sdk"},
		// D3: the version the build selects.
		{"a requirement below the selection", replaceIn("sdk/go.mod", "go 1.26.0", "go 1.26.0\n\nrequire example.test/kept v1.1.0"), "sdk/go.mod requires example.test/kept v1.1.0, vendor/modules.txt selects v1.2.0"},
		{"VENDOR.md follows the lower requirement", func(t *testing.T, root string) {
			replaceIn("sdk/go.mod", "go 1.26.0", "go 1.26.0\n\nrequire example.test/kept v1.1.0")(t, root)
			replaceIn(keptMD, "- Version: v1.2.0", "- Version: v1.1.0")(t, root)
		}, `pins "v1.1.0", vendor/modules.txt selects "v1.2.0"`},
		{"a required module with no selection", replaceIn("vendor/modules.txt", "# example.test/kept v1.2.0\n", ""), "vendor/modules.txt selects no version of example.test/kept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := budgetFixture(t)
			tc.plant(t, root)
			problems, err := budgetProblems(root, budgetFixtureList, budgetFixtureDeclared)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("want a problem containing %q, got %v", tc.want, problems)
			}
		})
	}
}

func TestBudgetReportsUnreadableFiles(t *testing.T) {
	for _, name := range []string{"go.work", "sdk/go.mod", "vendor/modules.txt"} {
		t.Run(name, func(t *testing.T) {
			root := budgetFixture(t)
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
			if _, err := budgetProblems(root, budgetFixtureList, budgetFixtureDeclared); err == nil {
				t.Fatalf("no error without %s", name)
			}
		})
	}
}

func TestBudgetReportsAMissingCopy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "third_party/copied/VENDOR.md"), fixtureVendorMD("example.test/copied", "v1.0.0", "h1:x"))
	if err := os.RemoveAll(filepath.Join(root, "third_party/copied")); err != nil {
		t.Fatal(err)
	}
	if _, err := treeSum(filepath.Join(root, "third_party/copied"), "example.test/copied", "v1.0.0"); err == nil {
		t.Fatal("no error for a missing copy")
	}
}

// The hash must be Go's h1, or a copy cannot be compared with go.sum.
// TestDependencyBudget proves it on real releases against their go.sum lines;
// this test pins the sort order and the VENDOR.md exclusion.
func TestTreeSumMatchesGoSumAlgorithm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
	writeFile(t, filepath.Join(dir, "sub", "a.txt"), "a\n")
	writeFile(t, filepath.Join(dir, "VENDOR.md"), "left out\n")
	summary := sha256.New()
	for _, line := range []struct{ content, name string }{{"b\n", "b.txt"}, {"a\n", "sub/a.txt"}} {
		fmt.Fprintf(summary, "%x  m@v1.0.0/%s\n", sha256.Sum256([]byte(line.content)), line.name)
	}
	want := "h1:" + base64.StdEncoding.EncodeToString(summary.Sum(nil))
	got, err := treeSum(dir, "m", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("treeSum = %s, want %s", got, want)
	}
}

func TestParseHelpers(t *testing.T) {
	if got := workspaceDirs("go 1.26.0\nuse ./one\nuse (\n\t./two\n\n)\n"); !slices.Equal(got, []string{"./one", "./two"}) {
		t.Errorf("workspaceDirs = %v", got)
	}
	facts := parseGoMod("module m\n\nrequire a v1.0.0 // indirect\nreplace (\n\tb v1.0.0 => ./b\n\tc => d v2.0.0\n\tmalformed\n)\n// comment only\n")
	if facts.module != "m" || facts.requires["a"] != "v1.0.0" || !slices.Equal(facts.replaces, []goReplace{{"b", "v1.0.0", "./b"}, {"c", "", "d v2.0.0"}}) {
		t.Errorf("parseGoMod = %+v", facts)
	}
	got := parseModulesTxt("## workspace\n# a v1.0.0\n## explicit\na/pkg\n# b => ./b\n")
	want := []vendoredModule{{module: "a", version: "v1.0.0"}, {module: "b", replacement: "./b"}}
	if !slices.Equal(got, want) {
		t.Errorf("parseModulesTxt = %+v", got)
	}
	if fields := vendorFields("- Key: value\n- no separator\nplain: line\n"); len(fields) != 1 || fields["Key"] != "value" {
		t.Errorf("vendorFields = %v", fields)
	}
}
