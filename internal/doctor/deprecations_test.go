package doctor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/shell"
)

var (
	rowKey = Deprecation{Kind: KindKey, Name: "merge.old_mode", Replacement: "merge.strategy", Since: "1.2.0", Fix: "herdsman config update"}
	rowVar = Deprecation{Kind: KindVariable, Name: "HERDSMAN_OLD_LIMIT", Replacement: "HERDSMAN_LOAD_LIMIT", Since: "1.3.0", Fix: "rename it in your environment"}
	rowFlg = Deprecation{Kind: KindFlag, Name: "--old-json", Replacement: "--json", Since: "1.4.0", Fix: "change the command line"}
)

// quiet are probes that find nothing in use.
func quiet() Probes {
	return Probes{
		Getenv:     func(string) string { return "" },
		ConfigKeys: func() ([]string, error) { return nil, nil },
		ShellRC:    func(string) (string, string, error) { return "", "", nil },
	}
}

// block is the integration exactly as the installer writes it.
func block(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path, err := shell.Install(name, shell.Env{Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// rcOf serves one shell's startup text.
func rcOf(name, text string) func(string) (string, string, error) {
	return func(asked string) (string, string, error) {
		if asked == name {
			return "/rc/." + name + "rc", text, nil
		}
		return "", "", nil
	}
}

func oneCheck(t *testing.T, cs []Check) Check {
	t.Helper()
	if len(cs) != 1 {
		t.Fatalf("want one check, got %+v", cs)
	}
	if cs[0].Name != "deprecated" {
		t.Errorf("check name %q", cs[0].Name)
	}
	return cs[0]
}

func TestShippedTableIsComplete(t *testing.T) {
	kinds := map[string]bool{KindKey: true, KindFlag: true, KindVariable: true}
	seen := map[string]bool{}
	for _, d := range deprecated {
		if !kinds[d.Kind] || d.Name == "" || d.Replacement == "" || d.Since == "" || d.Fix == "" {
			t.Errorf("incomplete row: %+v", d)
		}
		if seen[d.Kind+d.Name] {
			t.Errorf("duplicate row: %+v", d)
		}
		seen[d.Kind+d.Name] = true
	}
}

func TestNothingInUseReportsNothing(t *testing.T) {
	if cs := checkDeprecations(quiet(), []Deprecation{rowKey, rowVar}); len(cs) != 0 {
		t.Errorf("%+v", cs)
	}
}

func TestProbesMissingAreSkipped(t *testing.T) {
	p := Probes{Getenv: func(string) string { return "" }}
	if cs := checkDeprecations(p, []Deprecation{rowKey, rowVar}); len(cs) != 0 {
		t.Errorf("%+v", cs)
	}
}

func TestKeySetInConfigIsWarned(t *testing.T) {
	p := quiet()
	p.ConfigKeys = func() ([]string, error) { return []string{"merge.strategy", "merge.old_mode"}, nil }
	c := oneCheck(t, checkDeprecations(p, []Deprecation{rowKey}))
	if c.Level != Warn || !reflect.DeepEqual(c.Deprecation, &rowKey) {
		t.Errorf("%+v", c)
	}
	for _, want := range []string{"key merge.old_mode", "use merge.strategy", "1.2.0", "herdsman config update"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail %q lacks %q", c.Detail, want)
		}
	}
}

func TestUnreadableConfigIsWarnedOnce(t *testing.T) {
	p := quiet()
	p.ConfigKeys = func() ([]string, error) { return nil, errors.New("bad toml") }
	c := oneCheck(t, checkDeprecations(p, []Deprecation{rowKey, rowKey}))
	if c.Level != Warn || c.Deprecation != nil || !strings.Contains(c.Detail, "bad toml") {
		t.Errorf("%+v", c)
	}
}

func TestVariableSetIsWarned(t *testing.T) {
	p := quiet()
	p.Getenv = func(k string) string {
		if k == rowVar.Name {
			return "9"
		}
		return ""
	}
	c := oneCheck(t, checkDeprecations(p, []Deprecation{rowVar}))
	if c.Level != Warn || !reflect.DeepEqual(c.Deprecation, &rowVar) || !strings.Contains(c.Detail, "variable HERDSMAN_OLD_LIMIT") {
		t.Errorf("%+v", c)
	}
}

// A flag belongs to some other command's run, so it is listed, not warned.
func TestFlagIsListedWithoutWarning(t *testing.T) {
	c := oneCheck(t, checkDeprecations(quiet(), []Deprecation{rowFlg}))
	if c.Level != OK || !reflect.DeepEqual(c.Deprecation, &rowFlg) || !strings.Contains(c.Detail, "flag --old-json") {
		t.Errorf("%+v", c)
	}
}

func TestCurrentWrapperIsNotReported(t *testing.T) {
	for _, name := range shell.Shells {
		p := quiet()
		p.ShellRC = rcOf(name, "export A=1\n"+block(t, name)+"export B=2\n")
		if cs := checkDeprecations(p, nil); len(cs) != 0 {
			t.Errorf("%s: %+v", name, cs)
		}
	}
}

func TestOlderWrapperIsWarned(t *testing.T) {
	p := quiet()
	p.ShellRC = rcOf("zsh", strings.Replace(block(t, "zsh"), "command herdsman", "herdsman-old", 1))
	c := oneCheck(t, checkDeprecations(p, nil))
	want := Deprecation{Kind: KindWrapper, Name: "zsh", Replacement: "the wrapper of this binary", Fix: "herdsman config shell install zsh"}
	if c.Level != Warn || !reflect.DeepEqual(c.Deprecation, &want) {
		t.Errorf("%+v", c)
	}
	if !strings.Contains(c.Detail, "/rc/.zshrc") || !strings.Contains(c.Detail, want.Fix) || strings.Contains(c.Detail, "since") {
		t.Errorf("detail %q", c.Detail)
	}
}

// The installer refuses a block with no end marker, so doctor must not send
// the user to it.
func TestUnterminatedWrapperAsksForAHandFix(t *testing.T) {
	p := quiet()
	p.ShellRC = rcOf("bash", wrapperStart+"\nherdsman() { :; }\n")
	c := oneCheck(t, checkDeprecations(p, nil))
	if c.Level != Warn || c.Deprecation != nil || !strings.Contains(c.Detail, "by hand") || strings.Contains(c.Detail, "shell install") {
		t.Errorf("%+v", c)
	}
}

func TestUnreadableStartupFileIsWarned(t *testing.T) {
	p := quiet()
	p.ShellRC = func(name string) (string, string, error) {
		if name == "fish" {
			return "/f", "", errors.New("permission denied")
		}
		return "", "", nil
	}
	c := oneCheck(t, checkDeprecations(p, nil))
	if c.Level != Warn || c.Deprecation != nil || !strings.Contains(c.Detail, "/f") || !strings.Contains(c.Detail, "permission denied") {
		t.Errorf("%+v", c)
	}
}

func TestTableThenWrappersInOrder(t *testing.T) {
	p := quiet()
	p.ConfigKeys = func() ([]string, error) { return []string{rowKey.Name}, nil }
	p.Getenv = func(k string) string {
		if k == rowVar.Name {
			return "9"
		}
		return ""
	}
	p.ShellRC = rcOf("bash", wrapperStart+"\nold\n"+wrapperEnd+"\n")
	var got []string
	for _, c := range checkDeprecations(p, []Deprecation{rowFlg, rowVar, rowKey}) {
		got = append(got, c.Deprecation.Kind)
	}
	if want := []string{KindFlag, KindVariable, KindKey, KindWrapper}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func TestRunReportsAnOlderWrapper(t *testing.T) {
	p := fakes(t, nil)
	p.ShellRC = rcOf("bash", wrapperStart+"\nold\n"+wrapperEnd+"\n")
	if got := find(run(t, p), "deprecated"); len(got) != 1 || got[0].Level != Warn {
		t.Errorf("%+v", got)
	}
}

func TestCheckJSONCarriesTheDeprecationOnlyWhenThereIsOne(t *testing.T) {
	plain, err := json.Marshal(Check{Level: OK, Name: "gh", Detail: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"level":"ok","check":"gh","detail":"d"}` {
		t.Errorf("%s", plain)
	}
	full, err := json.Marshal(Check{Level: Warn, Name: "deprecated", Detail: "d", Deprecation: &rowKey})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"level":"warn","check":"deprecated","detail":"d","deprecation":{"kind":"key","name":"merge.old_mode","replacement":"merge.strategy","since":"1.2.0","fix":"herdsman config update"}}`
	if string(full) != want {
		t.Errorf("%s", full)
	}
}

func TestConfigKeysInUseCountsOnlyWhatTheUserSet(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := "[schema]\nversion = \"1.0.0\"\n[merge]\nstrategy = \"squash\"\nold_mode = \"x\"\n"
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDSMAN_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(sub)
	keys, err := configKeysInUse()
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, k := range keys {
		has[k] = true
	}
	if !has["merge.strategy"] || !has["merge.old_mode"] {
		t.Errorf("set and unknown keys missing: %v", keys)
	}
	if has["branch.prefix"] || len(keys) > 4 {
		t.Errorf("defaults counted: %v", keys)
	}
}

func TestConfigKeysInUseReturnsTheLoadError(t *testing.T) {
	t.Setenv("HERDSMAN_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := configKeysInUse(); err == nil {
		t.Error("want the load error")
	}
}

func TestNearestRepositoryOutsideAnyIsEmpty(t *testing.T) {
	if got := nearestRepository(filepath.Join(string(filepath.Separator), "no-such-root-for-doctor")); got != "" {
		t.Errorf("%q", got)
	}
}

func TestReadShellRC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	if path, text, err := readShellRC("bash"); err != nil || text != "" || path != filepath.Join(home, ".bashrc") {
		t.Errorf("no file: %q %q %v", path, text, err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, text, err := readShellRC("bash"); err != nil || text != "x" {
		t.Errorf("file: %q %v", text, err)
	}
	if _, _, err := readShellRC("tcsh"); err == nil {
		t.Error("want an unknown-shell error")
	}
	t.Setenv("HOME", "")
	if _, _, err := readShellRC("bash"); err == nil {
		t.Error("want a no-home error")
	}
}

func TestReadShellRCReportsARealReadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".bashrc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readShellRC("bash"); err == nil {
		t.Error("want the read error for a folder")
	}
}

func TestDefaultProbesWireTheDeprecationReaders(t *testing.T) {
	p := Default()
	if p.ConfigKeys == nil || p.ShellRC == nil {
		t.Error("readers not wired")
	}
}
