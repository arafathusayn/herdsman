package suggest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const validEntry = `---
# a comment
id: sample
kind: mcp
title: "A: quoted \"title\"\t\\ \n" # trailing
tags: [one, 'two', "three"] # list comment
url: https://example.com/x
repo: git@github.com:acme/repo.git
applies_to:
  harnesses: [claude]
  ecosystems: [go]
  platforms: [darwin, linux]
  later: ignored
install:
  mcp: |
    {"a": 1}

      nested
  touches: [~/.claude.json]
since: 1.2.3
future_key: value
future_map:
  deeper: 1
---

The body.

`

func TestParseEntryReadsEveryField(t *testing.T) {
	e, err := ParseEntry([]byte(strings.ReplaceAll(validEntry, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{
		ID: "sample", Kind: KindMCP, Title: "A: quoted \"title\"\t\\ \n",
		Tags: []string{"one", "two", "three"}, URL: "https://example.com/x",
		Repo:      "git@github.com:acme/repo.git",
		AppliesTo: AppliesTo{Harnesses: []string{"claude"}, Ecosystems: []string{"go"}, Platforms: []string{"darwin", "linux"}},
		Install:   &Install{MCP: "{\"a\": 1}\n\n  nested", Touches: []string{"~/.claude.json"}},
		Since:     "1.2.3", Body: "The body.",
	}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("got %+v\nwant %+v", e, want)
	}
}

func TestParseEntryAcceptsSmallForms(t *testing.T) {
	for name, src := range map[string]string{
		"closing at end": "---\nid: a\n---",
		"no keys":        "---\n---\nbody",
		"empty map":      "---\napplies_to:\nid: a\n---\n",
		"empty list":     "---\ntags: []\n---\n",
		"single quote":   "---\ntitle: 'it''s'\n---\n",
		"block at end":   "---\ninstall:\n  mcp: |\n    x\n\n---\nb",
		"comment child":  "---\ninstall:\n  # c\n  command: x\n---\n",
	} {
		if _, err := ParseEntry([]byte(src)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	e, err := ParseEntry([]byte("---\ntitle: 'it''s'\nid: a # comment\n---\n"))
	if err != nil || e.Title != "it's" || e.ID != "a" {
		t.Fatalf("%q %q %v", e.Title, e.ID, err)
	}
}

func TestParseEntryRefusesHostileInput(t *testing.T) {
	deep := "---\n"
	for i := 0; i < maxDepth+2; i++ {
		deep += strings.Repeat("  ", i) + "k:\n"
	}
	deep += strings.Repeat("  ", maxDepth+2) + "k: v\n---\n"
	many := "---\ntags: [" + strings.Repeat("a,", MaxListItems) + "a]\n---\n"
	for name, c := range map[string]struct{ src, want string }{
		"dash line":     {"---\nid: a\n---x\n---\n", "key"},
		"tab in child":  {"---\ninstall:\n\tx: y\n---\n", "tab"},
		"cut escape":    {"---\nid: \"a\\", "closing"},
		"end escape":    {"---\nid: \"a\\\n---\n", "unterminated"},
		"too large":     {strings.Repeat("a", MaxEntryBytes+1), "larger than"},
		"bad utf8":      {"---\nid: \xff\n---\n", "UTF-8"},
		"escape":        {"---\nid: a\n---\n\x1b[2J", "control"},
		"c1":            {"---\nid: a\n---\n\u0085", "control"},
		"bidi":          {"---\nid: a\n---\n\u202e", "control"},
		"no open":       {"id: a\n", "front matter"},
		"no close":      {"---\nid: a\n", "closing"},
		"tab indent":    {"---\n\tid: a\n---\n", "tab"},
		"bad indent":    {"---\nid: a\n  x: b\n---\n", "indent"},
		"no colon":      {"---\nid\n---\n", "key"},
		"bad key":       {"---\nI D: a\n---\n", "key"},
		"duplicate":     {"---\nid: a\nid: b\n---\n", "duplicate"},
		"too deep":      {deep, "deep"},
		"anchor":        {"---\nid: &a b\n---\n", "not supported"},
		"alias":         {"---\nid: *a\n---\n", "not supported"},
		"tag":           {"---\nid: !!x b\n---\n", "not supported"},
		"flow map":      {"---\nid: {a: b}\n---\n", "not supported"},
		"plain colon":   {"---\nid: a: b\n---\n", "quote"},
		"open dq":       {"---\nid: \"a\n---\n", "unterminated"},
		"open sq":       {"---\nid: 'a\n---\n", "unterminated"},
		"bad escape":    {"---\nid: \"\\q\"\n---\n", "escape"},
		"after quote":   {"---\nid: \"a\" b\n---\n", "after"},
		"after squote":  {"---\nid: 'a' b\n---\n", "after"},
		"open list":     {"---\ntags: [a\n---\n", "unterminated"},
		"open list dq":  {"---\ntags: [\"a\\\"]\n---\n", "unterminated"},
		"after list":    {"---\ntags: [a] b\n---\n", "after"},
		"empty item":    {"---\ntags: [a,,b]\n---\n", "empty"},
		"nested list":   {"---\ntags: [[a]]\n---\n", "not supported"},
		"long list":     {many, "more than"},
		"list as text":  {"---\nid: [a]\n---\n", "must be a single value"},
		"map as text":   {"---\nid:\n  a: b\n---\n", "must be a single value"},
		"text as list":  {"---\ntags: a\n---\n", "must be a list"},
		"text as map":   {"---\napplies_to: a\n---\n", "must be a map"},
		"text install":  {"---\ninstall: a\n---\n", "must be a map"},
		"bad child":     {"---\ninstall:\n  k: 'a\n---\n", "unterminated"},
		"bad applies":   {"---\napplies_to:\n  harnesses: a\n---\n", "must be a list"},
		"bad touches":   {"---\ninstall:\n  touches: a\n---\n", "must be a list"},
		"bad after map": {"---\ninstall:\n  k: a\n bad: b\n---\n", "indent"},
	} {
		_, err := ParseEntry([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func goodEntry() Entry {
	return Entry{ID: "a-1", Kind: KindTip, Title: "T", Tags: []string{"x"}, Since: "1.0.0", Body: "b"}
}

func TestValidateAcceptsCompleteEntries(t *testing.T) {
	e := goodEntry()
	e.URL, e.Repo = "http://a.example/x", "https://github.com/a/b"
	e.AppliesTo = AppliesTo{Harnesses: []string{"claude"}}
	e.Install = &Install{SkillRepo: "ssh://git@a.example/b", Touches: []string{"x"}}
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	for _, k := range []Kind{KindTip, KindMCP, KindSkill, KindPlugin, KindMod, KindRepo} {
		e.Kind = k
		if err := Validate(e); err != nil {
			t.Fatal(k, err)
		}
	}
}

func TestValidateReportsEveryRuleBroken(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*Entry)
		want string
	}{
		"id":         {func(e *Entry) { e.ID = "../x" }, "id"},
		"kind":       {func(e *Entry) { e.Kind = "game" }, "kind"},
		"title":      {func(e *Entry) { e.Title = " " }, "title"},
		"tags":       {func(e *Entry) { e.Tags = nil }, "tags"},
		"bad tag":    {func(e *Entry) { e.Tags = []string{"A b"} }, "tag"},
		"since":      {func(e *Entry) { e.Since = "1.0" }, "since"},
		"body":       {func(e *Entry) { e.Body = "\n" }, "body"},
		"url":        {func(e *Entry) { e.URL = "javascript:x" }, "url"},
		"url host":   {func(e *Entry) { e.URL = "https://" }, "url"},
		"url parse":  {func(e *Entry) { e.URL = "https://a b%" }, "url"},
		"repo":       {func(e *Entry) { e.Repo = "ext::sh -c x" }, "repo"},
		"file repo":  {func(e *Entry) { e.Repo = "file:///x" }, "repo"},
		"applies":    {func(e *Entry) { e.AppliesTo.Ecosystems = []string{"Go"} }, "applies_to"},
		"no recipe":  {func(e *Entry) { e.Install = &Install{Touches: []string{"x"}} }, "exactly one"},
		"two recipe": {func(e *Entry) { e.Install = &Install{Command: "a", MCP: "b", Touches: []string{"x"}} }, "exactly one"},
		"skill repo": {func(e *Entry) { e.Install = &Install{SkillRepo: "-x", Touches: []string{"x"}} }, "skill_repo"},
		"touches":    {func(e *Entry) { e.Install = &Install{Command: "a"} }, "touches"},
	} {
		e := goodEntry()
		c.mut(&e)
		err := Validate(e)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
	e := goodEntry()
	e.ID, e.Title = "", ""
	if err := Validate(e); err == nil || !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "title") {
		t.Fatalf("want both rules, got %v", err)
	}
}

func entryText(id, title string) string {
	return fmt.Sprintf("---\nid: %s\nkind: tip\ntitle: %s\ntags: [x]\nsince: 1.0.0\n---\nbody of %s\n", id, title, id)
}

func memSource(kind SourceKind, name string, files map[string]string) Source {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return FSSource(kind, name, m)
}

func TestLoadAppliesPrecedenceWhateverTheArgumentOrder(t *testing.T) {
	bundled := memSource(SourceBundled, "bundled", map[string]string{"a.md": entryText("a", "bundled"), "b.md": entryText("b", "bundled"), "c.md": entryText("c", "bundled"), "d.md": entryText("d", "bundled")})
	remote1 := memSource(SourceRemote, "r1", map[string]string{"a.md": entryText("a", "r1"), "b.md": entryText("b", "r1"), "c.md": entryText("c", "r1")})
	remote2 := memSource(SourceRemote, "r2", map[string]string{"c.md": entryText("c", "r2")})
	user := memSource(SourceUser, "user", map[string]string{"a.md": entryText("a", "user"), "b.md": entryText("b", "user")})
	project := memSource(SourceProject, "project", map[string]string{"a.md": entryText("a", "project")})

	cat := Load(bundled, remote1, remote2, user, project)
	got := map[string]string{}
	var ids []string
	for _, e := range cat.Entries {
		got[e.ID] = e.Title + "/" + e.Source.Name
		ids = append(ids, e.ID)
	}
	want := map[string]string{"a": "project/project", "b": "user/user", "c": "r1/r1", "d": "bundled/bundled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c", "d"}) {
		t.Fatalf("order %v", ids)
	}
	if len(cat.Shadowed) != 7 || cat.Shadowed[0].ID != "a" || cat.Shadowed[0].Winner.Name != "project" {
		t.Fatalf("shadowed %+v", cat.Shadowed)
	}
	if len(cat.Problems) != 0 {
		t.Fatalf("problems %+v", cat.Problems)
	}
	if got := SourceKind("other").rank(); got <= SourceBundled.rank() {
		t.Fatalf("unknown kind ranks %d", got)
	}
}

func TestLoadSkipsBadFilesAndKeepsTheRest(t *testing.T) {
	huge := "---\nid: huge\n---\n" + strings.Repeat("x", MaxEntryBytes)
	cat := Load(memSource(SourceUser, "user", map[string]string{
		"good.md":     entryText("good", "G"),
		"wrong.md":    entryText("right", "R"),
		"broken.md":   "nope",
		"invalid.md":  "---\nid: invalid\n---\n",
		"huge.md":     huge,
		"notes.txt":   "ignored",
		"sub/deep.md": entryText("deep", "D"),
	}))
	if len(cat.Entries) != 1 || cat.Entries[0].ID != "good" {
		t.Fatalf("entries %+v", cat.Entries)
	}
	var files []string
	for _, p := range cat.Problems {
		files = append(files, p.File)
		if p.Err == "" || p.Source.Name != "user" {
			t.Fatalf("problem %+v", p)
		}
	}
	if !reflect.DeepEqual(files, []string{"broken.md", "huge.md", "invalid.md", "wrong.md"}) {
		t.Fatalf("problems %v", files)
	}
}

func TestLoadCapsTheEntriesOfOneSource(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= MaxSourceEntries; i++ {
		id := fmt.Sprintf("e%05d", i)
		files[id+".md"] = entryText(id, "T")
	}
	cat := Load(memSource(SourceUser, "user", files))
	if len(cat.Entries) != MaxSourceEntries || len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Err, "more than") {
		t.Fatalf("%d entries, problems %+v", len(cat.Entries), cat.Problems)
	}
}

func TestDirSourceSkipsSymlinksAndToleratesAMissingFolder(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(secret, []byte(entryText("link", "secret")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.md"), []byte(entryText("ok", "O")), 0o600); err != nil {
		t.Fatal(err)
	}
	cat := Load(DirSource(SourceUser, "user", dir), DirSource(SourceProject, "project", filepath.Join(dir, "absent")))
	if len(cat.Entries) != 1 || cat.Entries[0].ID != "ok" {
		t.Fatalf("entries %+v", cat.Entries)
	}
	if len(cat.Problems) != 1 || cat.Problems[0].File != "link.md" || !strings.Contains(cat.Problems[0].Err, "regular") {
		t.Fatalf("problems %+v", cat.Problems)
	}
}

type failFS struct {
	readDir error
	open    error
	read    error
}

type failFile struct{ err error }

func (f failFile) Stat() (fs.FileInfo, error) { return nil, f.err }
func (f failFile) Read([]byte) (int, error)   { return 0, f.err }
func (f failFile) Close() error               { return nil }

func (f failFS) Open(name string) (fs.File, error) {
	if f.open != nil {
		return nil, f.open
	}
	return failFile{f.read}, nil
}

func (f failFS) ReadDir(string) ([]fs.DirEntry, error) {
	if f.readDir != nil {
		return nil, f.readDir
	}
	m := fstest.MapFS{"a.md": &fstest.MapFile{}}
	return m.ReadDir(".")
}

func TestLoadReportsFilesystemErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, fsys := range map[string]failFS{"readdir": {readDir: boom}, "open": {open: boom}, "read": {read: boom}} {
		cat := Load(FSSource(SourceUser, "u", fsys))
		if len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Err, "boom") {
			t.Errorf("%s: %+v", name, cat.Problems)
		}
	}
}

func TestBundledCatalogIsValid(t *testing.T) {
	if err := ValidateBundled(); err != nil {
		t.Fatal(err)
	}
	cat := Load(Bundled())
	if len(cat.Entries) == 0 || cat.Entries[0].Source.Kind != SourceBundled {
		t.Fatalf("%+v", cat.Entries)
	}
}

func TestValidateFSReportsEveryBadFile(t *testing.T) {
	err := validateFS(fstest.MapFS{"a.md": {Data: []byte("nope")}, "b.md": {Data: []byte("nope")}})
	if err == nil || !strings.Contains(err.Error(), "a.md") || !strings.Contains(err.Error(), "b.md") {
		t.Fatalf("got %v", err)
	}
	if err := validateFS(fstest.MapFS{}); err == nil || !strings.Contains(err.Error(), "no entries") {
		t.Fatalf("empty catalog: %v", err)
	}
}

type machineFixture struct {
	Machine  Machine  `json:"machine"`
	Want     []string `json:"want"`
	Excluded []string `json:"excluded"`
}

func summarize(r Ranking) (items, excluded []string) {
	items, excluded = []string{}, []string{}
	for _, s := range r.Items {
		items = append(items, fmt.Sprintf("%s %d", s.Entry.ID, s.Score))
	}
	for _, x := range r.Excluded {
		excluded = append(excluded, x.ID+" "+x.Reason.Code)
	}
	return items, excluded
}

func TestRankIsDeterministicOnFixtureMachines(t *testing.T) {
	cat := Load(DirSource(SourceBundled, "fixture", "testdata/machines/catalog"))
	if len(cat.Problems) != 0 || len(cat.Entries) != 7 {
		t.Fatalf("%d entries, %+v", len(cat.Entries), cat.Problems)
	}
	paths, err := filepath.Glob("testdata/machines/*.json")
	if err != nil || len(paths) != 3 {
		t.Fatalf("%v %v", paths, err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var fx machineFixture
		if err := json.Unmarshal(b, &fx); err != nil {
			t.Fatal(err)
		}
		first, _ := json.Marshal(Rank(cat.Entries, fx.Machine, Query{}))
		rng := rand.New(rand.NewSource(1))
		for range 50 {
			shuffled := append([]Entry(nil), cat.Entries...)
			rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			r := Rank(shuffled, fx.Machine, Query{})
			again, _ := json.Marshal(r)
			if !bytes.Equal(first, again) {
				t.Fatalf("%s: order depends on input", p)
			}
			items, excluded := summarize(r)
			if !reflect.DeepEqual(items, fx.Want) || !reflect.DeepEqual(excluded, fx.Excluded) {
				t.Fatalf("%s:\nitems %v\nwant  %v\nexcl  %v\nwant  %v", p, items, fx.Want, excluded, fx.Excluded)
			}
		}
	}
}

func TestRankExplainsEveryPoint(t *testing.T) {
	cat := Load(DirSource(SourceBundled, "fixture", "testdata/machines/catalog"))
	m := Machine{Platform: "darwin", Harnesses: []string{"claude"}, Ecosystems: []string{"go"}, Labels: []string{"testing", "review"}, RecentEvents: []string{"review"}}
	for _, s := range Rank(cat.Entries, m, Query{}).Items {
		sum := 0
		for _, r := range s.Reasons {
			sum += r.Points
		}
		if sum != s.Score || len(s.Reasons) == 0 {
			t.Fatalf("%s: score %d, reasons %+v", s.Entry.ID, s.Score, s.Reasons)
		}
	}
	top := Rank(cat.Entries, m, Query{}).Items[0]
	want := []Reason{{ReasonHarness, "claude", PointsHarness}, {ReasonLabel, "review", PointsLabel}, {ReasonEvent, "review", PointsEvent}}
	if top.Entry.ID != "claude-only" || !reflect.DeepEqual(top.Reasons, want) {
		t.Fatalf("%s %+v", top.Entry.ID, top.Reasons)
	}
	general := Rank(cat.Entries, Machine{}, Query{}).Items[0]
	if !reflect.DeepEqual(general.Reasons, []Reason{{ReasonGeneral, "", 0}}) {
		t.Fatalf("%+v", general.Reasons)
	}
}

func TestRankBreaksTiesBySourceThenID(t *testing.T) {
	es := []Entry{
		{ID: "b", Source: SourceRef{Kind: SourceProject}},
		{ID: "c", Source: SourceRef{Kind: SourceBundled}},
		{ID: "a", Source: SourceRef{Kind: SourceBundled}},
		{ID: "d", Source: SourceRef{Kind: SourceProject}},
	}
	items, _ := summarize(Rank(es, Machine{}, Query{}))
	if !reflect.DeepEqual(items, []string{"b 0", "d 0", "a 0", "c 0"}) {
		t.Fatalf("%v", items)
	}
}

func TestRankFiltersAndSearches(t *testing.T) {
	cat := Load(DirSource(SourceBundled, "fixture", "testdata/machines/catalog"))
	for name, c := range map[string]struct {
		m    Machine
		q    Query
		want []string
	}{
		"kind":     {Machine{}, Query{Kind: "skill"}, []string{"skill-repo 0"}},
		"tag":      {Machine{}, Query{Tag: "CI"}, []string{"go-tests 0"}},
		"for":      {Machine{Harnesses: []string{"claude"}}, Query{For: "codex", Tag: "agents"}, []string{"codex-only 30", "skill-repo 30"}},
		"title":    {Machine{}, Query{Text: "serial"}, []string{"go-tests 8"}},
		"tag term": {Machine{}, Query{Text: "ci"}, []string{"go-tests 4"}},
		"body":     {Machine{}, Query{Text: "resume"}, []string{"claude-only 1"}},
		"all":      {Machine{}, Query{Text: "serial package"}, []string{"go-tests 9"}},
		"miss":     {Machine{}, Query{Text: "serial nothing"}, []string{}},
	} {
		items, _ := summarize(Rank(cat.Entries, c.m, c.q))
		if !reflect.DeepEqual(items, c.want) {
			t.Errorf("%s: %v want %v", name, items, c.want)
		}
	}
}

func FuzzParseEntry(f *testing.F) {
	f.Add([]byte(validEntry))
	f.Add([]byte("---\ntags: [a, \"b\"]\n---\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := ParseEntry(data)
		if err != nil {
			return
		}
		_ = Validate(e)
	})
}

// gitFixture is a local repository reached through file://, so no test uses
// the network.
type gitFixture struct {
	t   *testing.T
	dir string
	url string
}

func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.com")
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	return newGitFixtureFormat(t, "sha1")
}

func newGitFixtureFormat(t *testing.T, format string) *gitFixture {
	t.Helper()
	isolateGit(t)
	g := &gitFixture{t: t, dir: t.TempDir()}
	g.url = "file://" + g.dir
	g.git("init", "--quiet", "--initial-branch=main", "--object-format="+format)
	g.git("config", "uploadpack.allowAnySHA1InWant", "true")
	return g
}

func (g *gitFixture) git(args ...string) string {
	g.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitFixture) write(files map[string]string) string {
	g.t.Helper()
	for name, body := range files {
		p := filepath.Join(g.dir, RemoteFolder, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			g.t.Fatal(err)
		}
	}
	g.git("add", "-A")
	g.git("commit", "--quiet", "--allow-empty", "-m", "c")
	return g.git("rev-parse", "HEAD")
}

func TestFetchPinsTheResolvedCommitAndServesItFromTheCache(t *testing.T) {
	g := newGitFixture(t)
	first := g.write(map[string]string{"one.md": entryText("one", "First")})
	g.git("tag", "v1")
	second := g.write(map[string]string{"two.md": entryText("two", "Second")})

	r := NewRemote(t.TempDir())
	ctx := context.Background()
	for ref, want := range map[string]string{"": second, "HEAD": second, "main": second, "v1": first, first: first} {
		pin, err := r.Fetch(ctx, g.url, ref)
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		if pin.Commit != want || pin.URL != g.url {
			t.Fatalf("ref %q: %+v want %s", ref, pin, want)
		}
	}

	pin := Pin{URL: g.url, Commit: first}
	g.write(map[string]string{"one.md": entryText("one", "Moved")})
	if err := os.RemoveAll(g.dir); err != nil {
		t.Fatal(err)
	}
	// The origin is gone: a pinned read can only have come from the cache.
	cat := Load(r.Source(ctx, pin))
	if len(cat.Problems) != 0 || len(cat.Entries) != 1 || cat.Entries[0].Title != "First" {
		t.Fatalf("%+v", cat)
	}
	if got := cat.Entries[0].Source; got.Kind != SourceRemote || got.Commit != first || got.Name != g.url {
		t.Fatalf("source %+v", got)
	}
}

func TestPinnedCommitSurvivesGarbageCollection(t *testing.T) {
	g := newGitFixture(t)
	first := g.write(map[string]string{"one.md": entryText("one", "First")})
	g.write(map[string]string{"one.md": entryText("one", "Later")})
	cache := t.TempDir()
	r := NewRemote(cache)
	ctx := context.Background()
	if _, err := r.Fetch(ctx, g.url, first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Fetch(ctx, g.url, "main"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "--git-dir="+r.repoDir(g.url), "gc", "--quiet", "--prune=now")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cat := Load(r.Source(ctx, Pin{URL: g.url, Commit: first}))
	if len(cat.Entries) != 1 || cat.Entries[0].Title != "First" {
		t.Fatalf("%+v", cat)
	}
}

func TestSourceReportsAnUncachedPin(t *testing.T) {
	isolateGit(t)
	r := NewRemote(t.TempDir())
	cat := Load(r.Source(context.Background(), Pin{URL: "https://example.com/a", Commit: strings.Repeat("a", 40)}))
	if len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Err, "not cached") {
		t.Fatalf("%+v", cat.Problems)
	}
}

func TestRemoteTreeIsReadAsDataOnly(t *testing.T) {
	g := newGitFixture(t)
	g.write(map[string]string{"ok.md": entryText("ok", "O"), "notes.txt": "x", "sub/deep.md": entryText("deep", "D"), "big.md": strings.Repeat("x", MaxEntryBytes+1)})
	if err := os.Symlink("/etc/passwd", filepath.Join(g.dir, RemoteFolder, "link.md")); err != nil {
		t.Fatal(err)
	}
	head := g.write(nil)
	r := NewRemote(t.TempDir())
	ctx := context.Background()
	pin, err := r.Fetch(ctx, g.url, head)
	if err != nil {
		t.Fatal(err)
	}
	cat := Load(r.Source(ctx, pin))
	if len(cat.Entries) != 1 || cat.Entries[0].ID != "ok" {
		t.Fatalf("%+v", cat.Entries)
	}
	var files []string
	for _, p := range cat.Problems {
		files = append(files, p.File)
	}
	if !reflect.DeepEqual(files, []string{"big.md", "link.md"}) {
		t.Fatalf("%+v", cat.Problems)
	}
	if entries, _ := os.ReadDir(r.repoDir(g.url)); len(entries) == 0 {
		t.Fatal("no cache")
	}
	if _, err := os.Stat(filepath.Join(r.repoDir(g.url), RemoteFolder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a working tree was written: %v", err)
	}
	if hooks, _ := os.ReadDir(filepath.Join(r.repoDir(g.url), "hooks")); len(hooks) != 0 {
		t.Fatalf("hooks present: %v", hooks)
	}
}

func TestFetchRefusesARepositoryWithoutTheFolder(t *testing.T) {
	g := newGitFixture(t)
	if err := os.WriteFile(filepath.Join(g.dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.git("add", "-A")
	g.git("commit", "--quiet", "-m", "c")
	_, err := NewRemote(t.TempDir()).Fetch(context.Background(), g.url, "")
	if err == nil || !strings.Contains(err.Error(), RemoteFolder) {
		t.Fatalf("got %v", err)
	}
}

func TestFetchRefusesUnsafeInputWithoutRunningGit(t *testing.T) {
	r := NewRemote(t.TempDir())
	r.Git = func(context.Context, []byte, ...string) ([]byte, error) {
		t.Fatal("git ran")
		return nil, nil
	}
	ctx := context.Background()
	for _, u := range []string{"", "-oProxy=x", "ext::sh -c x", "http://a/b", "/local/path", "https://", "https://a b/c", "git@host", "git@-x:y", "ssh://h/a\tb"} {
		if _, err := r.Fetch(ctx, u, ""); err == nil {
			t.Errorf("url %q accepted", u)
		}
	}
	for _, ref := range []string{"-x", "a b", "a:b", "+a", "a..b", ".a"} {
		if _, err := r.Fetch(ctx, "https://example.com/a", ref); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}
}

func TestParsePinRoundTripsAndRefusesLoosePins(t *testing.T) {
	sha := strings.Repeat("a1", 20)
	for _, u := range []string{"https://github.com/acme/cat", "git@github.com:acme/cat.git", "ssh://git@h/a"} {
		p, err := ParsePin(u + "@" + sha)
		if err != nil || p.URL != u || p.Commit != sha || p.String() != u+"@"+sha {
			t.Fatalf("%q: %+v %v", u, p, err)
		}
	}
	for _, s := range []string{"https://a.example/b", "https://a.example/b@9f1c2d3", "https://a.example/b@main", "ext::x@" + sha, "git@github.com:acme/cat.git"} {
		if _, err := ParsePin(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestPreviewUpdateShowsTheDiffAndLeavesThePin(t *testing.T) {
	g := newGitFixture(t)
	from := g.write(map[string]string{"same.md": entryText("same", "S"), "edit.md": entryText("edit", "Old"), "gone.md": entryText("gone", "G")})
	r := NewRemote(t.TempDir())
	ctx := context.Background()
	pin, err := r.Fetch(ctx, g.url, from)
	if err != nil {
		t.Fatal(err)
	}
	g.git("rm", "--quiet", RemoteFolder+"/gone.md")
	to := g.write(map[string]string{"edit.md": entryText("edit", "New"), "new.md": entryText("new", "N"), "bad.md": "nope"})

	up, err := r.PreviewUpdate(ctx, pin, "main")
	if err != nil {
		t.Fatal(err)
	}
	if up.From != pin || up.To.Commit != to {
		t.Fatalf("%+v", up)
	}
	if len(up.Problems) != 1 || up.Problems[0].File != "bad.md" {
		t.Fatalf("%+v", up.Problems)
	}
	var got []string
	for _, c := range up.Changes {
		got = append(got, c.ID+" "+string(c.Action))
	}
	if !reflect.DeepEqual(got, []string{"edit changed", "gone removed", "new added"}) {
		t.Fatalf("%v", got)
	}
	if d := up.Changes[0].Diff; !strings.Contains(d, "- title: Old\n") || !strings.Contains(d, "+ title: New\n") || strings.Contains(d, "kind: tip") {
		t.Fatalf("diff:\n%s", d)
	}
	if d := up.Changes[1].Diff; !strings.Contains(d, "- title: G\n") {
		t.Fatalf("diff:\n%s", d)
	}
	if d := up.Changes[2].Diff; !strings.Contains(d, "+ title: N\n") {
		t.Fatalf("diff:\n%s", d)
	}
	cat := Load(r.Source(ctx, pin))
	titles := map[string]string{}
	for _, e := range cat.Entries {
		titles[e.ID] = e.Title
	}
	if titles["edit"] != "Old" || titles["gone"] != "G" || titles["new"] != "" {
		t.Fatalf("the pin moved: %v", titles)
	}

	same, err := r.PreviewUpdate(ctx, Pin{URL: g.url, Commit: to}, to)
	if err != nil || len(same.Changes) != 0 {
		t.Fatalf("%+v %v", same, err)
	}
}

func TestDiffLinesBoundsHostileInput(t *testing.T) {
	if got := diffLines("a\nb\nc", "a\nx\nc"); got != "- b\n+ x\n" {
		t.Fatalf("%q", got)
	}
	if got := diffLines("a\nb\nc\nd", "b\nd\ne"); got != "- a\n- c\n+ e\n" {
		t.Fatalf("%q", got)
	}
	if got := diffLines("a\nb", "c\nb\nd"); got != "- a\n+ c\n+ d\n" {
		t.Fatalf("%q", got)
	}
	if got := diffLines("x", "x"); got != "" {
		t.Fatalf("%q", got)
	}
	var a, b strings.Builder
	for i := range 1100 { // 1100 squared is above maxDiffCells
		fmt.Fprintf(&a, "a%d\n", i)
		fmt.Fprintf(&b, "b%d\n", i)
	}
	got := diffLines(a.String(), b.String())
	if !strings.HasPrefix(got, "- a0\n") || !strings.Contains(got, "+ b0\n") {
		t.Fatalf("%.40q", got)
	}
}

func TestNormalizeRepoMatchesEveryForm(t *testing.T) {
	want := normalizeRepo("https://github.com/Acme/Cat")
	for _, u := range []string{"git@github.com:acme/cat.git", "ssh://git@github.com/acme/cat/", "https://github.com/acme/cat.git", "https://GitHub.com/ACME/CAT.GIT"} {
		if got := normalizeRepo(u); got != want {
			t.Errorf("%q: %q want %q", u, got, want)
		}
	}
	generic := normalizeRepo("https://git.example/team/Tool")
	for _, u := range []string{"git@git.example:team/Tool.git", "ssh://git@GIT.example/team/Tool/"} {
		if got := normalizeRepo(u); got != generic {
			t.Errorf("%q: %q want %q", u, got, generic)
		}
	}
	if got := normalizeRepo("https://git.example/team/tool"); got == generic {
		t.Errorf("path case folded on a generic host: %q", got)
	}
	if got := normalizeRepo("LocalName"); got != "localname" {
		t.Errorf("a bare host: %q", got)
	}
}

func TestRankKeepsARepositoryThatDiffersOnlyInCaseOnAGenericHost(t *testing.T) {
	entry := func(repo string) []Entry { return []Entry{{ID: "tool", Repo: repo}} }
	installed := Machine{Installed: Installed{Repos: []string{"https://git.example/team/tool"}}}
	if r := Rank(entry("https://git.example/team/Tool"), installed, Query{}); len(r.Excluded) != 0 || len(r.Items) != 1 {
		t.Fatalf("excluded a different repository: %+v", r)
	}
	if r := Rank(entry("https://git.example/team/tool.git"), installed, Query{}); len(r.Excluded) != 1 {
		t.Fatalf("kept the installed repository: %+v", r)
	}
	github := Machine{Installed: Installed{Repos: []string{"https://github.com/team/tool"}}}
	if r := Rank(entry("https://github.com/team/Tool"), github, Query{}); len(r.Excluded) != 1 {
		t.Fatalf("kept the installed repository: %+v", r)
	}
}

func TestPinBoundariesRefuseMovableRevisionsBeforeGitRuns(t *testing.T) {
	r := NewRemote(t.TempDir())
	r.Git = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		t.Fatalf("git ran: %v", args)
		return nil, nil
	}
	ctx := context.Background()
	url, full := "https://example.com/a", strings.Repeat("a", 40)
	for _, pin := range []Pin{
		{url, "FETCH_HEAD"}, {url, "HEAD"}, {url, "main"}, {url, "abc1234"}, {url, ""},
		{url, strings.Repeat("a", 39)}, {url, strings.Repeat("A", 40)}, {url, full + "^{tree}"},
		{"ext::sh -c x", full}, {"", full},
	} {
		cat := Load(r.Source(ctx, pin))
		if len(cat.Problems) != 1 || len(cat.Entries) != 0 {
			t.Errorf("Source accepted %+v: %+v", pin, cat)
		}
		if _, err := r.PreviewUpdate(ctx, pin, ""); err == nil {
			t.Errorf("PreviewUpdate accepted %+v", pin)
		}
	}
}

func TestPinnedReadsAndUpdatesWorkInEveryObjectFormat(t *testing.T) {
	for format, size := range map[string]int{"sha1": 40, "sha256": 64} {
		t.Run(format, func(t *testing.T) {
			g := newGitFixtureFormat(t, format)
			// The user's default must not decide the cache's format.
			other := map[string]string{"sha1": "sha256", "sha256": "sha1"}[format]
			t.Setenv("GIT_DEFAULT_HASH", other)
			first := g.write(map[string]string{"one.md": entryText("one", "First")})
			if len(first) != size {
				t.Fatalf("fixture commit %q", first)
			}
			r := NewRemote(t.TempDir())
			ctx := context.Background()
			pin, err := r.Fetch(ctx, g.url, "main")
			if err != nil {
				t.Fatal(err)
			}
			if pin.Commit != first {
				t.Fatalf("pin %s want %s", pin.Commit, first)
			}
			if _, err := r.Fetch(ctx, g.url, first); err != nil {
				t.Fatalf("by full id: %v", err)
			}
			second := g.write(map[string]string{"one.md": entryText("one", "Second")})
			up, err := r.PreviewUpdate(ctx, pin, "main")
			if err != nil {
				t.Fatal(err)
			}
			if up.To.Commit != second || len(up.Changes) != 1 || !strings.Contains(up.Changes[0].Diff, "+ title: Second\n") {
				t.Fatalf("%+v", up)
			}
			cat := Load(r.Source(ctx, pin))
			if len(cat.Problems) != 0 || len(cat.Entries) != 1 || cat.Entries[0].Title != "First" {
				t.Fatalf("%+v", cat)
			}
		})
	}
}

// stubGit answers each git subcommand from a table, for the failures a real
// repository cannot be made to produce.
func stubGit(t *testing.T, r *Remote, answers map[string]func(args []string) ([]byte, error)) {
	t.Helper()
	r.Git = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		for _, a := range args {
			if f, ok := answers[a]; ok {
				return f(args)
			}
		}
		return nil, nil
	}
}

func ok(s string) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) { return []byte(s), nil }
}

func fail(args []string) ([]byte, error) { return nil, errors.New("boom " + args[1]) }

func TestFetchReportsEveryGitFailure(t *testing.T) {
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	good := map[string]func([]string) ([]byte, error){"ls-remote": ok(sha + "\tHEAD\n"), "init": ok(""), "fetch": ok(""), "rev-parse": ok(sha + "\n"), "update-ref": ok(""), "ls-tree": ok(""), "cat-file": ok("")}
	for name, c := range map[string]struct {
		key  string
		f    func([]string) ([]byte, error)
		ref  string
		want string
	}{
		"ls-remote":  {"ls-remote", fail, "", "boom"},
		"no refs":    {"ls-remote", ok(""), "", "no refs"},
		"odd hash":   {"ls-remote", ok("zz\tHEAD\n"), "", "object format"},
		"init":       {"init", fail, "", "boom"},
		"fetch":      {"fetch", fail, "", "boom"},
		"rev-parse":  {"rev-parse", fail, "", "boom"},
		"not commit": {"rev-parse", ok("zz\n"), "", "commit"},
		"mismatch":   {"rev-parse", ok(sha + "\n"), other, "instead"},
		"update-ref": {"update-ref", fail, "", "boom"},
	} {
		r := NewRemote(t.TempDir())
		answers := map[string]func([]string) ([]byte, error){}
		for k, v := range good {
			answers[k] = v
		}
		answers[c.key] = c.f
		stubGit(t, r, answers)
		if _, err := r.Fetch(context.Background(), "https://example.com/a", c.ref); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	r := NewRemote(filepath.Join(t.TempDir(), "file"))
	if err := os.WriteFile(r.CacheDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stubGit(t, r, good)
	if _, err := r.Fetch(context.Background(), "https://example.com/a", ""); err == nil {
		t.Error("cache under a file accepted")
	}
}

func TestPreviewUpdateReportsFailures(t *testing.T) {
	sha := strings.Repeat("a", 40)
	pin := Pin{URL: "https://example.com/a", Commit: sha}
	r := NewRemote(t.TempDir())
	stubGit(t, r, map[string]func([]string) ([]byte, error){"ls-remote": ok(sha + "\tHEAD\n"), "init": fail})
	if _, err := r.PreviewUpdate(context.Background(), pin, ""); err == nil {
		t.Error("fetch failure hidden")
	}

	r = NewRemote(t.TempDir())
	calls := 0
	stubGit(t, r, map[string]func([]string) ([]byte, error){
		"ls-remote": ok(sha + "\tHEAD\n"), "init": ok(""), "fetch": ok(""), "rev-parse": ok(strings.Repeat("b", 40) + "\n"), "update-ref": ok(""),
		"ls-tree": func(args []string) ([]byte, error) {
			calls++
			if strings.HasPrefix(args[len(args)-1], sha) {
				return nil, errors.New("old pin gone")
			}
			return nil, nil
		},
	})
	if _, err := r.PreviewUpdate(context.Background(), pin, ""); err == nil || !strings.Contains(err.Error(), "not cached") || calls != 2 {
		t.Errorf("%v after %d calls", err, calls)
	}
}

func TestReadCommitRefusesMalformedGitOutput(t *testing.T) {
	sha := strings.Repeat("a", 40)
	pin := Pin{URL: "https://example.com/a", Commit: sha}
	h := strings.Repeat("c", 40)
	line := "100644 blob " + h + " 5\ta.md\x00"
	for name, c := range map[string]struct {
		tree  string
		batch func([]string) ([]byte, error)
		want  string
	}{
		"no tab":       {"100644 blob x 5 a.md\x00", ok(""), "malformed"},
		"few fields":   {"100644 blob\ta.md\x00", ok(""), "malformed"},
		"bad size":     {"100644 blob " + h + " nan\ta.md\x00", ok(""), "malformed"},
		"batch fails":  {line, fail, "boom"},
		"no newline":   {line, ok("nothing"), "malformed"},
		"missing":      {line, ok(h + " missing\n"), "malformed"},
		"bad length":   {line, ok(h + " blob x\nhello\n"), "malformed"},
		"short":        {line, ok(h + " blob 500\nhello\n"), "malformed"},
		"wrong object": {line, ok(strings.Repeat("d", 40) + " blob 5\nhello\n"), "malformed"},
	} {
		r := NewRemote(t.TempDir())
		stubGit(t, r, map[string]func([]string) ([]byte, error){"ls-tree": ok(c.tree), "cat-file": c.batch})
		if _, _, err := r.readCommit(context.Background(), pin); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}

	r := NewRemote(t.TempDir())
	var tree strings.Builder
	var batch strings.Builder
	for i := 0; i <= MaxSourceEntries; i++ {
		tree.WriteString(fmt.Sprintf("100644 blob %s 1\te%05d.md\x00", h, i))
		if i < MaxSourceEntries {
			batch.WriteString(h + " blob 1\nx\n")
		}
	}
	stubGit(t, r, map[string]func([]string) ([]byte, error){"ls-tree": ok(tree.String()), "cat-file": ok(batch.String())})
	files, problems, err := r.readCommit(context.Background(), pin)
	if err != nil || len(files) != MaxSourceEntries || len(problems) != 1 || !strings.Contains(problems[0].Err, "more than") {
		t.Fatalf("%d files, %+v, %v", len(files), problems, err)
	}
}

func TestExecGitLimitsOutputAndCleansTheEnvironment(t *testing.T) {
	isolateGit(t)
	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("KEEP_ME", "1")
	dir := t.TempDir()
	run := execGit(1 << 20)
	ctx := context.Background()
	if _, err := run(ctx, nil, "init", "--quiet", "--bare", dir); err != nil {
		t.Fatal(err)
	}
	out, err := run(ctx, []byte("x"), "--git-dir="+dir, "hash-object", "--stdin")
	if err != nil || len(strings.TrimSpace(string(out))) != 40 {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := execGit(4)(ctx, []byte("x"), "--git-dir="+dir, "hash-object", "--stdin"); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("limit: %v", err)
	}
	if _, err := run(ctx, nil, "--git-dir="+dir, "no-such-command"); err == nil {
		t.Fatal("failure hidden")
	}
	env := gitEnv([]string{"GIT_DIR=x", "GIT_WORK_TREE=x", "GIT_INDEX_FILE=x", "GIT_ALLOW_PROTOCOL=ext", "HOME=/h"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "GIT_DIR") || strings.Contains(joined, "GIT_WORK_TREE") || strings.Contains(joined, "GIT_INDEX_FILE") || strings.Contains(joined, "=ext") || !strings.Contains(joined, "HOME=/h") || !strings.Contains(joined, "GIT_ALLOW_PROTOCOL=file:https:ssh") || !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("%v", env)
	}
}
