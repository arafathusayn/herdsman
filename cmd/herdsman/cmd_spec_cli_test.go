package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

func TestSpecCLIMatchesGolden(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSpec([]string{"cli"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	golden, err := os.ReadFile("../../sdk/conformance/cli/testdata/spec.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), golden) {
		t.Fatal("spec cli differs from sdk/conformance/cli/testdata/spec.golden.json: run make proto")
	}
}

func TestCommandSpecReadsTheHelpText(t *testing.T) {
	got := commandSpec(command{
		name:    "demo",
		summary: "One line.",
		help:    "usage: herdsman demo <path> [--plain] --limit <n>\n\nSee --limit.\n\nexit codes:\n  0  done\n  N   the child's code\n  2  usage error\n    continued\n\nmore text\n",
		subHelp: map[string]string{"zeta": "usage: herdsman demo zeta\n", "alpha": "usage: herdsman demo alpha <id>\n\nexit codes:\n  3  absent\n"},
	})
	if got.GetName() != "demo" || got.GetSummary() != "One line." {
		t.Fatalf("spec %v", got)
	}
	if len(got.GetUsage()) != 1 || got.GetUsage()[0] != "herdsman demo <path> [--plain] --limit <n>" {
		t.Errorf("usage %q", got.GetUsage())
	}
	if len(got.GetPositionals()) != 1 || got.GetPositionals()[0].GetName() != "path" {
		t.Errorf("positionals %v", got.GetPositionals())
	}
	flags := got.GetFlags()
	if len(flags) != 2 || flags[0].GetName() != "--limit" || flags[0].GetValueName() != "n" || flags[1].GetName() != "--plain" || flags[1].GetValueName() != "" {
		t.Errorf("flags %v", flags)
	}
	codes := got.GetExitCodes()
	if len(codes) != 3 || codes[0].GetCode() != "0" || codes[1].GetCode() != "N" || codes[2].GetCode() != "2" || codes[2].GetMeaning() != "usage error continued" {
		t.Errorf("exit codes %v", codes)
	}
	subs := got.GetSubcommands()
	if len(subs) != 2 || subs[0].GetName() != "alpha" || subs[1].GetName() != "zeta" {
		t.Fatalf("subcommands %v", subs)
	}
	if len(subs[0].GetExitCodes()) != 1 || subs[0].GetPositionals()[0].GetName() != "id" {
		t.Errorf("subcommand %v", subs[0])
	}
}

func TestCommandSpecWithoutHelpKeepsTheSummary(t *testing.T) {
	got := commandSpec(command{name: "bare", summary: "Only this."})
	if got.GetHelp() != "" || len(got.GetUsage()) != 0 || len(got.GetFlags()) != 0 {
		t.Fatalf("spec %v", got)
	}
}

func TestCommandSpecReadsShortFlagsAndChoices(t *testing.T) {
	got := specFromHelp("demo", "usage: herdsman demo [-m <message>] [--format text|json-lines] [-v]\n\nThen pipe it to jq -r for scripts.\n\nExit codes:\n  0  done\n")
	if len(got.GetPositionals()) != 0 {
		t.Errorf("positionals %v", got.GetPositionals())
	}
	flags := got.GetFlags()
	if len(flags) != 3 {
		t.Fatalf("flags %v", flags)
	}
	if flags[0].GetName() != "--format" || flags[0].GetValueName() != "text|json-lines" {
		t.Errorf("choice flag %v", flags[0])
	}
	if flags[1].GetName() != "-m" || flags[1].GetShort() != "-m" || flags[1].GetValueName() != "message" {
		t.Errorf("short flag with a value %v", flags[1])
	}
	if flags[2].GetName() != "-v" || flags[2].GetShort() != "-v" || flags[2].GetValueName() != "" {
		t.Errorf("short switch %v", flags[2])
	}
	if len(got.GetExitCodes()) != 1 || got.GetExitCodes()[0].GetMeaning() != "done" {
		t.Errorf("capitalized table %v", got.GetExitCodes())
	}
}

func TestSpecCLIListsEveryExecutableCommand(t *testing.T) {
	var out bytes.Buffer
	if err := writeSpecCLI(&out); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Commands []struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	listed := map[string]int{}
	for _, c := range document.Commands {
		listed[c.Name]++
		if c.Summary == "" {
			t.Errorf("%s has no summary", c.Name)
		}
	}
	want := []string{"help", "version", "web"}
	for name := range commands {
		want = append(want, name)
	}
	for _, name := range want {
		if listed[name] != 1 {
			t.Errorf("%s is listed %d times", name, listed[name])
		}
	}
	// The dispatcher answers these before it looks at the registered commands.
	for _, name := range []string{"help", "version"} {
		if err := run([]string{name}, io.Discard, io.Discard); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken") }

func TestWriteSpecCLIReportsAWriteFailure(t *testing.T) {
	if err := writeSpecCLI(brokenWriter{}); err == nil {
		t.Fatal("no error")
	}
}

// Only these have nothing to declare: help and version take no arguments, and
// message is described by its subcommands.
var specWithoutUsage = map[string]bool{"help": true, "version": true, "message": true}

func TestSpecCLIDescribesEveryCommandsInterface(t *testing.T) {
	all := append([]command{}, builtinCommands...)
	for _, c := range commands {
		all = append(all, c)
	}
	for _, c := range all {
		spec := commandSpec(c)
		if specWithoutUsage[c.name] {
			if len(spec.GetUsage()) != 0 {
				t.Errorf("%s now has usage: take it off the list", c.name)
			}
			continue
		}
		if len(spec.GetUsage()) == 0 {
			t.Errorf("%s has no usage in the spec", c.name)
		}
	}
}

func TestSpecDeclaresTheWebFlags(t *testing.T) {
	flags := map[string][2]string{}
	spec := specFromHelp("web", webHelp)
	for _, f := range spec.GetFlags() {
		flags[f.GetName()] = [2]string{f.GetValueName(), f.GetDefaultValue()}
	}
	want := map[string][2]string{
		"--port": {"n", "7878"}, "--host": {"address", "127.0.0.1"}, "--no-qr": {}, "--session": {"name"}, "--open": {},
		"--inbox": {"folder"}, "--client": {"url"}, "--public-url": {"url"}, "--route": {"folder"}, "--allow-host": {"host"},
	}
	if len(flags) != len(want) {
		t.Errorf("flags %v", flags)
	}
	for name, values := range want {
		if flags[name] != values {
			t.Errorf("%s: %q, want %q", name, flags[name], values)
		}
	}
	if len(spec.GetExitCodes()) == 0 {
		t.Error("no exit codes")
	}
}

func TestSpecReadsADeclaredDefault(t *testing.T) {
	flags := specFromHelp("demo", "usage: herdsman demo [--limit <n>] [--mode <m>] [--socket <p>]\n\n  --limit <n>  how many (default 10)\n  --mode <m>   which (default: from MODE)\n  --socket <p> where (default found by search)\n\nSee --limit (default 99).\n").GetFlags()
	got := map[string]string{}
	for _, f := range flags {
		got[f.GetName()] = f.GetDefaultValue()
	}
	if got["--limit"] != "10" || got["--mode"] != "" || got["--socket"] != "" {
		t.Fatalf("defaults %v", got)
	}
}

func TestWebHelpIsTheDeclaredText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWeb([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != webHelp {
		t.Fatalf("help %q", stdout.String())
	}
}
