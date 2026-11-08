package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arafathusayn/herdsman/internal/suggest"
	"github.com/arafathusayn/herdsman/sdk/rpc"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
	"google.golang.org/protobuf/proto"
)

func newSuggestService(t *testing.T) *SuggestService {
	t.Helper()
	root := t.TempDir()
	return &SuggestService{
		ConfigDir: filepath.Join(root, "config"),
		CacheDir:  filepath.Join(root, "cache"),
		Home:      filepath.Join(root, "home"),
		Harnesses: []string{"claude"},
	}
}

func writeUserEntry(t *testing.T, s *SuggestService, id, frontMatter string) {
	t.Helper()
	dir := filepath.Join(s.ConfigDir, "herdsman", "suggest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "---\nid: " + id + "\nkind: tip\ntitle: Title of " + id + "\ntags: [one]\nsince: 1.0.0\n" + frontMatter + "---\nBody of " + id + ".\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandInstall(command string) string {
	return "install:\n  command: \"" + command + "\"\n  touches: [a.txt]\n"
}

func TestSearchCarriesReasonsAndIsRepeatable(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "for-claude", "applies_to:\n  harnesses: [claude]\n")
	ctx := context.Background()
	first, err := s.Search(ctx, SuggestSearchRequest{Dir: t.TempDir(), Text: "for-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) == 0 || first.Items[0].Entry.ID != "for-claude" {
		t.Fatalf("items = %+v", first.Items)
	}
	codes := []string{}
	for _, reason := range first.Items[0].Reasons {
		codes = append(codes, reason.Code)
	}
	if !slices.Contains(codes, suggest.ReasonHarness) {
		t.Fatalf("reasons = %v, want the harness reason", codes)
	}
	data, err := s.Call(ctx, "Search", []byte(`{"text":"for-claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reasons"`) {
		t.Fatalf("response has no reasons: %s", data)
	}
	again, err := s.Search(ctx, SuggestSearchRequest{Dir: first.Dir, Text: "for-claude"})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := json.Marshal(first.Items); string(a) != mustJSON(t, again.Items) {
		t.Fatal("the same machine must give the same ranking")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSearchAppliesFiltersAndDetectsTheMachine(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "go-tip", "applies_to:\n  ecosystems: [go]\n")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := s.Search(context.Background(), SuggestSearchRequest{Dir: dir, Text: "go-tip", Kind: "tip", Tag: "one", For: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || !strings.Contains(mustJSON(t, result.Items[0].Reasons), suggest.ReasonEcosystem) {
		t.Fatalf("items = %s", mustJSON(t, result.Items))
	}
	none, err := s.Search(context.Background(), SuggestSearchRequest{Dir: dir, Text: "go-tip", Kind: "mcp"})
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("filtered = %+v, %v", none.Items, err)
	}
}

func TestSearchSkipsWhatIsAlreadyInstalled(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "present", "")
	skill := filepath.Join(s.Home, ".claude", "skills", "present")
	if err := os.MkdirAll(filepath.Join(skill, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\n\turl = https://example.com/owner/thing.git\n"
	if err := os.WriteFile(filepath.Join(skill, ".git", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	machine := s.machine(t.TempDir())
	if !slices.Contains(machine.Installed.IDs, "present") || !slices.Contains(machine.Installed.Repos, "https://example.com/owner/thing.git") {
		t.Fatalf("installed = %+v", machine.Installed)
	}
	result, err := s.Search(context.Background(), SuggestSearchRequest{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustJSON(t, result.Excluded), "present") {
		t.Fatalf("excluded = %s", mustJSON(t, result.Excluded))
	}
}

func TestHarnessesAreLookedUpWhenNotGiven(t *testing.T) {
	s := newSuggestService(t)
	s.Harnesses = nil
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := s.machine(t.TempDir()).Harnesses; !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("harnesses = %v", got)
	}
}

func TestGetReturnsTheEntryOrAnError(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "known", "")
	got, err := s.Get(context.Background(), SuggestGetRequest{ID: "known"})
	if err != nil || got.Entry.Title != "Title of known" || got.Entry.Source.Kind != suggest.SourceUser {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.Get(context.Background(), SuggestGetRequest{ID: "absent"}); err == nil {
		t.Fatal("an unknown id must fail")
	}
}

func TestAddWritesAnEntryTheCatalogReads(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	added, err := s.Add(ctx, SuggestAddRequest{Kind: "tip", Title: `Keep "quotes" safe`, Tags: []string{"one"}, URL: "https://example.com/x", Repo: "https://example.com/a/b", Body: "Body text."})
	if err != nil {
		t.Fatal(err)
	}
	if added.ID != "keep-quotes-safe" || !strings.HasSuffix(added.Path, "keep-quotes-safe.md") {
		t.Fatalf("added = %+v", added)
	}
	got, err := s.Get(ctx, SuggestGetRequest{ID: added.ID})
	if err != nil || got.Entry.Title != `Keep "quotes" safe` || got.Entry.Body != "Body text." || got.Entry.URL == "" || got.Entry.Repo == "" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.Add(ctx, SuggestAddRequest{Kind: "tip", Title: `Keep "quotes" safe`, Tags: []string{"one"}, Body: "Again."}); err == nil {
		t.Fatal("adding the same id twice must be refused")
	}
}

func TestAddRefusesBadInput(t *testing.T) {
	s := newSuggestService(t)
	for name, request := range map[string]SuggestAddRequest{
		"injected title": {Kind: "tip", Title: "x\n---\nid: evil", Tags: []string{"one"}, Body: "b"},
		"no title":       {Kind: "tip", Tags: []string{"one"}, Body: "b"},
		"symbols title":  {Kind: "tip", Title: "!!!", Tags: []string{"one"}, Body: "b"},
		"no body":        {Kind: "tip", Title: "t", Tags: []string{"one"}},
		"no tag":         {Kind: "tip", Title: "t", Body: "b"},
		"bad kind":       {Kind: "nope", Title: "t", Tags: []string{"one"}, Body: "b"},
		"bad id":         {ID: "../up", Kind: "tip", Title: "t", Tags: []string{"one"}, Body: "b"},
		"commented id":   {ID: "safe #/../../escaped", Kind: "tip", Title: "t", Tags: []string{"one"}, Body: "b"},
		"commented kind": {Kind: "tip # x", Title: "t", Tags: []string{"one"}, Body: "b"},
	} {
		if _, err := s.Add(context.Background(), request); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.ConfigDir, "herdsman", "suggest"))
	if len(entries) != 0 {
		t.Fatalf("refused entries left files: %v", entries)
	}
	if matches, _ := filepath.Glob(filepath.Join(s.ConfigDir, "*", "*.md")); len(matches) != 0 {
		t.Fatalf("an id escaped the catalog folder: %v", matches)
	}
	if matches, _ := filepath.Glob(filepath.Join(s.ConfigDir, "*.md")); len(matches) != 0 {
		t.Fatalf("an id escaped the catalog folder: %v", matches)
	}
}

func TestAddToTheProjectNeedsAContextRepo(t *testing.T) {
	s := newSuggestService(t)
	request := SuggestAddRequest{Dir: t.TempDir(), Project: true, Kind: "tip", Title: "Shared", Tags: []string{"one"}, Body: "b"}
	if _, err := s.Add(context.Background(), request); !errors.Is(err, ErrNoContextRepo) {
		t.Fatalf("err = %v", err)
	}
	context_ := t.TempDir()
	s.ContextDir = func(string) string { return context_ }
	added, err := s.Add(context.Background(), request)
	if err != nil || added.Path != filepath.Join(context_, "suggest", "shared.md") {
		t.Fatalf("added = %+v, %v", added, err)
	}
	got, err := s.Get(context.Background(), SuggestGetRequest{Dir: request.Dir, ID: "shared"})
	if err != nil || got.Entry.Source.Kind != suggest.SourceProject {
		t.Fatalf("get = %+v, %v", got, err)
	}
}

func TestPlanShowsTheRecipeAndItsApproval(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "installable", commandInstall("echo one"))
	writeUserEntry(t, s, "plain", "")
	ctx := context.Background()
	plan, err := s.Plan(ctx, SuggestPlanRequest{ID: "installable"})
	if err != nil || plan.Recipe.Text != "echo one" || plan.Approved || !strings.HasPrefix(plan.Recipe.Checksum, "sha256:") {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if _, err := s.Plan(ctx, SuggestPlanRequest{ID: "plain"}); !errors.Is(err, suggest.ErrNoRecipe) {
		t.Fatalf("no recipe: %v", err)
	}
	if _, err := s.Plan(ctx, SuggestPlanRequest{ID: "absent"}); err == nil {
		t.Fatal("an unknown id must fail")
	}
}

// An agent reaches the service; only a person at a terminal may install.
func TestTheServiceCannotRunARecipe(t *testing.T) {
	s := newSuggestService(t)
	for _, method := range []string{"Run", "Apply", "Approve"} {
		if _, err := s.Call(context.Background(), method, []byte(`{"id":"x"}`)); err == nil {
			t.Errorf("method %s must not exist", method)
		}
	}
}

func TestSuggestRunRefusesWithoutATerminalAndRunsWithOne(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "installable", commandInstall("echo ran > made.txt"))
	dir := t.TempDir()
	ctx := context.Background()
	var out strings.Builder
	in := strings.NewReader("yes\n")
	err := s.Run(ctx, dir, "installable", suggest.Terminal{In: in, Out: &out, Err: &out})
	if !errors.Is(err, suggest.ErrNoTerminal) {
		t.Fatalf("err = %v", err)
	}
	if err := s.Run(ctx, dir, "installable", suggest.Terminal{In: in, Out: &out, Err: &out, IsTerminal: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, dir, "absent", suggest.Terminal{In: in, Out: &out, Err: &out, IsTerminal: true}); err == nil {
		t.Fatal("an unknown id must fail")
	}
}

func catalogRepo(t *testing.T) (url string, commit func(id string) string) {
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
		folder := filepath.Join(dir, suggest.RemoteFolder)
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

func TestSourcesArePinnedAndMoveOnlyAfterThePreview(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	url, commit := catalogRepo(t)
	first := commit("remote-one")

	added, err := s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url})
	if err != nil || added.Pin.Commit != first {
		t.Fatalf("add = %+v, %v (want %s)", added, err, first)
	}
	if _, err := s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url}); err == nil {
		t.Fatal("adding a source twice must be refused")
	}
	listed, err := s.Sources(ctx, SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != 1 || listed.Sources[0].Commit != first {
		t.Fatalf("sources = %+v, %v", listed, err)
	}
	found, err := s.Search(ctx, SuggestSearchRequest{Dir: t.TempDir(), Text: "remote-one"})
	if err != nil || len(found.Items) != 1 || found.Items[0].Entry.Source.Commit != first {
		t.Fatalf("search = %s, %v", mustJSON(t, found.Items), err)
	}

	second := commit("remote-two")
	preview, err := s.SourcePreview(ctx, SuggestSourcePreviewRequest{URL: url})
	if err != nil || preview.To.Commit != second || len(preview.Changes) != 1 || !strings.Contains(preview.Changes[0].Diff, "Remote remote-two") {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	listed, _ = s.Sources(ctx, SuggestSourcesRequest{})
	if listed.Sources[0].Commit != first {
		t.Fatal("a preview must not move the pin")
	}
	moved := SuggestSourcePinRequest{URL: url, From: first, To: second}
	if _, err := s.SourcePin(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SourcePin(ctx, moved); err == nil {
		t.Fatal("a pin that moved since the preview must be refused")
	}
	listed, _ = s.Sources(ctx, SuggestSourcesRequest{})
	if listed.Sources[0].Commit != second {
		t.Fatalf("sources = %+v", listed)
	}

	if _, err := s.SourceRemove(ctx, SuggestSourceRemoveRequest{URL: url}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SourceRemove(ctx, SuggestSourceRemoveRequest{URL: url}); err == nil {
		t.Fatal("removing an unknown source must fail")
	}
	if _, err := s.SourcePreview(ctx, SuggestSourcePreviewRequest{URL: url}); err == nil {
		t.Fatal("previewing an unknown source must fail")
	}
}

func TestABrokenSourcesFileIsReported(t *testing.T) {
	s := newSuggestService(t)
	path := s.sourcesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a pin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sources(context.Background(), SuggestSourcesRequest{}); err == nil {
		t.Fatal("a bad pin must be an error")
	}
	if _, err := s.Search(context.Background(), SuggestSearchRequest{Dir: t.TempDir()}); err == nil {
		t.Fatal("search must report a bad pin")
	}
}

func TestSuggestCallRoutesEveryMethod(t *testing.T) {
	s := newSuggestService(t)
	writeUserEntry(t, s, "known", commandInstall("echo one"))
	ctx := context.Background()
	for method, request := range map[string]string{
		"Search":  `{"text":"known"}`,
		"Get":     `{"id":"known"}`,
		"Plan":    `{"id":"known"}`,
		"Sources": `{}`,
		"Add":     `{"kind":"tip","title":"Routed","tags":["one"],"body":"b"}`,
	} {
		if _, err := s.Call(ctx, method, []byte(request)); err != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	for _, method := range []string{"SourceAdd", "SourceRemove", "SourcePreview", "SourcePin"} {
		if _, err := s.Call(ctx, method, []byte(`{"url":"file://`+filepath.Join(s.Home, "no-such-repo")+`"}`)); err == nil {
			t.Errorf("%s: an unknown or unreachable source must fail", method)
		}
	}
	if _, err := s.Call(ctx, "Search", []byte(`{`)); err == nil {
		t.Error("a bad request must fail")
	}
}

func TestSuggestDefaultsUseTheUsersFolders(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	data, err := SuggestCall(context.Background(), "Sources", []byte(`{}`))
	if err != nil || !strings.Contains(string(data), `"sources"`) {
		t.Fatalf("data = %s, %v", data, err)
	}
	s := &SuggestService{}
	if got := s.sourcesPath(); got != filepath.Join(root, "config", "herdsman", "suggest-sources.txt") {
		t.Fatalf("sources path = %s", got)
	}
	if got := s.cacheDir(); got != filepath.Join(root, "cache", "herdsman", "suggest") {
		t.Fatalf("cache = %s", got)
	}
	if err := SuggestRun(context.Background(), root, "absent", suggest.Terminal{In: strings.NewReader(""), Out: os.Stderr, Err: os.Stderr}); err == nil {
		t.Fatal("an unknown id must fail")
	}
}

func TestSuggestFallsBackToTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	s := &SuggestService{}
	if got := s.sourcesPath(); got != filepath.Join(home, ".config", "herdsman", "suggest-sources.txt") {
		t.Fatalf("sources path = %s", got)
	}
	if got := s.cacheDir(); got != filepath.Join(home, ".cache", "herdsman", "suggest") {
		t.Fatalf("cache = %s", got)
	}
	t.Setenv("HOME", "")
	if got := s.home(); got != "" {
		t.Fatalf("home = %q", got)
	}
}

func suggestAdapter(t *testing.T) (*suggestRPC, *SuggestService) {
	t.Helper()
	s := newSuggestService(t)
	return &suggestRPC{s: s}, s
}

func failureCode(t *testing.T, err error) rpc.Code {
	t.Helper()
	var failure *rpc.Error
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a wire failure", err)
	}
	return failure.Code
}

func TestSuggestRPCCarriesEveryEntryField(t *testing.T) {
	r, s := suggestAdapter(t)
	writeUserEntry(t, s, "full", "url: https://example.com/a\nrepo: https://example.com/b\napplies_to:\n  harnesses: [claude]\n  ecosystems: [go]\n  platforms: [linux, darwin]\n"+commandInstall("true"))
	ctx := context.Background()
	got, err := r.Get(ctx, &suggestv1.GetRequest{Id: "full"})
	if err != nil {
		t.Fatal(err)
	}
	e := got.GetEntry()
	if e.GetUrl() != "https://example.com/a" || e.GetRepo() != "https://example.com/b" || e.GetSince() != "1.0.0" || e.GetBody() != "Body of full." || e.GetSource().GetName() != "user" {
		t.Fatalf("entry = %v", e)
	}
	if a := e.GetAppliesTo(); !slices.Equal(a.GetHarnesses(), []string{"claude"}) || !slices.Equal(a.GetEcosystems(), []string{"go"}) || !slices.Equal(a.GetPlatforms(), []string{"linux", "darwin"}) {
		t.Fatalf("applies_to = %v", a)
	}
	if i := e.GetInstall(); i.GetCommand() != "true" || !slices.Equal(i.GetTouches(), []string{"a.txt"}) {
		t.Fatalf("install = %v", i)
	}
	plan, err := r.Plan(ctx, &suggestv1.PlanRequest{Id: "full"})
	if err != nil || plan.GetRecipe().GetKind() != "command" || plan.GetRecipe().GetText() != "true" || plan.GetApproved() {
		t.Fatalf("plan = %v, %v", plan, err)
	}
	if !proto.Equal(entryToProto(suggest.Entry{ID: "x"}).GetInstall(), (*suggestv1.Install)(nil)) {
		t.Fatal("an entry with no recipe must carry no install")
	}
}

func TestSuggestRPCReportsExcludedAndProblems(t *testing.T) {
	r, s := suggestAdapter(t)
	writeUserEntry(t, s, "elsewhere", "applies_to:\n  harnesses: [opencode]\n")
	broken := filepath.Join(s.ConfigDir, "herdsman", "suggest", "broken.md")
	if err := os.WriteFile(broken, []byte("not an entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserEntry(t, s, "for-this-machine", "applies_to:\n  harnesses: [claude]\n")
	near, err := r.Search(context.Background(), &suggestv1.SearchRequest{Text: "for-this-machine"})
	if err != nil || len(near.GetItems()) != 1 || len(near.GetItems()[0].GetReasons()) == 0 || near.GetItems()[0].GetScore() == 0 {
		t.Fatalf("search = %v, %v", near, err)
	}
	found, err := r.Search(context.Background(), &suggestv1.SearchRequest{Dir: "/d", Text: "elsewhere"})
	if err != nil || found.GetDir() != "/d" {
		t.Fatalf("search = %v, %v", found, err)
	}
	if !slices.ContainsFunc(found.GetExcluded(), func(e *suggestv1.Excluded) bool {
		return e.GetId() == "elsewhere" && e.GetReason().GetCode() != ""
	}) {
		t.Fatalf("excluded = %v", found.GetExcluded())
	}
	if !slices.ContainsFunc(found.GetProblems(), func(p *suggestv1.Problem) bool {
		return strings.Contains(p.GetFile(), "broken.md") && p.GetError() != "" && p.GetSource().GetKind() == "user"
	}) {
		t.Fatalf("problems = %v", found.GetProblems())
	}
}

func TestSuggestRPCFailureCodes(t *testing.T) {
	r, s := suggestAdapter(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(s.ConfigDir, "herdsman"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(ctx, &suggestv1.AddRequest{Id: "x", Kind: "tip", Title: "line\nbreak", Tags: []string{"t"}, Body: "b"}); failureCode(t, err) != rpc.InvalidArgument {
		t.Fatalf("a line break gave %v", err)
	}
	if _, err := r.SourceRemove(ctx, &suggestv1.SourceRemoveRequest{Url: "https://example.com/none"}); failureCode(t, err) != rpc.NotFound {
		t.Fatalf("an unknown source gave %v", err)
	}
	if _, err := r.SourcePin(ctx, &suggestv1.SourcePinRequest{Url: "https://example.com/none"}); failureCode(t, err) != rpc.NotFound {
		t.Fatalf("an unknown pin gave %v", err)
	}
	if err := os.WriteFile(s.sourcesPath(), []byte("not-a-pin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sources(ctx, &suggestv1.SourcesRequest{}); failureCode(t, err) != rpc.Internal {
		t.Fatalf("a broken sources file gave %v", err)
	}
	if _, err := r.Search(ctx, &suggestv1.SearchRequest{}); failureCode(t, err) != rpc.Internal {
		t.Fatalf("search with a broken sources file gave %v", err)
	}
	if _, err := r.SourceAdd(ctx, &suggestv1.SourceAddRequest{Url: "https://example.com/a"}); failureCode(t, err) != rpc.Internal {
		t.Fatalf("source add with a broken sources file gave %v", err)
	}
	if _, err := r.SourcePreview(ctx, &suggestv1.SourcePreviewRequest{Url: "https://example.com/a"}); failureCode(t, err) != rpc.Internal {
		t.Fatalf("source preview with a broken sources file gave %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := suggestFailure(canceled, errors.New("anything")); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled call gave %v", err)
	}
}

func TestSuggestServiceIsOnTheDaemonsRPC(t *testing.T) {
	paths := []string{}
	for _, procedure := range (&Daemon{}).rpcProcedures() {
		paths = append(paths, procedure.Path)
	}
	for _, method := range []string{"Search", "Get", "Add", "Plan", "Sources", "SourceAdd", "SourceRemove", "SourcePreview", "SourcePin"} {
		if !slices.Contains(paths, rpc.Path(suggestv1.SuggestServiceName, method)) {
			t.Errorf("%s is not served; paths = %v", method, paths)
		}
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "/Run") || strings.HasSuffix(path, "/Apply") || strings.HasSuffix(path, "/Approve") {
			t.Errorf("%s must not exist: only a person at a terminal runs a recipe", path)
		}
	}
}

func TestConcurrentSourceChangesKeepEveryPin(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	const sources = 6
	urls := make([]string, sources)
	for i := range urls {
		url, commit := catalogRepo(t)
		commit("entry-one")
		urls[i] = url
	}
	var group sync.WaitGroup
	errs := make([]error, sources)
	for i, url := range urls {
		group.Go(func() {
			_, errs[i] = s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url})
		})
	}
	group.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Sources(ctx, SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != sources {
		t.Fatalf("sources = %d of %d, %v", len(listed.Sources), sources, err)
	}
}

func TestTwoAddsOfOneSourceLeaveOnePin(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	url, commit := catalogRepo(t)
	commit("entry-one")
	var group sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		group.Go(func() {
			_, errs[i] = s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url})
		})
	}
	group.Wait()
	refusals := 0
	for _, err := range errs {
		if err != nil {
			refusals++
		}
	}
	listed, err := s.Sources(ctx, SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != 1 || refusals != 3 {
		t.Fatalf("sources = %v, refusals = %d, %v", listed.Sources, refusals, err)
	}
}

func TestSuggestRefusalsReadTheSameOnTheCommandLine(t *testing.T) {
	s := newSuggestService(t)
	_, err := s.Get(context.Background(), SuggestGetRequest{ID: "none"})
	if err == nil || err.Error() != `no suggestion has the id "none"` {
		t.Fatalf("error = %v", err)
	}
}

func TestSuggestRPCSourceRoundTrip(t *testing.T) {
	r, _ := suggestAdapter(t)
	ctx := context.Background()
	url, commit := catalogRepo(t)
	first := commit("rpc-one")
	added, err := r.SourceAdd(ctx, &suggestv1.SourceAddRequest{Url: url})
	if err != nil || added.GetPin().GetCommit() != first {
		t.Fatalf("add = %v, %v", added, err)
	}
	listed, err := r.Sources(ctx, &suggestv1.SourcesRequest{})
	if err != nil || len(listed.GetSources()) != 1 {
		t.Fatalf("sources = %v, %v", listed, err)
	}
	second := commit("rpc-two")
	preview, err := r.SourcePreview(ctx, &suggestv1.SourcePreviewRequest{Url: url})
	if err != nil || preview.GetTo().GetCommit() != second || len(preview.GetChanges()) != 1 || preview.GetChanges()[0].GetDiff() == "" {
		t.Fatalf("preview = %v, %v", preview, err)
	}
	pinned, err := r.SourcePin(ctx, &suggestv1.SourcePinRequest{Url: url, From: first, To: second})
	if err != nil || pinned.GetPin().GetCommit() != second {
		t.Fatalf("pin = %v, %v", pinned, err)
	}
	removed, err := r.SourceRemove(ctx, &suggestv1.SourceRemoveRequest{Url: url})
	if err != nil || removed.GetUrl() != url {
		t.Fatalf("remove = %v, %v", removed, err)
	}
}

func TestSuggestRPCEntryRefusals(t *testing.T) {
	r, s := suggestAdapter(t)
	ctx := context.Background()
	writeUserEntry(t, s, "plain", "")
	added, err := r.Add(ctx, &suggestv1.AddRequest{Id: "fresh", Kind: "tip", Title: "T", Tags: []string{"t"}, Body: "b"})
	if err != nil || added.GetId() != "fresh" || added.GetPath() == "" {
		t.Fatalf("add = %v, %v", added, err)
	}
	cases := []struct {
		name string
		call func() error
		want rpc.Code
	}{
		{"get no id", func() error { _, err := r.Get(ctx, &suggestv1.GetRequest{}); return err }, rpc.InvalidArgument},
		{"get unknown", func() error { _, err := r.Get(ctx, &suggestv1.GetRequest{Id: "none"}); return err }, rpc.NotFound},
		{"plan no id", func() error { _, err := r.Plan(ctx, &suggestv1.PlanRequest{}); return err }, rpc.InvalidArgument},
		{"plan unknown", func() error { _, err := r.Plan(ctx, &suggestv1.PlanRequest{Id: "none"}); return err }, rpc.NotFound},
		{"plan no recipe", func() error { _, err := r.Plan(ctx, &suggestv1.PlanRequest{Id: "plain"}); return err }, rpc.FailedPrecondition},
		{"add twice", func() error {
			_, err := r.Add(ctx, &suggestv1.AddRequest{Id: "fresh", Kind: "tip", Title: "T", Tags: []string{"t"}, Body: "b"})
			return err
		}, rpc.AlreadyExists},
		{"project without a context repo", func() error {
			_, err := r.Add(ctx, &suggestv1.AddRequest{Project: true, Id: "p", Kind: "tip", Title: "T", Tags: []string{"t"}, Body: "b"})
			return err
		}, rpc.FailedPrecondition},
		{"bad address", func() error { _, err := r.SourceAdd(ctx, &suggestv1.SourceAddRequest{Url: "ext::x"}); return err }, rpc.InvalidArgument},
		{"preview unknown", func() error {
			_, err := r.SourcePreview(ctx, &suggestv1.SourcePreviewRequest{Url: "https://example.com/none"})
			return err
		}, rpc.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := failureCode(t, c.call()); got != c.want {
				t.Fatalf("code %v, want %v", got, c.want)
			}
		})
	}
}

// blockFolder puts a file where the service needs a folder.
func blockFolder(t *testing.T, s *SuggestService) {
	t.Helper()
	if err := os.WriteFile(s.ConfigDir, []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestReportsAnUnusableConfigFolder(t *testing.T) {
	ctx := context.Background()
	entry := SuggestAddRequest{ID: "x", Kind: "tip", Title: "T", Tags: []string{"t"}, Body: "b"}

	s := newSuggestService(t)
	blockFolder(t, s)
	if _, err := s.Add(ctx, entry); err == nil {
		t.Fatal("add must report a config folder it cannot create")
	}
	if err := s.storePins(nil); err == nil {
		t.Fatal("storing pins must report a config folder it cannot create")
	}

	// The sources path is a folder: reading it fails, and so does the rename.
	s = newSuggestService(t)
	if err := os.MkdirAll(s.sourcesPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pins(); err == nil {
		t.Fatal("pins must report an unreadable file")
	}
	if err := s.storePins(nil); err == nil {
		t.Fatal("storing pins must report a failed rename")
	}
	if _, err := s.Get(ctx, SuggestGetRequest{ID: "x"}); err == nil {
		t.Fatal("get must report an unreadable sources file")
	}
	if err := s.changePins(func(p []suggest.Pin) ([]suggest.Pin, error) { return p, nil }); err == nil {
		t.Fatal("changing pins must report an unreadable file")
	}
}

func TestSuggestAddReportsAFolderItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	s := newSuggestService(t)
	folder := filepath.Join(s.ConfigDir, "herdsman", "suggest")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })
	entry := SuggestAddRequest{ID: "x", Kind: "tip", Title: "T", Tags: []string{"t"}, Body: "b"}
	if _, err := s.Add(context.Background(), entry); err == nil {
		t.Fatal("add must report a folder it cannot write to")
	}
	configDir := filepath.Dir(s.sourcesPath())
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })
	if err := s.storePins(nil); err == nil {
		t.Fatal("storing pins must report a folder it cannot write to")
	}
}

func TestSuggestFailureServesAnUnknownErrorAsInternal(t *testing.T) {
	if got := failureCode(t, suggestFailure(context.Background(), errors.New("broken"))); got != rpc.Internal {
		t.Fatalf("code %v", got)
	}
	exists := fmt.Errorf("write: %w", fs.ErrExist)
	if got := failureCode(t, suggestFailure(context.Background(), exists)); got != rpc.AlreadyExists {
		t.Fatalf("code %v", got)
	}
}

func TestSourceMovesReportAStoreFailure(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	url, commit := catalogRepo(t)
	first := commit("store-one")
	if _, err := s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url}); err != nil {
		t.Fatal(err)
	}
	second := commit("store-two")
	if _, err := s.SourcePin(ctx, SuggestSourcePinRequest{URL: url, From: first, To: "main"}); err == nil {
		t.Fatal("a pin to a branch name must be refused")
	}
	if _, err := s.SourcePreview(ctx, SuggestSourcePreviewRequest{URL: url, Ref: "-bad"}); err == nil {
		t.Fatal("a bad ref must be refused")
	}
	if _, err := s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url + "-other", Ref: "-bad"}); err == nil {
		t.Fatal("a bad ref must be refused on add")
	}
	if _, err := s.SourcePin(ctx, SuggestSourcePinRequest{URL: url, From: first, To: second}); err != nil {
		t.Fatal(err)
	}
}

func TestASkillFolderWithoutGitStillCountsAsInstalled(t *testing.T) {
	s := newSuggestService(t)
	if err := os.MkdirAll(filepath.Join(s.Home, ".claude", "skills", "by-hand"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := s.installed()
	if !slices.Equal(found.IDs, []string{"by-hand"}) || len(found.Repos) != 0 {
		t.Fatalf("installed = %+v", found)
	}
}
