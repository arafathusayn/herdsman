package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestUnsetRemovesATableInsideAnInlineAncestor(t *testing.T) {
	for name, text := range map[string]string{
		"direct":   "[hooks]\npre_start = {one = \"echo x\"}\n",
		"ancestor": "hooks = {pre_start = {one = \"echo x\"}, pre_merge = \"echo y\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := configFile(t, text)
			removed, err := UnsetFile(path, "hooks.pre_start")
			if err != nil || !removed {
				t.Fatalf("removed %v, err %v", removed, err)
			}
			got, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config.GetHooks().GetPreStart() != nil {
				t.Fatal("the hook is still there")
			}
		})
	}
	path := configFile(t, "hooks = {pre_start = {one = \"echo x\"}, pre_merge = \"echo y\"}\n")
	if _, err := UnsetFile(path, "hooks.pre_start"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(path); err != nil || got.Config.GetHooks().GetPreMerge().GetCommand() != "echo y" {
		t.Fatalf("the other hook was lost: %v %v", got, err)
	}
}

func TestUpdateKeepsTheCommentAndPlaceOfAScalar(t *testing.T) {
	path := configFile(t, "# head\n[hooks]\npre-merge = \"echo x\" # why this hook exists\npost_merge = \"echo y\"\n")
	if err := UpdateFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pre_merge = \"echo x\" # why this hook exists\npost_merge") {
		t.Fatalf("got:\n%s", data)
	}
}

func TestReadFileKeepsWireFieldsItDoesNotDefine(t *testing.T) {
	future := []byte{0xa0, 0x06, 0x01}
	got, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(future)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Config.ProtoReflect().GetUnknown(), future) {
		t.Fatalf("unknown: %x", got.Config.ProtoReflect().GetUnknown())
	}
}

func TestBinaryExportDoesNotInventAHookForm(t *testing.T) {
	future := []byte{0xa0, 0x06, 0x01}
	bare := &configv1.HookSpec{}
	bare.ProtoReflect().SetUnknown(future)
	stage := &configv1.PipelineStage{}
	stage.ProtoReflect().SetUnknown(future)
	msg := &configv1.Config{Hooks: &configv1.Hooks{
		PreStart: bare,
		PreMerge: &configv1.HookSpec{Form: &configv1.HookSpec_Pipeline{Pipeline: &configv1.Pipeline{Stages: []*configv1.PipelineStage{stage}}}},
	}}
	before, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ExportFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(before)), FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	back := &configv1.Config{}
	if err := proto.Unmarshal(data, back); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(msg, back) {
		t.Fatalf("before %v\nafter %v", msg, back)
	}
}

func TestSettingAHookOverAFormlessOneKeepsTheSetting(t *testing.T) {
	bare := &configv1.HookSpec{}
	bare.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	before, err := proto.Marshal(&configv1.Config{Hooks: &configv1.Hooks{PreStart: bare}})
	if err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(before))
	if err := SetFile(path, "hooks.pre_start", "echo a"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || got.Config.GetHooks().GetPreStart().GetCommand() != "echo a" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestEmptyListSurvivesEveryFormatConversion(t *testing.T) {
	const text = "[branch]\ntypes = []\n[projects.\"a.b/c\".branch]\ntypes = []\n"
	effective := func(t *testing.T, path string) {
		t.Helper()
		got, err := Load(Options{ConfigPath: path, UserDir: t.TempDir(), Environ: []string{}, ProjectKey: "a.b/c"})
		if err != nil {
			t.Fatal(err)
		}
		if types := got.Config.GetBranch().GetTypes(); len(types) != 0 {
			t.Fatalf("defaults came back: %v", types)
		}
	}
	source := configFile(t, text)
	effective(t, source)
	for _, first := range []string{FormatTOML, FormatYAML, FormatJSON, FormatBin} {
		for _, second := range []string{FormatTOML, FormatYAML, FormatJSON, FormatBin} {
			t.Run(first+"-"+second, func(t *testing.T) {
				path := source
				for _, format := range []string{first, second} {
					data, err := ExportFile(path, format)
					if err != nil {
						t.Fatal(err)
					}
					path = writeFile(t, filepath.Join(t.TempDir(), "c."+format), string(data))
					effective(t, path)
				}
			})
		}
	}
}

func TestEmptyListInAListItemSurvivesBinary(t *testing.T) {
	path := configFile(t, "[[launch.prelude]]\nargv = []\n")
	data, err := ExportFile(path, FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ExportFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)), FormatTOML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(again), "argv = []") {
		t.Fatalf("got:\n%s", again)
	}
}

func TestEmptyListFieldIsReservedAndHiddenFromUnknownReports(t *testing.T) {
	if !configDescriptor().ReservedRanges().Has(emptyListsField) {
		t.Fatalf("field %d is not reserved in the contract", emptyListsField)
	}
	data, err := ExportFile(configFile(t, "[branch]\ntypes = []\n"), FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data)))
	if err != nil || len(got.Warnings) != 0 || len(got.Config.ProtoReflect().GetUnknown()) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestDamagedEmptyListRecordsAreRefused(t *testing.T) {
	record := func(segments ...string) []byte {
		var inner []byte
		for _, segment := range segments {
			inner = protowire.AppendTag(inner, 1, protowire.BytesType)
			inner = protowire.AppendString(inner, segment)
		}
		return protowire.AppendBytes(protowire.AppendTag(nil, emptyListsField, protowire.BytesType), inner)
	}
	withList, err := proto.Marshal(&configv1.Config{Branch: &configv1.Branch{Types: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	withItem, err := proto.Marshal(&configv1.Config{Launch: &configv1.Launch{Prelude: []*configv1.LaunchStep{{}}}})
	if err != nil {
		t.Fatal(err)
	}
	withName, err := proto.Marshal(&configv1.Config{Branch: &configv1.Branch{Name: proto.String("n")}})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"no path":          record(),
		"already set":      append(withList, record("branch", "types")...),
		"through a scalar": append(withList, record("branch", "types", "x")...),
		"bad index":        append(withItem, record("launch", "prelude", "7", "argv")...),
		"text index":       append(withItem, record("launch", "prelude", "x", "argv")...),
		"through a string": append(append([]byte{}, withName...), record("branch", "name", "x", "y")...),
		"cut tag":          protowire.AppendBytes(protowire.AppendTag(nil, emptyListsField, protowire.BytesType), []byte{0x80}),
		"wrong wire":       protowire.AppendVarint(protowire.AppendTag(nil, emptyListsField, protowire.VarintType), 1),
		"bad segment":      protowire.AppendBytes(protowire.AppendTag(nil, emptyListsField, protowire.BytesType), protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 1)),
		"cut segment":      protowire.AppendBytes(protowire.AppendTag(nil, emptyListsField, protowire.BytesType), []byte{0x0a, 0x05, 'a'}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadFile(writeFile(t, filepath.Join(t.TempDir(), "c.bin"), string(data))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
