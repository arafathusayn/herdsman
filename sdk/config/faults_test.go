package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"google.golang.org/protobuf/encoding/protowire"
)

var errDisk = errors.New("disk failed")

// failingFile fails the one operation it names.
type failingFile struct {
	*os.File
	fail string
}

func (f failingFile) Chmod(mode os.FileMode) error {
	if f.fail == "chmod" {
		return errDisk
	}
	return f.File.Chmod(mode)
}

func (f failingFile) Write(data []byte) (int, error) {
	if f.fail == "write" {
		return 0, errDisk
	}
	return f.File.Write(data)
}

func (f failingFile) Close() error {
	err := f.File.Close()
	if f.fail == "close" {
		return errDisk
	}
	return err
}

func failDisk(t *testing.T, fail string) {
	t.Helper()
	oldTemp, oldNew := createTemp, openNew
	t.Cleanup(func() { createTemp, openNew = oldTemp, oldNew })
	createTemp = func(dir, pattern string) (writableFile, error) {
		file, err := os.CreateTemp(dir, pattern)
		return failingFile{file, fail}, err
	}
	openNew = func(path string) (writableFile, error) {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		return failingFile{file, fail}, err
	}
}

func TestADiskFailureLeavesTheFileAlone(t *testing.T) {
	const text = "[merge]\nland = \"pr\"\n"
	for _, fail := range []string{"chmod", "write", "close"} {
		t.Run(fail, func(t *testing.T) {
			path := configFile(t, text)
			failDisk(t, fail)
			if err := SetFile(path, "merge.land", "push"); !errors.Is(err, errDisk) {
				t.Fatalf("err = %v", err)
			}
			if data, _ := os.ReadFile(path); string(data) != text {
				t.Fatalf("the file changed:\n%s", data)
			}
		})
	}
}

func TestInitReportsADiskFailure(t *testing.T) {
	failDisk(t, "write")
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := InitFile(path, FormatTOML); !errors.Is(err, errDisk) {
		t.Fatalf("init: %v", err)
	}
	if _, err := InitDetected(filepath.Join(t.TempDir(), "d.toml"), FormatTOML, t.TempDir()); !errors.Is(err, errDisk) {
		t.Fatalf("detected: %v", err)
	}
}

func TestNilEnvironmentMeansTheProcessEnvironment(t *testing.T) {
	path := configFile(t, "[branch]\nname = \"a\"\n")
	t.Setenv(envConfigPath, path)
	t.Setenv("HOME", t.TempDir())
	got, err := Load(Options{UserDir: t.TempDir()})
	if err != nil || got.Config.GetBranch().GetName() != "a" {
		t.Fatalf("%v %v", got, err)
	}
	if found, err := ProjectFile(Options{}); err != nil || found != path {
		t.Fatalf("%q %v", found, err)
	}
	if _, _, err := UserFile(Options{UserDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestAppendRefusesAKeyHoldingSomethingElse(t *testing.T) {
	path := configFile(t, "[branch]\ntypes = \"one\"\n")
	if _, err := EditFile(path, "branch.types", "two", true); err == nil {
		t.Fatal("appended to a string")
	}
}

func TestReadBinaryWithAMissingParentMakesTheSection(t *testing.T) {
	body := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "branch")
	body = protowire.AppendString(protowire.AppendTag(body, 1, protowire.BytesType), "types")
	data := protowire.AppendBytes(protowire.AppendTag(nil, emptyListsField, protowire.BytesType), body)
	got, err := Load(Options{ConfigPath: writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)), UserDir: t.TempDir(), Environ: []string{}})
	if err != nil || len(got.Config.GetBranch().GetTypes()) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestClearInventedFormsLeavesAnEmptyCaseAlone(t *testing.T) {
	from, to := (&configv1.HookSpec{}).ProtoReflect(), (&configv1.HookSpec{}).ProtoReflect()
	clearInventedForms(from, to)
	if to.WhichOneof(to.Descriptor().Oneofs().Get(0)) != nil {
		t.Fatal("a case appeared")
	}
}

func TestBindRefusesTreesOfTheWrongShape(t *testing.T) {
	array := tree.NewArray()
	table := func(key string, value *tree.Node) *tree.Node {
		out := tree.NewTable()
		out.Set(key, value)
		return out
	}
	for name, root := range map[string]*tree.Node{
		"no field": table("nope", tree.NewString("x")),
		"list":     table("branch", table("types", tree.NewString("x"))),
		"map":      table("aliases", array),
	} {
		t.Run(name, func(t *testing.T) {
			if err := bindMessage(root, (&configv1.Config{}).ProtoReflect(), ""); err == nil {
				t.Fatal("bound")
			}
		})
	}
}

func TestSectionsCannotBeSetAsText(t *testing.T) {
	path := configFile(t, "")
	err := SetFile(path, "branch", "x")
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("err = %v", err)
	}
}

func TestNormalizeNamesTheFileWhenTheRootIsNotATable(t *testing.T) {
	_, err := normalize(tree.NewString("x"), configDescriptor(), "", false, &report{})
	if err == nil || !strings.Contains(err.Error(), "the file") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenamesSkipAMapOfScalars(t *testing.T) {
	fd := configDescriptor().Fields().ByName("aliases")
	if got := fieldRenames(tree.NewTable(), fd, nil, nil); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestASectionFromTwoSourcesIsMixed(t *testing.T) {
	result := &Result{Sources: map[string]string{"a.b": SourceProject, "a.c": SourceDefault}}
	if got := result.sourceOf("a"); got != "mixed" {
		t.Fatal(got)
	}
}

func TestPlainValueOfABool(t *testing.T) {
	if got := plainValue(tree.NewBool(true)); got != true {
		t.Fatal(got)
	}
}
