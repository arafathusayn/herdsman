package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func suggestHome(t *testing.T) (dir string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	dir = filepath.Join(root, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func writeInstallable(t *testing.T) {
	t.Helper()
	folder := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdsman", "suggest")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nid: installable\nkind: tip\ntitle: Installable thing\ntags: [one]\nsince: 1.0.0\ninstall:\n  command: \"echo ran > made.txt\"\n  touches: [made.txt]\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(folder, "installable.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSuggest(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := commands["suggest"].run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func withTerminal(t *testing.T, value bool) {
	t.Helper()
	previous := suggestIsTerminal
	suggestIsTerminal = func(any) bool { return value }
	t.Cleanup(func() { suggestIsTerminal = previous })
}

func TestApplyRunRefusesWithoutATerminal(t *testing.T) {
	dir := suggestHome(t)
	writeInstallable(t)
	_, errOut, code := runSuggest(t, "yes\n", "apply", "installable", "--run")
	if code != 1 || !strings.Contains(errOut, "terminal") {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err == nil {
		t.Fatal("the recipe ran without a terminal")
	}
}

func TestApplyRunAsksOnATerminal(t *testing.T) {
	dir := suggestHome(t)
	writeInstallable(t)
	withTerminal(t, true)
	out, errOut, code := runSuggest(t, "yes\n", "apply", "installable", "--run")
	if code != 0 || !strings.Contains(out, "sha256:") {
		t.Fatalf("code = %d, out = %s, stderr = %s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := runSuggest(t, "no\n", "apply", "absent", "--run"); code != 1 {
		t.Fatalf("code = %d", code)
	}
}

func TestApplyPrintsTheRecipeForAnyone(t *testing.T) {
	suggestHome(t)
	writeInstallable(t)
	out, _, code := runSuggest(t, "", "apply", "installable")
	for _, want := range []string{"echo ran > made.txt", "made.txt", "sha256:", "needs approval"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("code = %d, want %q in %s", code, want, out)
		}
	}
	out, _, code = runSuggest(t, "", "apply", "installable", "--json")
	var plan struct {
		Recipe struct {
			Checksum string `json:"checksum"`
		} `json:"recipe"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &plan) != nil || plan.Recipe.Checksum == "" {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	if _, errOut, code := runSuggest(t, "", "apply", "installable", "--run", "--json"); code != 2 || errOut == "" {
		t.Fatalf("--run with --json: code = %d", code)
	}
	if _, _, code := runSuggest(t, "", "apply"); code != 2 {
		t.Fatalf("no id: code = %d", code)
	}
	if _, _, code := runSuggest(t, "", "apply", "absent"); code != 1 {
		t.Fatalf("unknown id: code = %d", code)
	}
}

func TestSuggestJSONCarriesTheReasons(t *testing.T) {
	suggestHome(t)
	out, _, code := runSuggest(t, "", "herdsman", "--json")
	var result struct {
		Items []struct {
			Reasons []struct {
				Code string `json:"code"`
			} `json:"reasons"`
		} `json:"items"`
	}
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(result.Items) == 0 || len(result.Items[0].Reasons) == 0 || result.Items[0].Reasons[0].Code == "" {
		t.Fatalf("no reasons: %s", out)
	}
}

func TestSuggestTextListsEntries(t *testing.T) {
	suggestHome(t)
	writeInstallable(t)
	out, _, code := runSuggest(t, "", "installable", "--kind", "tip", "--tag", "one", "--for", "claude")
	if code != 0 || !strings.Contains(out, "installable") || !strings.Contains(out, "Installable thing") || !strings.Contains(out, "user") {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	out, _, code = runSuggest(t, "", "nothing-matches-this-text-at-all")
	if code != 0 || !strings.Contains(out, "no suggestions") {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	if _, _, code := runSuggest(t, "", "--nope"); code != 2 {
		t.Fatalf("bad flag: code = %d", code)
	}
}

func TestRecommendIsTheSameCommand(t *testing.T) {
	suggestHome(t)
	var out, errOut bytes.Buffer
	code := commands["recommend"].run([]string{"herdsman", "--json"}, strings.NewReader(""), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), `"reasons"`) {
		t.Fatalf("code = %d, out = %s, stderr = %s", code, out.String(), errOut.String())
	}
}

func TestShowPrintsTheEntry(t *testing.T) {
	suggestHome(t)
	writeInstallable(t)
	out, _, code := runSuggest(t, "", "show", "installable")
	for _, want := range []string{"Installable thing", "Body.", "user"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("code = %d, want %q in %s", code, want, out)
		}
	}
	out, _, code = runSuggest(t, "", "show", "installable", "--json")
	if code != 0 || !strings.Contains(out, `"source"`) {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	if _, _, code := runSuggest(t, "", "show"); code != 2 {
		t.Fatalf("no id: code = %d", code)
	}
	if _, _, code := runSuggest(t, "", "show", "absent"); code != 1 {
		t.Fatalf("unknown: code = %d", code)
	}
}

func TestAddWritesFromABodyFile(t *testing.T) {
	dir := suggestHome(t)
	body := filepath.Join(dir, "body.md")
	if err := os.WriteFile(body, []byte("Written body.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runSuggest(t, "", "add", "--title", "Added thing", "--tag", "one", "--tag", "two", "--body-file", body)
	if code != 0 || !strings.Contains(out, "added-thing") {
		t.Fatalf("code = %d, out = %s, stderr = %s", code, out, errOut)
	}
	shown, _, _ := runSuggest(t, "", "show", "added-thing")
	if !strings.Contains(shown, "Written body.") {
		t.Fatalf("show: %s", shown)
	}
	out, _, code = runSuggest(t, "", "add", "--title", "Second", "--tag", "one", "--body-file", body, "--json")
	if code != 0 || !strings.Contains(out, `"path"`) {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	if _, _, code := runSuggest(t, "", "add", "--title", "Third", "--tag", "one", "--body-file", filepath.Join(dir, "absent")); code != 1 {
		t.Fatalf("absent body: code = %d", code)
	}
	if _, _, code := runSuggest(t, "", "add", "extra", "--title", "t"); code != 2 {
		t.Fatalf("extra argument: code = %d", code)
	}
	if _, _, code := runSuggest(t, "", "add", "--title", "No context", "--tag", "one", "--body-file", body, "--project"); code != 1 {
		t.Fatalf("no context repo: code = %d", code)
	}
}

func TestSourceCommandsCheckTheirArguments(t *testing.T) {
	suggestHome(t)
	out, _, code := runSuggest(t, "", "source", "list")
	if code != 0 || !strings.Contains(out, "no remote sources") {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	out, _, code = runSuggest(t, "", "source", "list", "--json")
	if code != 0 || !strings.Contains(out, `"sources"`) {
		t.Fatalf("code = %d, out = %s", code, out)
	}
	for _, args := range [][]string{{"source"}, {"source", "nope"}, {"source", "add"}, {"source", "remove"}, {"source", "update"}, {"source", "list", "extra"}} {
		if _, _, code := runSuggest(t, "", args...); code != 2 {
			t.Errorf("%v: code = %d", args, code)
		}
	}
	for _, args := range [][]string{{"source", "remove", "https://example.com/none"}, {"source", "update", "https://example.com/none"}} {
		if _, _, code := runSuggest(t, "", args...); code != 1 {
			t.Errorf("%v: code = %d", args, code)
		}
	}
}

func remoteCatalog(t *testing.T) (url string, commit func(id string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	return "file://" + dir, func(id string) string {
		folder := filepath.Join(dir, "herdsman-suggest")
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		text := "---\nid: " + id + "\nkind: tip\ntitle: Remote " + id + "\ntags: [one]\nsince: 1.0.0\n---\nFrom afar.\n"
		if err := os.WriteFile(filepath.Join(folder, id+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", "entry")
		return git("rev-parse", "HEAD")
	}
}

func TestSourceUpdateShowsTheDiffBeforeMovingThePin(t *testing.T) {
	suggestHome(t)
	url, commit := remoteCatalog(t)
	first := commit("remote-one")
	out, errOut, code := runSuggest(t, "", "source", "add", url)
	if code != 0 || !strings.Contains(out, first) {
		t.Fatalf("add: code = %d, out = %s, stderr = %s", code, out, errOut)
	}
	listed := func() string {
		out, _, _ := runSuggest(t, "", "source", "list")
		return out
	}
	if !strings.Contains(listed(), first) {
		t.Fatalf("list: %s", listed())
	}

	second := commit("remote-two")
	out, errOut, code = runSuggest(t, "", "source", "update", url)
	if code != 0 || !strings.Contains(out, "added remote-two") || !strings.Contains(errOut, "did not move") || !strings.Contains(listed(), first) {
		t.Fatalf("update without a terminal: code = %d, out = %s, stderr = %s", code, out, errOut)
	}
	out, _, code = runSuggest(t, "", "source", "update", url, "--json")
	if code != 0 || !strings.Contains(out, `"changes"`) || !strings.Contains(listed(), first) {
		t.Fatalf("update --json: code = %d, out = %s", code, out)
	}
	withTerminal(t, true)
	if _, _, code = runSuggest(t, "no\n", "source", "update", url); code != 0 || !strings.Contains(listed(), first) {
		t.Fatalf("declined: code = %d, list = %s", code, listed())
	}
	out, _, code = runSuggest(t, "yes\n", "source", "update", url)
	if code != 0 || !strings.Contains(out, "moved to "+second) || !strings.Contains(listed(), second) {
		t.Fatalf("accepted: code = %d, out = %s", code, out)
	}
	out, _, code = runSuggest(t, "", "source", "update", url)
	if code != 0 || !strings.Contains(out, "already at") {
		t.Fatalf("current: code = %d, out = %s", code, out)
	}
	third := commit("remote-three")
	if _, _, code = runSuggest(t, "", "source", "update", url, "--yes"); code != 0 || !strings.Contains(listed(), third) {
		t.Fatalf("--yes: code = %d, list = %s", code, listed())
	}
	if out, _, code = runSuggest(t, "", "source", "remove", url); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("remove: code = %d, out = %s", code, out)
	}
	if _, errOut, code = runSuggest(t, "", "source", "add", "file:///no/such/repo"); code != 1 || errOut == "" {
		t.Fatalf("bad add: code = %d", code)
	}
}

func TestSearchWarnsAboutABadEntry(t *testing.T) {
	suggestHome(t)
	folder := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdsman", "suggest")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "broken.md"), []byte("no front matter"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runSuggest(t, "", "herdsman")
	if code != 0 || !strings.Contains(errOut, "broken.md") {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
}

func TestShowPrintsTheLinks(t *testing.T) {
	dir := suggestHome(t)
	body := filepath.Join(dir, "body.md")
	if err := os.WriteFile(body, []byte("Text.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := runSuggest(t, "", "add", "--title", "Linked", "--tag", "one", "--url", "https://example.com/page", "--body-file", body); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut)
	}
	out, _, code := runSuggest(t, "", "show", "linked")
	if code != 0 || !strings.Contains(out, "link: https://example.com/page") {
		t.Fatalf("code = %d, out = %s", code, out)
	}
}

func TestApplyRunRefusesACharacterDeviceThatIsNoTerminal(t *testing.T) {
	dir := suggestHome(t)
	writeInstallable(t)
	made := filepath.Join(dir, "made.txt")

	previous := suggestIsTerminal
	suggestIsTerminal = func(any) bool { return true }
	_, errOut, code := runSuggest(t, "yes\n", "apply", "installable", "--run")
	suggestIsTerminal = previous
	if code != 0 {
		t.Fatalf("approving: code = %d, stderr = %s", code, errOut)
	}
	if err := os.Remove(made); err != nil {
		t.Fatal(err)
	}

	zero, err := os.Open("/dev/zero")
	if err != nil {
		t.Fatal(err)
	}
	defer zero.Close()
	var out, stderr bytes.Buffer
	if code := commands["suggest"].run([]string{"apply", "installable", "--run"}, zero, &out, &stderr); code != 1 || !strings.Contains(stderr.String(), "terminal") {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if _, err := os.Stat(made); err == nil {
		t.Fatal("an approved recipe ran from a device that is not a terminal")
	}
}
