package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Component is one upstream codebase the release binaries carry.
type Component struct {
	Name     string
	Upstream string
	Version  string
	Commit   string
	License  string
}

// GitlinkFunc returns the commit recorded for a submodule path.
type GitlinkFunc func(root, path string) (string, error)

// Gitlink reads a submodule's commit from the repository's HEAD.
func Gitlink(root, path string) (string, error) {
	out, err := exec.Command("git", "-C", root, "ls-tree", "HEAD", "--", path).Output()
	if err != nil {
		return "", fmt.Errorf("read submodule %s: %w", path, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || fields[1] != "commit" {
		return "", fmt.Errorf("%s is not a submodule", path)
	}
	return fields[2], nil
}

var (
	vendorField   = regexp.MustCompile(`(?m)^- (Module|Upstream|Version|Commit|License): (\S+)`)
	sqliteVersion = regexp.MustCompile(`(?m)^#define SQLITE_VERSION\s+"([^"]+)"`)
	submodulePath = regexp.MustCompile(`(?m)^\s*path\s*=\s*(\S+)\s*$`)
	submoduleURL  = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)\s*$`)
)

// testOnly says a folder holds code no binary ships. The conformance module
// is the case today; it is a rule on the path so that a later test-only copy
// needs no change here.
func testOnly(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "conformance" {
			return true
		}
	}
	return false
}

// Components lists what the binaries carry: each VENDOR.md under vendor/ and
// third_party/, the SQLite amalgamation inside a copied driver, and each git
// submodule. A module present in both trees is one component.
func Components(root string, gitlink GitlinkFunc) ([]Component, error) {
	found := map[string]Component{}
	add := func(c Component) error {
		if old, ok := found[c.Name]; ok && old != c {
			return fmt.Errorf("%s is recorded twice, as %s and %s", c.Name, old.Version, c.Version)
		}
		found[c.Name] = c
		return nil
	}
	for _, dir := range []string{"vendor", "third_party"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			if d.IsDir() {
				if testOnly(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			var c Component
			switch d.Name() {
			case "VENDOR.md":
				c, err = readManifest(p)
			case "sqlite3-binding.c":
				c, err = readSQLite(p)
			default:
				return nil
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			return add(c)
		})
		if err != nil {
			return nil, err
		}
	}
	subs, err := readSubmodules(root, gitlink)
	if err != nil {
		return nil, err
	}
	for _, c := range subs {
		if err := add(c); err != nil {
			return nil, err
		}
	}
	out := make([]Component, 0, len(found))
	for _, c := range found {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readManifest(path string) (Component, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Component{}, err
	}
	fields := map[string]string{}
	for _, m := range vendorField.FindAllSubmatch(data, -1) {
		fields[string(m[1])] = string(m[2])
	}
	for _, need := range []string{"Module", "Upstream", "Version", "Commit", "License"} {
		if fields[need] == "" {
			return Component{}, fmt.Errorf("no %s line", need)
		}
	}
	return Component{Name: fields["Module"], Upstream: fields["Upstream"], Version: fields["Version"], Commit: fields["Commit"], License: fields["License"]}, nil
}

func readSQLite(path string) (Component, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Component{}, err
	}
	m := sqliteVersion.FindSubmatch(data)
	if m == nil {
		return Component{}, errors.New("no SQLITE_VERSION")
	}
	return Component{Name: "sqlite", Upstream: "https://sqlite.org", Version: string(m[1]), License: "blessing"}, nil
}

func readSubmodules(root string, gitlink GitlinkFunc) ([]Component, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Component
	sections := strings.Split(string(data), "[submodule")[1:]
	for _, s := range sections {
		path, url := submodulePath.FindStringSubmatch(s), submoduleURL.FindStringSubmatch(s)
		if path == nil || url == nil {
			return nil, errors.New(".gitmodules: an entry lacks its path or url")
		}
		commit, err := gitlink(root, path[1])
		if err != nil {
			return nil, err
		}
		license, err := submoduleLicense(filepath.Join(root, path[1]))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path[1], err)
		}
		out = append(out, Component{Name: filepath.Base(path[1]), Upstream: url[1], Version: commit, Commit: commit, License: license})
	}
	return out, nil
}

// submoduleLicense reads the license from the submodule's package.json, or
// from its first package when the workspace root declares none.
func submoduleLicense(dir string) (string, error) {
	files := []string{filepath.Join(dir, "package.json")}
	more, err := filepath.Glob(filepath.Join(dir, "packages", "*", "package.json"))
	if err != nil {
		return "", err
	}
	sort.Strings(more)
	for _, f := range append(files, more...) {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var pkg struct {
			License string `json:"license"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.License != "" {
			return pkg.License, nil
		}
	}
	return "", errors.New("no license in its package.json")
}

type spdxPackage struct {
	Name             string `json:"name"`
	SPDXID           string `json:"SPDXID"`
	VersionInfo      string `json:"versionInfo"`
	DownloadLocation string `json:"downloadLocation"`
	FilesAnalyzed    bool   `json:"filesAnalyzed"`
	LicenseConcluded string `json:"licenseConcluded"`
	LicenseDeclared  string `json:"licenseDeclared"`
	CopyrightText    string `json:"copyrightText"`
	SourceInfo       string `json:"sourceInfo,omitempty"`
}

type spdxRelationship struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

type spdxDocument struct {
	SPDXVersion       string `json:"spdxVersion"`
	DataLicense       string `json:"dataLicense"`
	SPDXID            string `json:"SPDXID"`
	Name              string `json:"name"`
	DocumentNamespace string `json:"documentNamespace"`
	CreationInfo      struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	} `json:"creationInfo"`
	Packages      []spdxPackage      `json:"packages"`
	Relationships []spdxRelationship `json:"relationships"`
}

var spdxIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9.-]`)

// SPDX writes an SPDX 2.3 JSON document. Its only time is the one passed in
// (the commit date), so two builds of one commit write the same bytes.
func SPDX(product, version, created string, components []Component) ([]byte, error) {
	if _, err := time.Parse(time.RFC3339, created); err != nil {
		return nil, fmt.Errorf("creation time: %w", err)
	}
	const rootID = "SPDXRef-Package-" + "root"
	doc := spdxDocument{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              product + "-" + version,
		DocumentNamespace: "https://github.com/" + Repository + "/releases/" + version + "/sbom",
	}
	doc.CreationInfo.Created = created
	doc.CreationInfo.Creators = []string{"Tool: herdsman-releasetool"}
	doc.Packages = []spdxPackage{{
		Name: product, SPDXID: rootID, VersionInfo: version,
		DownloadLocation: "https://github.com/" + Repository,
		LicenseConcluded: "GPL-3.0-only", LicenseDeclared: "GPL-3.0-only", CopyrightText: "NOASSERTION",
	}}
	doc.Relationships = []spdxRelationship{{"SPDXRef-DOCUMENT", "DESCRIBES", rootID}}
	for _, c := range components {
		id := "SPDXRef-Package-" + spdxIDUnsafe.ReplaceAllString(c.Name, "-")
		p := spdxPackage{
			Name: c.Name, SPDXID: id, VersionInfo: c.Version, DownloadLocation: c.Upstream,
			LicenseConcluded: c.License, LicenseDeclared: c.License, CopyrightText: "NOASSERTION",
		}
		if c.Commit != "" {
			p.SourceInfo = "commit " + c.Commit
		}
		doc.Packages = append(doc.Packages, p)
		doc.Relationships = append(doc.Relationships, spdxRelationship{rootID, "CONTAINS", id})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// StaticLinkComponents are the C runtime parts zig links into the static linux
// binaries. Zig bundles them, so zig's version is what pins them.
func StaticLinkComponents(zig string) []Component {
	version := "bundled with zig " + zig
	return []Component{
		{Name: "musl", Upstream: "https://musl.libc.org", Version: version, License: "MIT"},
		{Name: "zig-compiler-rt", Upstream: "https://ziglang.org", Version: version, License: "MIT"},
	}
}

// ToolchainComponent is the Go standard library linked into every binary, at
// the version of the toolchain running this tool.
func ToolchainComponent() Component {
	return Component{Name: "go-stdlib", Upstream: "https://go.dev", Version: runtime.Version(), License: "BSD-3-Clause"}
}
