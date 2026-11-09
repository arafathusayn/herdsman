package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestExportKeepsAnExplicitEmptyListInTextFormats(t *testing.T) {
	for _, format := range []string{FormatTOML, FormatYAML, FormatJSON} {
		t.Run(format, func(t *testing.T) {
			source := configFile(t, "[branch]\ntypes = []\n")
			data, err := ExportFile(source, format)
			if err != nil {
				t.Fatal(err)
			}
			opts := isolated(t)
			opts.ConfigPath = writeFile(t, filepath.Join(t.TempDir(), "c."+format), string(data))
			got, err := Load(opts)
			if err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			if types := got.Config.GetBranch().GetTypes(); len(types) != 0 {
				t.Fatalf("defaults came back: %v\n%s", types, data)
			}
		})
	}
}

func TestBinaryHoldsAnEmptyListInEveryPlace(t *testing.T) {
	for name, tc := range map[string]struct{ text, key string }{
		"section": {"[branch]\ntypes = []\n", "branch.types"},
		"step":    {"[[launch.prelude]]\nargv = []\n", "launch.prelude[0].argv"},
		"project": {"[projects.a.branch]\ntypes = []\n", "projects.a.branch.types"},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := ExportFile(configFile(t, tc.text), FormatBin)
			if err != nil {
				t.Fatal(err)
			}
			again, err := ExportFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)), FormatTOML)
			if err != nil || !strings.Contains(string(again), "= []") {
				t.Fatalf("%s: %v\n%s", tc.key, err, again)
			}
		})
	}
}

func TestBinaryKeepsAnEmptyPipelineHook(t *testing.T) {
	data, err := ExportFile(configFile(t, "[hooks]\npre-start = []\n"), FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Config.GetHooks().GetPreStart().GetForm().(*configv1.HookSpec_Pipeline); !ok {
		t.Fatalf("hooks: %v", got.Config.GetHooks())
	}
}

func TestSettingAnEmptyListOnABinaryFileKeepsIt(t *testing.T) {
	original, err := encodeBin(&configv1.Config{Branch: &configv1.Branch{Types: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(original))
	if err := SetFile(path, "branch.types", ""); err != nil {
		t.Fatal(err)
	}
	got, err := Load(Options{ConfigPath: path, UserDir: t.TempDir(), Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if types := got.Config.GetBranch().GetTypes(); len(types) != 0 {
		t.Fatalf("types: %v", types)
	}
}

// futureField is a field number no message here defines, with a varint value.
const futureField = protowire.Number(1000)

func withFuture(b []byte, value uint64) []byte {
	b = protowire.AppendTag(b, futureField, protowire.VarintType)
	return protowire.AppendVarint(b, value)
}

// futureFile writes a binary file with an unknown field at the top and inside
// the merge section.
func futureFile(t *testing.T) string {
	t.Helper()
	msg := &configv1.Config{
		Merge:  &configv1.Merge{Verify: proto.Bool(true)},
		Branch: &configv1.Branch{Name: proto.String("kept")},
	}
	msg.ProtoReflect().SetUnknown(withFuture(nil, 7))
	msg.GetMerge().ProtoReflect().SetUnknown(withFuture(nil, 8))
	data, err := encodeBin(msg)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data))
}

func readBinary(t *testing.T, data []byte) *configv1.Config {
	t.Helper()
	msg := &configv1.Config{}
	if err := proto.Unmarshal(data, msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func assertFutureKept(t *testing.T, msg *configv1.Config) {
	t.Helper()
	if !bytes.Equal(msg.ProtoReflect().GetUnknown(), withFuture(nil, 7)) {
		t.Fatalf("top-level unknown data: %x", msg.ProtoReflect().GetUnknown())
	}
	if !bytes.Equal(msg.GetMerge().ProtoReflect().GetUnknown(), withFuture(nil, 8)) {
		t.Fatalf("nested unknown data: %x", msg.GetMerge().ProtoReflect().GetUnknown())
	}
}

func TestBinaryExportKeepsUnknownData(t *testing.T) {
	data, err := ExportFile(futureFile(t), FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	assertFutureKept(t, readBinary(t, data))
}

func TestTextExportRefusesUnknownBinaryData(t *testing.T) {
	path := futureFile(t)
	for _, format := range []string{FormatTOML, FormatYAML, FormatJSON} {
		if _, err := ExportFile(path, format); err == nil || !strings.Contains(err.Error(), "1000") {
			t.Fatalf("%s: err = %v", format, err)
		}
	}
}

func TestBinaryEditsKeepUnknownData(t *testing.T) {
	path := futureFile(t)
	if err := SetFile(path, "merge.strategy", "rebase"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := readBinary(t, data)
	assertFutureKept(t, msg)
	if msg.GetMerge().GetStrategy() != configv1.MergeStrategy_MERGE_STRATEGY_REBASE {
		t.Fatalf("merge: %v", msg.GetMerge())
	}
	if removed, err := UnsetFile(path, "branch.name"); err != nil || !removed {
		t.Fatalf("unset: %v %v", removed, err)
	}
	if data, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	assertFutureKept(t, readBinary(t, data))
}

func TestUnknownBinaryDataWarnsAndStrictStops(t *testing.T) {
	path := futureFile(t)
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 2 || got.Warnings[0].Key != "#1000" || got.Warnings[1].Key != "merge.#1000" {
		t.Fatalf("warnings: %v", got.Warnings)
	}
	opts := isolated(t)
	opts.ConfigPath = path
	opts.Strict = true
	if _, err := Load(opts); err == nil {
		t.Fatal("strict accepted unknown binary data")
	}
}

func TestUnknownBinaryDataInASafetySectionStops(t *testing.T) {
	msg := &configv1.Config{Policy: &configv1.Policy{MaxAgents: proto.Int32(1)}}
	msg.GetPolicy().ProtoReflect().SetUnknown(withFuture(nil, 1))
	data, err := encodeBin(msg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)))
	var strict *StrictError
	if !errors.As(err, &strict) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownBinaryDataFoundInListsAndMaps(t *testing.T) {
	msg := &configv1.Config{
		Launch:   &configv1.Launch{Prelude: []*configv1.LaunchStep{{Argv: []string{"a"}}}},
		Projects: map[string]*configv1.Config{"p": {Branch: &configv1.Branch{Name: proto.String("n")}}},
	}
	msg.GetProjects()["p"].GetBranch().ProtoReflect().SetUnknown(withFuture(nil, 2))
	found := unknownFields(msg.ProtoReflect(), "")
	if len(found) != 1 || found[0] != "projects.p.branch.#1000" {
		t.Fatalf("found: %v", found)
	}
	msg.GetLaunch().GetPrelude()[0].ProtoReflect().SetUnknown(withFuture(nil, 3))
	found = unknownFields(msg.ProtoReflect(), "")
	if len(found) != 2 || found[0] != "launch.prelude[0].#1000" {
		t.Fatalf("found: %v", found)
	}
}

func TestCarryUnknownFollowsListsAndMaps(t *testing.T) {
	from := &configv1.Config{
		Launch:   &configv1.Launch{Prelude: []*configv1.LaunchStep{{Argv: []string{"a"}}}},
		Projects: map[string]*configv1.Config{"p": {}, "gone": {}},
	}
	from.GetLaunch().GetPrelude()[0].ProtoReflect().SetUnknown(withFuture(nil, 3))
	from.GetProjects()["p"].ProtoReflect().SetUnknown(withFuture(nil, 4))
	to := &configv1.Config{
		Launch:   &configv1.Launch{Prelude: []*configv1.LaunchStep{{Argv: []string{"a"}}}},
		Projects: map[string]*configv1.Config{"p": {}},
	}
	carryUnknown(from.ProtoReflect(), to.ProtoReflect())
	if len(to.GetLaunch().GetPrelude()[0].ProtoReflect().GetUnknown()) == 0 {
		t.Fatal("list element lost its unknown data")
	}
	if len(to.GetProjects()["p"].ProtoReflect().GetUnknown()) == 0 {
		t.Fatal("map value lost its unknown data")
	}
}
