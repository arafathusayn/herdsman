package docgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/arafathusayn/herdsman/proto"
	sdkconfig "github.com/arafathusayn/herdsman/sdk/config"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const cliFixture = `{"contract":"1.2.3","commands":[
 {"name":"alpha","summary":"First verb.","help":"usage: herdsman alpha\n\ntext\n","subcommands":[{"name":"go","help":"usage: herdsman alpha go\n"}]},
 {"name":"bare","summary":"No help text."},
 {"name":"fence","summary":"Help with a fence.","help":"before\n` + "```" + `\nafter\n"}
]}`

func TestCommandPagesGiveEveryVerbAPage(t *testing.T) {
	pages, err := CommandPages([]byte(cliFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha.md", "bare.md", "fence.md", "index.md"} {
		if pages[name] == "" {
			t.Errorf("no page %s in %v", name, pages)
		}
	}
	if len(pages) != 4 {
		t.Errorf("got %d pages, want 4", len(pages))
	}
	alpha := pages["alpha.md"]
	for _, want := range []string{"# herdsman alpha\n", "First verb.", "```text\nusage: herdsman alpha\n\ntext\n```", "## herdsman alpha go\n", "usage: herdsman alpha go\n```"} {
		if !strings.Contains(alpha, want) {
			t.Errorf("alpha page lacks %q:\n%s", want, alpha)
		}
	}
	if strings.Contains(pages["bare.md"], "```") {
		t.Errorf("a verb without help has an empty block:\n%s", pages["bare.md"])
	}
	if !strings.Contains(pages["fence.md"], "````text\nbefore\n```\nafter\n````") {
		t.Errorf("help holding a fence breaks out of its block:\n%s", pages["fence.md"])
	}
	for _, want := range []string{"[`alpha`](alpha.md): First verb.", "[`bare`](bare.md): No help text."} {
		if !strings.Contains(pages["index.md"], want) {
			t.Errorf("index lacks %q:\n%s", want, pages["index.md"])
		}
	}
}

func TestCommandPagesRefuseBadInput(t *testing.T) {
	for name, input := range map[string]string{
		"not json":     "{",
		"path in name": `{"commands":[{"name":"../evil"}]}`,
		"reserved":     `{"commands":[{"name":"index"}]}`,
		"no commands":  `{"commands":[]}`,
	} {
		if _, err := CommandPages([]byte(input)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestProtoCommentsFollowDeclarations(t *testing.T) {
	got := protoComments(`// file: a.proto
syntax = "proto3";
package x.v1;

// Top says what it is.
// It has two lines.
message Top {
  // A field.
  optional string a_b = 1;

  // Detached, then a gap.

  string none = 2;
  map<string, string> env = 3; // trailing is ignored
  message Inner {
    // Deep.
    repeated int32 deep = 1;
  }
  oneof form {
    // In a oneof.
    Inner inner = 4;
  }
}
message Empty {}
// Kind picks.
enum Kind {
  // Not a field.
  KIND_A = 0;
}
// file: b.proto
package y.v1;
// Other.
message Top {
}
`)
	want := map[string]string{
		"x.v1.Top":            "Top says what it is. It has two lines.",
		"x.v1.Top.a_b":        "A field.",
		"x.v1.Top.Inner":      "",
		"x.v1.Top.Inner.deep": "Deep.",
		"x.v1.Top.inner":      "In a oneof.",
		"x.v1.Kind":           "Kind picks.",
		"y.v1.Top":            "Other.",
	}
	for key, text := range want {
		if got[key] != text {
			t.Errorf("%s = %q, want %q", key, got[key], text)
		}
	}
	for _, key := range []string{"x.v1.Top.none", "x.v1.Top.env", "x.v1.Empty", "x.v1.Kind.KIND_A"} {
		if got[key] != "" {
			t.Errorf("%s = %q, want nothing", key, got[key])
		}
	}
}

func realConfigPage(t *testing.T) string {
	t.Helper()
	set, err := sdkconfig.DescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	source, err := contracts.Files.ReadFile("herdsman/config/v1/config.proto")
	if err != nil {
		t.Fatal(err)
	}
	page, err := ConfigPage(set, string(source))
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestConfigPageCoversTheContract(t *testing.T) {
	page := realConfigPage(t)
	for _, want := range []string{
		"# Configuration\n",
		"## [aliases]\n",
		"## [branch]\n",
		"Branch controls how task branches are named.",
		"| `name` | string | A template for the branch name. |",
		"| `types` | array of string | The change types a branch name may start with. |",
		"### [commit.generation]\n",
		"## [projects.<key>]\n",
		"| `post_clean` | string, table or array |",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(page, "[projects.<key>"); n != 1 {
		t.Errorf("the recursive section appears %d times, want 1", n)
	}
}

// The page must spell every choice the way a file does, so each enum list in
// the schema of the text formats has to appear on it.
func TestConfigPageSpellsEnumsLikeTheSchema(t *testing.T) {
	data, err := sdkconfig.JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	page := realConfigPage(t)
	found := 0
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if choices, ok := v["enum"].([]any); ok {
				parts := make([]string, len(choices))
				for i, c := range choices {
					parts[i] = "`" + c.(string) + "`"
				}
				found++
				if want := strings.Join(parts, ` \| `); !strings.Contains(page, want) {
					t.Errorf("page lacks choices %s", want)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(schema)
	if found == 0 {
		t.Fatal("the schema had no enums to compare")
	}
}

func TestConfigPageRefusesBadInput(t *testing.T) {
	if _, err := ConfigPage([]byte("not a descriptor"), ""); err == nil {
		t.Error("garbage: no error")
	}
	if _, err := ConfigPage(nil, ""); err == nil {
		t.Error("an empty set has no Config message: no error")
	}
}

func TestWriteReplacesStalePages(t *testing.T) {
	dir := t.TempDir()
	commands := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commands, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gone.md", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(commands, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(dir, map[string]string{"alpha.md": "page\n"}, "config\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(commands, "gone.md")); !os.IsNotExist(err) {
		t.Errorf("a removed verb's page stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(commands, "keep.txt")); err != nil {
		t.Errorf("a file that is no page was removed: %v", err)
	}
	for path, want := range map[string]string{"commands/alpha.md": "page\n", "config.md": "config\n"} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
}

func TestWriteReportsAMissingDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(file, map[string]string{"a.md": "x"}, "y"); err == nil {
		t.Error("no error for a parent that is a file")
	}
}

func marshalSet(t *testing.T, files ...*descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func configFile() *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{
		Name:    proto.String("c.proto"),
		Package: proto.String("herdsman.config.v1"),
		Syntax:  proto.String("proto3"),
	}
}

func TestConfigPageRefusesAnUnusableSet(t *testing.T) {
	broken := configFile()
	broken.Dependency = []string{"missing.proto"}
	if _, err := ConfigPage(marshalSet(t, broken), ""); err == nil {
		t.Error("an unresolved import: no error")
	}
	notMessage := configFile()
	notMessage.EnumType = []*descriptorpb.EnumDescriptorProto{{
		Name:  proto.String("Config"),
		Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("CONFIG_NONE"), Number: proto.Int32(0)}},
	}}
	if _, err := ConfigPage(marshalSet(t, notMessage), ""); err == nil {
		t.Error("Config as an enum: no error")
	}
}

// The real contract has no numeric keys and comments every map of tables, so
// those paths need a contract of their own.
func TestConfigPageNumbersAndUncommentedMaps(t *testing.T) {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	message := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	file := configFile()
	file.MessageType = []*descriptorpb.DescriptorProto{
		{
			Name: proto.String("Config"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("limits"), Number: proto.Int32(1), Label: optional, Type: message, TypeName: proto.String(".herdsman.config.v1.Limits")},
				{Name: proto.String("named"), Number: proto.Int32(2), Label: repeated, Type: message, TypeName: proto.String(".herdsman.config.v1.Config.NamedEntry")},
			},
			NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("NamedEntry"),
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("key"), Number: proto.Int32(1), Label: optional, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("value"), Number: proto.Int32(2), Label: optional, Type: message, TypeName: proto.String(".herdsman.config.v1.Limits")},
				},
			}},
		},
		{
			Name: proto.String("Limits"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("ratio"), Number: proto.Int32(1), Label: optional, Type: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE.Enum()},
				{Name: proto.String("count"), Number: proto.Int32(2), Label: optional, Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
			},
		},
	}
	source := "package herdsman.config.v1;\n// Limits text.\nmessage Limits {\n}\n"
	page, err := ConfigPage(marshalSet(t, file), source)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| `ratio` | number |", "| `count` | integer |", "## [named.<key>]\n\nLimits text.\n"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
}

func TestWriteHandlesBracketsAndFolders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a[b")
	folder := filepath.Join(dir, "commands", "sub.md")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, map[string]string{"alpha.md": "x"}, "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Errorf("a folder was treated as a page: %v", err)
	}
}

func TestWriteReportsFilesystemErrors(t *testing.T) {
	cases := map[string]struct {
		mode  os.FileMode
		stale bool
	}{
		"unreadable folder":   {mode: 0o000},
		"cannot remove stale": {mode: 0o555, stale: true},
		"cannot write page":   {mode: 0o555},
	}
	for name, c := range cases {
		dir := t.TempDir()
		commands := filepath.Join(dir, "commands")
		if err := os.MkdirAll(commands, 0o755); err != nil {
			t.Fatal(err)
		}
		if c.stale {
			if err := os.WriteFile(filepath.Join(commands, "gone.md"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(commands, c.mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(commands, 0o755) })
		if err := Write(dir, map[string]string{"alpha.md": "x"}, "y"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
