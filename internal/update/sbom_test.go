package update

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const manifest = `# %NAME%

- Module: %NAME%
- Upstream: https://example.test/%NAME%
- Version: v1.0.0
- Commit: 0123456789012345678901234567890123456789
- License: MIT
- Sum: h1:abc=
`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func manifestFor(name string) string { return strings.ReplaceAll(manifest, "%NAME%", name) }

func noGit(string, string) (string, error) { return "", os.ErrNotExist }

func names(cs []Component) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

func TestComponentsReadsVendorAndThirdParty(t *testing.T) {
	root := writeTree(t, map[string]string{
		"vendor/a.test/one/VENDOR.md":       manifestFor("a.test/one"),
		"third_party/two/VENDOR.md":         manifestFor("b.test/two"),
		"vendor/b.test/two/VENDOR.md":       manifestFor("b.test/two"), // the same copy, listed twice
		"third_party/VENDOR.template.md":    "# not a manifest",
		"third_party/two/sqlite3-binding.c": "#define SQLITE_VERSION        \"3.50.1\"\n",
	})
	got, err := Components(root, noGit)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.test/one b.test/two sqlite"
	if s := strings.Join(names(got), " "); s != want {
		t.Fatalf("components %q, want %q", s, want)
	}
	if got[2].Version != "3.50.1" || got[2].License != "blessing" {
		t.Fatalf("sqlite %+v", got[2])
	}
}

func TestComponentsExcludesConformanceByRule(t *testing.T) {
	root := writeTree(t, map[string]string{
		"vendor/a.test/one/VENDOR.md":              manifestFor("a.test/one"),
		"third_party/conformance/x/VENDOR.md":      manifestFor("c.test/test-only"),
		"vendor/sdk/conformance/connect/VENDOR.md": manifestFor("c.test/connect"),
	})
	got, err := Components(root, noGit)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(names(got), " "); s != "a.test/one" {
		t.Fatalf("components %q", s)
	}
}

func TestComponentsRefusesBrokenManifests(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"missing field": {"vendor/a/VENDOR.md": "# a\n\n- Module: a\n"},
		"two versions": {
			"vendor/a/VENDOR.md":      manifestFor("a"),
			"third_party/a/VENDOR.md": strings.Replace(manifestFor("a"), "v1.0.0", "v2.0.0", 1),
		},
		"sqlite without a version": {"third_party/s/sqlite3-binding.c": "int x;\n"},
		"submodule without url":    {".gitmodules": "[submodule \"x\"]\n\tpath = web/vendor/x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Components(writeTree(t, files), noGit); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestComponentsReadsSubmodules(t *testing.T) {
	root := writeTree(t, map[string]string{
		".gitmodules":               "[submodule \"x\"]\n\tpath = web/vendor/x\n\turl = https://example.test/x.git\n",
		"web/vendor/x/package.json": `{"license": "MIT"}`,
	})
	got, err := Components(root, func(_, path string) (string, error) {
		if path != "web/vendor/x" {
			t.Fatalf("path %q", path)
		}
		return strings.Repeat("c", 40), nil
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, err %v", got, err)
	}
	c := got[0]
	if c.Name != "x" || c.Upstream != "https://example.test/x.git" || c.Commit != strings.Repeat("c", 40) || c.License != "MIT" {
		t.Fatalf("%+v", c)
	}
	if _, err := Components(root, noGit); err == nil {
		t.Fatal("a submodule without a recorded commit was accepted")
	}
	bad := writeTree(t, map[string]string{
		".gitmodules":               "[submodule \"x\"]\n\tpath = web/vendor/x\n\turl = https://example.test/x.git\n",
		"web/vendor/x/package.json": `{}`,
	})
	if _, err := Components(bad, func(string, string) (string, error) { return "c", nil }); err == nil {
		t.Fatal("a submodule without a license was accepted")
	}
}

func TestGitlinkReadsTheRecordedCommit(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := Gitlink(root, "web/vendor/reactre")
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		t.Fatalf("commit %q, err %v", commit, err)
	}
	if _, err := Gitlink(root, "web/vendor/none"); err == nil {
		t.Fatal("a path that is no submodule was accepted")
	}
}

func TestSPDXIsStableAndComplete(t *testing.T) {
	cs := []Component{{Name: "a.test/one", Upstream: "https://example.test", Version: "v1.0.0", Commit: "abc", License: "MIT"}}
	a, err := SPDX("herdsman", "1.0.0", "2026-01-02T03:04:05Z", cs)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SPDX("herdsman", "1.0.0", "2026-01-02T03:04:05Z", cs)
	if string(a) != string(b) {
		t.Fatal("two runs differ")
	}
	var doc struct {
		SPDXVersion  string `json:"spdxVersion"`
		CreationInfo struct{ Created string }
		Packages     []struct {
			Name             string `json:"name"`
			SPDXID           string `json:"SPDXID"`
			VersionInfo      string `json:"versionInfo"`
			LicenseDeclared  string `json:"licenseDeclared"`
			DownloadLocation string `json:"downloadLocation"`
		} `json:"packages"`
		Relationships []struct{ RelationshipType string } `json:"relationships"`
	}
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SPDXVersion != "SPDX-2.3" || doc.CreationInfo.Created != "2026-01-02T03:04:05Z" || len(doc.Packages) != 2 || len(doc.Relationships) != 2 {
		t.Fatalf("%+v", doc)
	}
	p := doc.Packages[1]
	if p.Name != "a.test/one" || p.VersionInfo != "v1.0.0" || p.LicenseDeclared != "MIT" || !regexp.MustCompile(`^SPDXRef-[A-Za-z0-9.-]+$`).MatchString(p.SPDXID) {
		t.Fatalf("%+v", p)
	}
	if _, err := SPDX("herdsman", "1.0.0", "yesterday", cs); err == nil {
		t.Fatal("a bad time was accepted")
	}
}

// The check that the shipped SBOM is whole: an independent walk of the real
// tree, so a manifest the generator skips fails here.
func TestSBOMListsEveryVendorManifest(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Components(root, Gitlink)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, c := range got {
		listed[c.Name] = true
	}
	module := regexp.MustCompile(`(?m)^- Module: (\S+)$`)
	seen := 0
	for _, dir := range []string{"vendor", "third_party"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "VENDOR.md" || strings.Contains(p, "/conformance/") {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m := module.FindSubmatch(data)
			if m == nil {
				t.Errorf("%s names no module", p)
				return nil
			}
			seen++
			if !listed[string(m[1])] {
				t.Errorf("%s is not in the SBOM", m[1])
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("no manifest found: the walk is wrong")
	}
	for _, shipped := range []string{"sqlite", "reactre"} {
		if !listed[shipped] {
			t.Errorf("%s is shipped but not in the SBOM: %v", shipped, names(got))
		}
	}
}
