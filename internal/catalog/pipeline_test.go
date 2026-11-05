package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable shell script named name into dir.
func fakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fixturePath(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A29: one pipeline for `herdsman models` and the web: herdr's kinds, kept
// when installed, each asked for its own list (a models subcommand
// included); a list that cannot be read is "unavailable: <reason>" with no
// models, never a guess. Real programs (shell scripts) on a temp PATH.
func TestPipelineWithFakeBinaries(t *testing.T) {
	bin := t.TempDir()
	web := "../web/catalogdata/"
	fakeBin(t, bin, "herdr", `echo "  <KIND>  [possible values: claude, codex, opencode, agy, gemini, pi]"`)
	fakeBin(t, bin, "claude", `[ "$1" = --version ] && { echo "claude 9.9"; exit 0; }; cat '`+fixturePath(t, web+"claude-help.txt")+`'`)
	fakeBin(t, bin, "codex", `[ "$1" = --version ] && { echo "codex 1.0"; exit 0; }; echo "not json"`)
	fakeBin(t, bin, "opencode", `case "$1" in --help) cat '`+fixturePath(t, "catalogdata/opencode-help.txt")+`';; models) printf 'prov-a/model-a\nprov-b/model-b\n\n';; --version) echo 0.1;; esac`)
	fakeBin(t, bin, "agy", `case "$1" in --help) cat '`+fixturePath(t, "catalogdata/agy-help.txt")+`';; models) echo "auth needed" >&2; exit 3;; esac`)
	fakeBin(t, bin, "gemini", `[ "$1" = --help ] && cat '`+fixturePath(t, "catalogdata/gemini-help.txt")+`'`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin") // the scripts use cat

	got := Pipeline(context.Background(), ExecRunner, "")
	byKind := map[string]Kind{}
	var order []string
	for _, k := range got {
		byKind[k.Kind] = k
		order = append(order, k.Kind)
		if k.Models == nil {
			t.Errorf("%s: models null", k.Kind)
		}
		if k.FreeModel {
			t.Errorf("%s: a guessed free model", k.Kind)
		}
	}
	if strings.Join(order, ",") != "claude,codex,opencode,agy,gemini" {
		t.Fatalf("kinds %v (pi is not installed)", order)
	}
	if c := byKind["claude"]; c.Unavailable != "" || len(c.Models) == 0 || c.Version != "claude 9.9" {
		t.Errorf("claude %+v", c)
	}
	if c := byKind["codex"]; !c.Offline || len(c.Models) != 0 || !strings.HasPrefix(c.Unavailable, "codex debug models: ") {
		t.Errorf("malformed codex %+v", c)
	}
	o := byKind["opencode"]
	if o.Unavailable != "" || len(o.Models) != 2 || o.Models[0].ID != "prov-a/model-a" || o.Models[1].ID != "prov-b/model-b" {
		t.Errorf("opencode %+v", o)
	}
	if a := byKind["agy"]; !a.Offline || len(a.Models) != 0 || !strings.HasPrefix(a.Unavailable, "agy models: ") || a.EffortFlag != "--effort" {
		t.Errorf("failed agy %+v", a)
	}
	if g := byKind["gemini"]; !g.Offline || g.Unavailable != "gemini lists no models" {
		t.Errorf("gemini %+v", g)
	}

	// One named kind skips herdr; herdr failing leaves the probed harnesses.
	if one := Pipeline(context.Background(), ExecRunner, "opencode"); len(one) != 1 || one[0].Kind != "opencode" {
		t.Fatalf("one %+v", one)
	}
	fakeBin(t, bin, "herdr", `exit 1`)
	got = Pipeline(context.Background(), ExecRunner, "")
	if len(got) != 2 || got[0].Kind != "claude" || got[1].Kind != "codex" {
		t.Fatalf("without herdr %+v", got)
	}
	if !strings.HasPrefix(HerdrError(context.Background(), ExecRunner), "herdr agent start --help: ") {
		t.Error("herdr's failure is not reported")
	}
}

func TestParseModelLines(t *testing.T) {
	ms, err := ParseModelLines([]byte("Available models:\nprov/a\n  prov/b  (default)\nbad id!\n"), []string{"low", "high"}, "high")
	if err != nil || len(ms) != 2 || ms[1].ID != "prov/b" || ms[1].DefaultEffort != "high" {
		t.Fatalf("%+v %v", ms, err)
	}
	if _, err := ParseModelLines([]byte("nothing here!\n"), nil, ""); err == nil {
		t.Error("no models accepted")
	}
}
