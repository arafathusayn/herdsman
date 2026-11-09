package automations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/policy"
)

func sourcedRig(t *testing.T, rule Rule, deny ...string) *rig {
	t.Helper()
	r := newRig(t)
	r.engine.Config, r.engine.Dir = Config{}, ""
	r.engine.Source = func(string) (Config, policy.Policy, error) {
		return Config{Enabled: true, Rules: []Rule{rule}}, policy.Policy{Deny: deny}, nil
	}
	return r
}

func TestASourcedRuleStartsInItsProject(t *testing.T) {
	project := t.TempDir()
	rule := Rule{Event: "route.started", Kind: KindShell, Body: "pwd > where"}
	r := sourcedRig(t, rule)
	if err := r.engine.Approvals.Approve(project, rule); err != nil {
		t.Fatal(err)
	}
	ev := Event{Key: "k", Kind: "route.started", Project: project}
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(project, "where")); err != nil {
		t.Fatal(err)
	}
}

func TestASourcedPolicyDenies(t *testing.T) {
	project := t.TempDir()
	rule := Rule{Event: "route.started", Kind: KindShell, Body: "touch forbidden"}
	r := sourcedRig(t, rule, "touch")
	if err := r.engine.Approvals.Approve(project, rule); err != nil {
		t.Fatal(err)
	}
	ev := Event{Key: "k", Kind: "route.started", Project: project}
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusDenied {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(project, "forbidden")); err == nil {
		t.Fatal("a denied command ran")
	}
}

func TestAnUnreadableSourceRunsNothingAndSaysSo(t *testing.T) {
	r := newRig(t)
	r.engine.Source = func(string) (Config, policy.Policy, error) {
		return Config{}, policy.Policy{}, errors.New("broken file")
	}
	got := one(t, r.engine.Fire(context.Background(), event("route.started", "k")))
	if got.Status != StatusError || !strings.Contains(got.Error, "broken file") {
		t.Fatalf("%+v", got)
	}
	data, err := os.ReadFile(filepath.Join(r.engine.StateDir, "automations", "log.jsonl"))
	if err != nil || !strings.Contains(string(data), "broken file") {
		t.Fatalf("%q %v", data, err)
	}
}

func TestLoadProjectReadsTheRepositorysSection(t *testing.T) {
	project := t.TempDir()
	text := "[schema]\nversion = \"1.0.0\"\n\n[policy]\ndeny = [\"rm\"]\n\n[automations]\nexperimental = true\n\n[automations.on.\"route.started\"]\nrun = \"echo hi\"\n"
	if err := os.WriteFile(filepath.Join(project, "herdsman.config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	config, pol, err := LoadProject(project, []string{"HOME=" + t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || len(config.Rules) != 1 || config.Rules[0].Body != "echo hi" {
		t.Fatalf("%+v", config)
	}
	if len(pol.Deny) != 1 || pol.Deny[0] != "rm" {
		t.Fatalf("%+v", pol)
	}
}

func TestLoadProjectRefusesABadSection(t *testing.T) {
	project := t.TempDir()
	text := "[schema]\nversion = \"1.0.0\"\n\n[automations.on.\"route.started\"]\nbuiltin = \"no such\"\n"
	if err := os.WriteFile(filepath.Join(project, "herdsman.config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadProject(project, []string{"HOME=" + t.TempDir()}); err == nil {
		t.Fatal("accepted")
	}
}

func TestADryRunWithAnUnreadableSourceWritesNoLog(t *testing.T) {
	r := newRig(t)
	r.engine.DryRun = true
	r.engine.Source = func(string) (Config, policy.Policy, error) {
		return Config{}, policy.Policy{}, errors.New("broken file")
	}
	if got := one(t, r.engine.Fire(context.Background(), event("route.started", "k"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.engine.StateDir, "automations", "log.jsonl")); err == nil {
		t.Fatal("a dry run wrote the log")
	}
}

func TestLoadProjectRefusesAnUnreadableFile(t *testing.T) {
	project := t.TempDir()
	for _, name := range []string{"herdsman.config.toml", "herdsman.config.json"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := LoadProject(project, []string{"HOME=" + t.TempDir()}); err == nil {
		t.Fatal("accepted")
	}
}
