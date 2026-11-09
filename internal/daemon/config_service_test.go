package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/gitrun"
	"github.com/arafathusayn/herdsman/sdk/config"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func configClient(t *testing.T, inspect bool) configv1.ConfigService {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HERDSMAN_CONFIG", "")
	server := httptest.NewServer((&Daemon{cfg: Config{Inspect: inspect}}).rpcHandler())
	t.Cleanup(server.Close)
	return configv1.NewConfigServiceClient(&http.Client{Transport: &rpc.Transport{}}, server.URL)
}

func configTarget(t *testing.T, text string) *configv1.Target {
	t.Helper()
	path := filepath.Join(t.TempDir(), "herdsman.config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return &configv1.Target{ConfigPath: path}
}

func TestConfigServiceReadsAndChangesAFile(t *testing.T) {
	client := configClient(t, false)
	ctx := context.Background()
	target := configTarget(t, "# keep\n[teardown]\ngrace = 2\n")

	got, err := client.Get(ctx, &configv1.GetRequest{Target: target, Key: "teardown.grace"})
	if err != nil || got.GetValue() != "2" || got.GetSource() != config.SourceProject {
		t.Fatalf("get: %v %v", got, err)
	}
	set, err := client.Set(ctx, &configv1.SetRequest{Target: target, Key: "merge.land", Value: "push"})
	if err != nil || set.GetPath() != target.GetConfigPath() {
		t.Fatalf("set: %v %v", set, err)
	}
	shown, err := client.Show(ctx, &configv1.ShowRequest{Target: target})
	if err != nil || shown.GetProjectPath() != target.GetConfigPath() {
		t.Fatalf("show: %v %v", shown, err)
	}
	if !slices.ContainsFunc(shown.GetValues(), func(v *configv1.ShownValue) bool {
		return v.GetKey() == "merge.land" && v.GetValue() == "push" && v.GetSource() == config.SourceProject
	}) {
		t.Fatalf("values: %v", shown.GetValues())
	}
	exported, err := client.Export(ctx, &configv1.ExportRequest{Target: target, Format: "json"})
	if err != nil || !strings.Contains(string(exported.GetData()), "push") {
		t.Fatalf("export: %s %v", exported.GetData(), err)
	}
	removed, err := client.Unset(ctx, &configv1.UnsetRequest{Target: target, Key: "merge.land"})
	if err != nil || !removed.GetRemoved() {
		t.Fatalf("unset: %v %v", removed, err)
	}
	removed, err = client.Unset(ctx, &configv1.UnsetRequest{Target: target, Key: "merge.land"})
	if err != nil || removed.GetRemoved() {
		t.Fatalf("second unset: %v %v", removed, err)
	}
	data, _ := os.ReadFile(target.GetConfigPath())
	if !strings.Contains(string(data), "# keep") {
		t.Fatalf("lost a comment:\n%s", data)
	}
}

func TestConfigServiceValidateReportsWarningsAndStrictness(t *testing.T) {
	client := configClient(t, false)
	ctx := context.Background()
	target := configTarget(t, "[branch]\nnmae = \"x\"\n")
	got, err := client.Validate(ctx, &configv1.ValidateRequest{Target: target})
	if err != nil || len(got.GetWarnings()) != 1 || !strings.Contains(got.GetWarnings()[0], "nmae") {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := client.Validate(ctx, &configv1.ValidateRequest{Target: target, Strict: true}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("strict: %v", err)
	}
	if _, err := client.Show(ctx, &configv1.ShowRequest{Target: target, Strict: true}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("show strict: %v", err)
	}
	shown, err := client.Show(ctx, &configv1.ShowRequest{Target: target})
	if err != nil || len(shown.GetWarnings()) != 1 {
		t.Fatalf("show: %v %v", shown, err)
	}
}

func TestConfigServiceAppliesTheRepositorysOverrides(t *testing.T) {
	client := configClient(t, false)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "remote", "add", "origin", "https://github.com/acme/app.git")
	dir := config.UserDir(os.Environ())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "[projects.\"github.com/acme/app\".branch]\nname = \"special\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(context.Background(), &configv1.GetRequest{Target: &configv1.Target{RepoRoot: repo}, Key: "branch.name"})
	if err != nil || got.GetValue() != "special" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestConfigServiceRefusals(t *testing.T) {
	client := configClient(t, false)
	ctx := context.Background()
	empty := &configv1.Target{RepoRoot: t.TempDir()}
	good := configTarget(t, "")
	broken := configTarget(t, "not toml")
	cases := map[string]struct {
		call func() error
		want rpc.Code
	}{
		"get unknown": {func() error {
			_, err := client.Get(ctx, &configv1.GetRequest{Target: good, Key: "no.such"})
			return err
		}, rpc.InvalidArgument},
		"get unset": {func() error {
			_, err := client.Get(ctx, &configv1.GetRequest{Target: good, Key: "hooks.pre_merge"})
			return err
		}, rpc.InvalidArgument},
		"get broken": {func() error {
			_, err := client.Get(ctx, &configv1.GetRequest{Target: broken, Key: "merge.land"})
			return err
		}, rpc.InvalidArgument},
		"set no file": {func() error {
			_, err := client.Set(ctx, &configv1.SetRequest{Target: empty, Key: "merge.land", Value: "push"})
			return err
		}, rpc.FailedPrecondition},
		"set bad value": {func() error {
			_, err := client.Set(ctx, &configv1.SetRequest{Target: good, Key: "merge.land", Value: "nope"})
			return err
		}, rpc.InvalidArgument},
		"unset no file": {func() error {
			_, err := client.Unset(ctx, &configv1.UnsetRequest{Target: empty, Key: "merge.land"})
			return err
		}, rpc.FailedPrecondition},
		"unset bad key": {func() error {
			_, err := client.Unset(ctx, &configv1.UnsetRequest{Target: good, Key: "no.such"})
			return err
		}, rpc.InvalidArgument},
		"export no file": {func() error {
			_, err := client.Export(ctx, &configv1.ExportRequest{Target: empty, Format: "json"})
			return err
		}, rpc.FailedPrecondition},
		"export bad format": {func() error {
			_, err := client.Export(ctx, &configv1.ExportRequest{Target: good, Format: "ini"})
			return err
		}, rpc.InvalidArgument},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := tc.call(); codeOf(err) != tc.want {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestConfigServiceChangesNothingInInspectMode(t *testing.T) {
	client := configClient(t, true)
	ctx := context.Background()
	const text = "[merge]\nland = \"pr\"\n"
	target := configTarget(t, text)
	if _, err := client.Set(ctx, &configv1.SetRequest{Target: target, Key: "merge.land", Value: "push"}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("set: %v", err)
	}
	if _, err := client.Unset(ctx, &configv1.UnsetRequest{Target: target, Key: "merge.land"}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("unset: %v", err)
	}
	if got, err := client.Get(ctx, &configv1.GetRequest{Target: target, Key: "merge.land"}); err != nil || got.GetValue() != "pr" {
		t.Fatalf("get: %v %v", got, err)
	}
	if _, err := client.Show(ctx, &configv1.ShowRequest{Target: target}); err != nil {
		t.Fatalf("show: %v", err)
	}
	if _, err := client.Validate(ctx, &configv1.ValidateRequest{Target: target}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := client.Export(ctx, &configv1.ExportRequest{Target: target, Format: "json"}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if data, _ := os.ReadFile(target.GetConfigPath()); string(data) != text {
		t.Fatalf("file changed:\n%s", data)
	}
}

// settingsIn loads the settings of a repository whose parent is folder.
func settingsIn(t *testing.T, folder string) (*repository, error) {
	t.Helper()
	main := filepath.Join(folder, "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &repository{git: &gitrun.Runner{}, main: main}
	return r, (&WorktreeService{ConfigDir: t.TempDir()}).loadSettings(context.Background(), r)
}

func TestHooksLoadFromEveryFormat(t *testing.T) {
	for name, tc := range map[string]struct{ file, text string }{
		"toml": {"herdsman.config.toml", "[hooks]\npre-start = \"echo a\"\n"},
		"yaml": {"herdsman.config.yaml", "hooks:\n  pre_start: echo a\n"},
		"yml":  {"herdsman.config.yml", "hooks:\n  pre-start: echo a\n"},
		"json": {"herdsman.config.json", `{"hooks":{"pre-start":"echo a"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			folder := t.TempDir()
			if err := os.WriteFile(filepath.Join(folder, tc.file), []byte(tc.text), 0o644); err != nil {
				t.Fatal(err)
			}
			r, err := settingsIn(t, folder)
			if err != nil || len(r.hooks["pre-start"]) != 1 || r.hooks["pre-start"][0][0].Template != "echo a" {
				t.Fatalf("%+v %v", r.hooks, err)
			}
		})
	}
	t.Run("binary", func(t *testing.T) {
		folder := t.TempDir()
		source := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(source, []byte("[hooks]\npre-start = \"echo a\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		data, err := config.ExportFile(source, config.FormatBin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "herdsman.config.bin"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		r, err := settingsIn(t, folder)
		if err != nil || len(r.hooks["pre-start"]) != 1 {
			t.Fatalf("%+v %v", r.hooks, err)
		}
	})
}

func TestTwoConfigFilesStopHookLoading(t *testing.T) {
	folder := t.TempDir()
	for _, name := range []string{"herdsman.config.toml", "herdsman.config.json"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := settingsIn(t, folder); err == nil || !strings.Contains(err.Error(), "keep one") {
		t.Fatalf("two files: %v", err)
	}
}

func TestEveryApprovalUsesTheStoreTheConfigCommandReads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	want := filepath.Join(config.UserDir(os.Environ()), "approvals.toml")

	worktrees, err := (&WorktreeService{}).approvals()
	if err != nil || worktrees.Path != want {
		t.Fatalf("worktrees: %v %v", worktrees, err)
	}
	if got := (&SuggestService{}).approvals().Path; got != want {
		t.Fatalf("suggest: %s", got)
	}
}
