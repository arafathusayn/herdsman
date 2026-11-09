package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/yaml"
)

func TestFileInAndReadHooks(t *testing.T) {
	dir := t.TempDir()
	if path, err := FileIn(dir); err != nil || path != "" {
		t.Fatalf("%q %v", path, err)
	}
	written := writeFile(t, filepath.Join(dir, "herdsman.config.toml"), "[hooks]\npre-start = \"a\"\n")
	path, err := FileIn(dir)
	if err != nil || path != written {
		t.Fatalf("%q %v", path, err)
	}
	table, err := ReadHooks(path)
	if err != nil || table["pre-start"] != "a" {
		t.Fatalf("%v %v", table, err)
	}
	if table, err := ReadHooks(configFile(t, "")); err != nil || table != nil {
		t.Fatalf("%v %v", table, err)
	}
	other := configFile(t, "[commit]\nstage = \"none\"\n[hooks]\npre-start = \"a\"\n")
	if table, err := ReadHooks(other); err != nil || table["pre-start"] != "a" {
		t.Fatalf("another section stopped the hooks: %v %v", table, err)
	}
	if _, err := ReadHooks(filepath.Join(dir, "gone.toml")); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if _, err := ReadHooks(writeFile(t, filepath.Join(t.TempDir(), "c.json"), "[1]")); err == nil {
		t.Fatal("a list was accepted as a file")
	}
	if _, err := ReadHooks(configFile(t, "hooks = 1\n")); err == nil {
		t.Fatal("a number was accepted as the hooks section")
	}
}

func TestProjectFileReportsDiscovery(t *testing.T) {
	root := t.TempDir()
	if path, err := ProjectFile(Options{RepoRoot: root, Environ: []string{}}); err != nil || path != "" {
		t.Fatalf("%q %v", path, err)
	}
	written := writeFile(t, filepath.Join(root, "herdsman.config.yaml"), "")
	if path, err := ProjectFile(Options{RepoRoot: root, Environ: []string{}}); err != nil || path != written {
		t.Fatalf("%q %v", path, err)
	}
	if _, err := ProjectFile(Options{ConfigPath: filepath.Join(root, "missing.toml"), Environ: []string{}}); err == nil {
		t.Fatal("a missing named file was accepted")
	}
}

func TestWriteAtomicFailures(t *testing.T) {
	if err := writeAtomic(filepath.Join(t.TempDir(), "no", "dir"), nil); err == nil {
		t.Fatal("wrote into a missing folder")
	}
	target := filepath.Join(t.TempDir(), "held")
	if err := os.MkdirAll(filepath.Join(target, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(target, []byte("x")); err == nil {
		t.Fatal("replaced a folder")
	}
}

func TestCheckEditRefusesWhatWouldNotLoad(t *testing.T) {
	if err := checkEdit(FormatTOML, []byte("not toml")); err == nil {
		t.Fatal("broken text accepted")
	}
	if err := checkEdit(FormatTOML, []byte("[merge]\nland = 1\n")); err == nil {
		t.Fatal("a wrong type accepted")
	}
}

func TestEditYAMLPassesFailuresThrough(t *testing.T) {
	if _, err := editYAML([]byte("a: ["), nil, nil); err == nil {
		t.Fatal("broken text accepted")
	}
	boom := errors.New("boom")
	edit := func(*yaml.Document) error { return boom }
	if _, err := editYAML([]byte("a: 1\n"), edit, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestDetectedPropagatesAnUnreadableRoot(t *testing.T) {
	file := writeFile(t, filepath.Join(t.TempDir(), "plain"), "")
	if _, err := InitDetected(filepath.Join(t.TempDir(), "c.toml"), FormatTOML, file); err == nil {
		t.Fatal("a file was accepted as a repository")
	}
	opts := isolated(t)
	opts.RepoRoot = file
	if _, err := Load(opts); err == nil {
		t.Fatal("a file was accepted as a repository")
	}
}

func TestInitFileFailures(t *testing.T) {
	if err := InitFile(filepath.Join(t.TempDir(), "no", "c.toml"), FormatTOML); err == nil {
		t.Fatal("wrote into a missing folder")
	}
}

func TestUpdateRewritesBinaryAndJSON(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "c.bin")
	data, err := ExportFile(configFile(t, "[schema]\nversion = \"0.9.0\"\n"), FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, binary, string(data))
	if err := UpdateFile(binary); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(binary)
	if err != nil || got.Config.GetSchema().GetVersion() != SchemaVersion {
		t.Fatalf("%v %v", got, err)
	}
	if err := UpdateFile(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("a missing file was accepted")
	}
	broken := writeFile(t, filepath.Join(t.TempDir(), "c.toml"), "[policy]\nmax_agnets = 1\n[hooks]\npre-merge = \"a\"\n")
	if err := UpdateFile(broken); err == nil {
		t.Fatal("updated a file that cannot load")
	}
}

func TestEditsRefuseAFileThatIsNotReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.ini")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateFile(path); err == nil {
		t.Fatal("an unknown format was accepted")
	}
	if _, err := UnsetFile(filepath.Join(t.TempDir(), "gone.toml"), "merge.land"); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if _, err := ExportFile(filepath.Join(t.TempDir(), "gone.toml"), FormatJSON); err == nil {
		t.Fatal("a missing file was accepted")
	}
	if _, err := ExportFile(configFile(t, ""), "ini"); err == nil {
		t.Fatal("an unknown target was accepted")
	}
}

func TestUnsetAcrossYAMLAndJSONDetails(t *testing.T) {
	yamlFile := writeFile(t, filepath.Join(t.TempDir(), "c.yaml"), "{merge: {land: pr}}\n")
	if removed, err := UnsetFile(yamlFile, "merge.land"); err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	if removed, err := UnsetFile(yamlFile, "merge.land"); err != nil || removed {
		t.Fatalf("second: removed %v, err %v", removed, err)
	}
	jsonFile := writeFile(t, filepath.Join(t.TempDir(), "c.json"), `{"merge":{"land":"pr"}}`)
	if removed, err := UnsetFile(jsonFile, "merge.land"); err != nil || !removed {
		t.Fatalf("removed %v, err %v", removed, err)
	}
	updated := writeFile(t, filepath.Join(t.TempDir(), "c.yaml"), "{hooks: {pre-merge: a}}\n")
	if err := UpdateFile(updated); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(updated); !strings.Contains(string(data), "pre_merge") {
		t.Fatalf("file:\n%s", data)
	}
}

func TestSetPathAndRenderingEdges(t *testing.T) {
	root := tree.NewTable()
	root.Set("a", tree.NewInt(1))
	if err := setPath(root, []string{"a", "b"}, tree.NewInt(2)); err == nil {
		t.Fatal("went through a value")
	}
	if got := renderNode(tree.NewFloat(1.5)); got != "1.5" {
		t.Fatalf("%q", got)
	}
	if got := renderNode(tree.NewNull()); got != "null" {
		t.Fatalf("%q", got)
	}
	if got := renderNode(tree.NewBool(true)); got != "true" {
		t.Fatalf("%q", got)
	}
}

func TestRemoteWithABadURL(t *testing.T) {
	if got := RepositoryKey("https://exa mple.com/a/b"); got != "" {
		t.Fatalf("%q", got)
	}
	if got := RepositoryKey("host/a:b"); got != "" {
		t.Fatalf("%q", got)
	}
}
