package main

import (
	"bytes"
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
