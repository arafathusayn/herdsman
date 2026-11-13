package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/project"
	"github.com/arafathusayn/herdsman/internal/suggest"
	suggestv1 "github.com/arafathusayn/herdsman/sdk/suggest/v1"
)

const boundConfig = "[project]\nid = \"01JC3Q4D8W2X6Y1Z3A5B7C9E0F\"\nname = \"acme\"\n"

func writeProjectFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// boundContextRepo is a folder bound to a project, laid out as a context repo.
func boundContextRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(root, project.ConfigFile), boundConfig)
	for _, folder := range []string{"events", "suggest", "knowledge"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func event(t *testing.T, root, id, kind string) {
	t.Helper()
	writeProjectFile(t, filepath.Join(root, "events", id+".md"), "---\nid: "+id+"\nkind: "+kind+"\nat: 2026-01-01T00:00:00Z\nactor: a\nparents: []\n---\nbody\n")
}

func TestProductionFindsTheBoundContextRepoFromASubfolder(t *testing.T) {
	root := boundContextRepo(t)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := projectContextDir(nil, nested); got != root {
		t.Fatalf("got %q, want %q", got, root)
	}
}

func TestABoundFolderWithoutTheLayoutHasNoContextRepo(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(root, project.ConfigFile), boundConfig)
	if got := projectContextDir(nil, root); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := projectContextDir(nil, "relative"); got != "" {
		t.Fatalf("a relative folder: %q", got)
	}
	unbound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := projectContextDir(nil, unbound); got != "" {
		t.Fatalf("an unbound folder: %q", got)
	}
}

func TestProductionAddSharesAnEntryThroughTheContextRepo(t *testing.T) {
	root := boundContextRepo(t)
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	service, ctx := NewSuggestService(), context.Background()
	added, err := service.Add(ctx, &suggestv1.AddRequest{Dir: root, Project: true, Id: "shared", Kind: "tip", Title: "Shared", Tags: []string{"one"}, Body: "b"})
	if err != nil || added.GetPath() != filepath.Join(root, "suggest", "shared.md") {
		t.Fatalf("%v %v", added, err)
	}
	got, err := service.Get(ctx, &suggestv1.GetRequest{Dir: root, Id: "shared"})
	if err != nil || got.GetEntry().GetSource().GetKind() != string(suggest.SourceProject) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestRankingUsesTheProjectsLabelsAndRecentEvents(t *testing.T) {
	root := boundContextRepo(t)
	event(t, root, "01KB1A00000000000000000001", "merge.landed")
	s := newSuggestService(t)
	s.ContextDir = func(dir string) string { return projectContextDir(nil, dir) }
	s.Labels = func(string) []string { return []string{"database"} }
	writeUserEntry(t, s, "by-label", "")
	entry := filepath.Join(s.ConfigDir, "herdsman", "suggest", "by-label.md")
	data, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte(strings.Replace(string(data), "tags: [", "tags: [database, merge.landed, ", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := s.Search(context.Background(), SuggestSearchRequest{Dir: root, Text: "by-label"})
	if err != nil || len(found.Items) == 0 {
		t.Fatalf("%+v %v", found, err)
	}
	codes := map[string]bool{}
	for _, reason := range found.Items[0].Reasons {
		codes[reason.Code+"="+reason.Value] = true
	}
	if !codes[suggest.ReasonLabel+"=database"] {
		t.Errorf("no label reason in %v", codes)
	}
	if !codes[suggest.ReasonEvent+"=merge.landed"] {
		t.Errorf("no event reason in %v", codes)
	}
}

func TestProjectLabelsComeFromTheAccountStore(t *testing.T) {
	h := newHarness(t)
	records := project.New(h.d.acctDB, h.d.ops)
	created, err := records.CreateProject(project.ProjectInput{Name: "acme", Repos: []project.Repo{{Name: "api", Path: t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, labels := range [][]string{{"b", "a"}, {"a"}} {
		if _, err := records.CreateTask(project.TaskInput{ProjectID: created.ID, Title: "T " + labels[0], Labels: labels}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, filepath.Join(root, project.ConfigFile), "[project]\nid = \""+created.ID+"\"\nname = \"acme\"\n")
	if got := strings.Join(projectLabels(h.d.acctDB, root), ","); got != "a,b" {
		t.Fatalf("labels = %q", got)
	}
	if got := projectLabels(nil, root); got != nil {
		t.Fatalf("no store: %v", got)
	}
	if got := projectLabels(h.d.acctDB, t.TempDir()); got != nil {
		t.Fatalf("an unbound folder: %v", got)
	}
}

func TestRecentEventsAreTheNewestKinds(t *testing.T) {
	root := boundContextRepo(t)
	for i := range recentEventCount + 5 {
		id := "01KB1A" + strings.Repeat("0", 18) + string(rune('A'+i/26)) + string(rune('A'+i%26))
		kind := "old.kind"
		if i >= 5 {
			kind = "new.kind"
		}
		event(t, root, id, kind)
	}
	writeProjectFile(t, filepath.Join(root, "events", ".gitkeep"), "")
	writeProjectFile(t, filepath.Join(root, "events", "01KZZZ00000000000000000000.md"), "no header\n")
	kinds := recentEventKinds(root)
	if len(kinds) != recentEventCount-1 || strings.Contains(strings.Join(kinds, ","), "old.kind") {
		t.Fatalf("%d kinds: %v", len(kinds), kinds)
	}
	if got := recentEventKinds(t.TempDir()); got != nil {
		t.Fatalf("no events folder: %v", got)
	}
	if got := eventKind(filepath.Join(root, "absent.md")); got != "" {
		t.Fatal(got)
	}
	headerless := filepath.Join(root, "events", "headerless.md")
	writeProjectFile(t, headerless, "---\nid: x\n---\nkind: body.text\n")
	if got := eventKind(headerless); got != "" {
		t.Fatalf("read the body: %q", got)
	}
}

func pinLine(url string) string { return url + "@" + strings.Repeat("a", 40) }

func TestConfiguredSourcesAreLoadedInTheirPrecedence(t *testing.T) {
	s := newSuggestService(t)
	writeProjectFile(t, filepath.Join(s.configDir(), "config.toml"), "[suggest]\nsources = [\""+pinLine("https://example.com/user")+"\"]\n")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Sources(context.Background(), SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != 1 || listed.Sources[0].URL != "https://example.com/user" {
		t.Fatalf("%+v %v", listed, err)
	}
	nested := filepath.Join(repo, "deep")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repositoryRoot(nested); got != repo {
		t.Fatalf("root = %q", got)
	}
	if got := repositoryRoot("relative"); got != "" {
		t.Fatalf("relative root = %q", got)
	}
}

func TestAConfiguredSourceThatIsNotPinnedIsReported(t *testing.T) {
	s := newSuggestService(t)
	writeProjectFile(t, filepath.Join(s.configDir(), "config.toml"), "[suggest]\nsources = [\"https://example.com/a\"]\n")
	if _, err := s.Sources(context.Background(), SuggestSourcesRequest{}); err == nil || !strings.Contains(err.Error(), "suggest.sources") {
		t.Fatalf("%v", err)
	}
	if _, err := s.changeablePins(); err == nil {
		t.Fatal("a change must refuse a damaged configuration")
	}
}

func TestSourceChangesUpdateTheUsersConfiguration(t *testing.T) {
	s := newSuggestService(t)
	ctx := context.Background()
	url, commit := catalogRepo(t)
	first := commit("one")
	added, err := s.SourceAdd(ctx, SuggestSourceAddRequest{URL: url})
	if err != nil || added.Pin.Commit != first {
		t.Fatalf("%+v %v", added, err)
	}
	path := filepath.Join(s.configDir(), "config.toml")
	text, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(text), first) {
		t.Fatalf("%s %v", text, err)
	}
	second := commit("two")
	if _, err := s.SourcePin(ctx, SuggestSourcePinRequest{URL: url, From: first, To: second}); err != nil {
		t.Fatal(err)
	}
	listed, err := s.Sources(ctx, SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != 1 || listed.Sources[0].Commit != second {
		t.Fatalf("%+v %v", listed, err)
	}
	if _, err := s.SourceRemove(ctx, SuggestSourceRemoveRequest{URL: url}); err != nil {
		t.Fatal(err)
	}
	if text, err = os.ReadFile(path); err != nil || strings.Contains(string(text), url) {
		t.Fatalf("%s %v", text, err)
	}
}

func TestAnOlderSourcesFileMovesIntoTheConfiguration(t *testing.T) {
	s := newSuggestService(t)
	older := pinLine("https://example.com/older")
	writeProjectFile(t, s.sourcesPath(), older+"\n")
	listed, err := s.Sources(context.Background(), SuggestSourcesRequest{})
	if err != nil || len(listed.Sources) != 1 {
		t.Fatalf("%+v %v", listed, err)
	}
	err = s.changePins(func(current []suggest.Pin) ([]suggest.Pin, error) { return current, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.sourcesPath()); !os.IsNotExist(err) {
		t.Fatalf("the older file stayed: %v", err)
	}
	text, err := os.ReadFile(filepath.Join(s.configDir(), "config.toml"))
	if err != nil || !strings.Contains(string(text), older) {
		t.Fatalf("%s %v", text, err)
	}
}

func TestAPinWithACommaIsRefused(t *testing.T) {
	s := newSuggestService(t)
	pin := suggest.Pin{URL: "https://example.com/a,b", Commit: strings.Repeat("a", 40)}
	if err := s.storePins([]suggest.Pin{pin}); err == nil || !strings.Contains(err.Error(), "comma") {
		t.Fatalf("%v", err)
	}
}

func TestMergedPinsKeepTheStrongerSource(t *testing.T) {
	strong := suggest.Pin{URL: "u", Commit: "1"}
	weak := suggest.Pin{URL: "u", Commit: "2"}
	if got := mergePins([]suggest.Pin{strong}, []suggest.Pin{weak}); len(got) != 1 || got[0] != strong {
		t.Fatalf("%v", got)
	}
}

func TestAnOrdinaryBoundFolderFindsTheProjectsContextRepo(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	records := project.New(db, ops.NewEngine(db, ops.Options{Effects: project.Effects(db)}))
	created, err := records.CreateProject(project.ProjectInput{Name: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	folder := func() string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := records.Bind(created.ID, dir); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	ordinary, plain, shared := folder(), folder(), folder()
	for _, sub := range []string{"events", "suggest"} {
		if err := os.Mkdir(filepath.Join(shared, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mark := filepath.Join(shared, project.ConfigFile)
	text, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectContextDir(db, ordinary); got != "" {
		t.Fatalf("an unmarked folder was taken: %q", got)
	}
	writeProjectFile(t, mark, strings.Replace(string(text), "[project]\n", "[project]\ncontext = true\n", 1))
	if got := projectContextDir(db, ordinary); got != shared {
		t.Fatalf("got %q, want %q", got, shared)
	}
	if got := projectContextDir(nil, plain); got != "" {
		t.Fatalf("no store: %q", got)
	}
	event(t, shared, "01KB1A00000000000000000001", "merge.landed")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	service := &suggestRPC{s: productionSuggest(func() store.Conn { return db })}
	added, err := service.Add(context.Background(), &suggestv1.AddRequest{Dir: ordinary, Project: true, Id: "shared", Kind: "tip", Title: "Shared", Tags: []string{"one"}, Body: "b"})
	if err != nil || added.GetPath() != filepath.Join(shared, "suggest", "shared.md") {
		t.Fatalf("%v %v", added, err)
	}
	if kinds := service.s.machine(ordinary).RecentEvents; len(kinds) != 1 || kinds[0] != "merge.landed" {
		t.Fatalf("%v", kinds)
	}
}
