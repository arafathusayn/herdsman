package context

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	idA = "01KB1A00000000000000000001"
	idB = "01KB1B00000000000000000002"
	idC = "01KB1C00000000000000000003"
	idD = "01KB1D00000000000000000004"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func event(id, actor, at, parents string) string {
	return "---\nid: " + id + "\nkind: note\nat: " + at + "\nactor: " + actor + "\nparents: [" + parents + "]\n---\nbody\n"
}

func put(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, EventsDir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func codes(r Report) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Code+" "+p.File)
	}
	return out
}

func TestInitLaysOutAndIsRepeatable(t *testing.T) {
	dir := newRepo(t)
	for _, sub := range []string{KnowledgeDir, EventsDir, SuggestDir} {
		if _, err := os.Stat(filepath.Join(dir, sub, keepFile)); err != nil {
			t.Errorf("%s survives a clone only with a placeholder: %v", sub, err)
		}
	}
	readme := filepath.Join(dir, ReadmeFile)
	if err := os.WriteFile(readme, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(readme)
	if err != nil || string(got) != "mine\n" {
		t.Fatalf("a second init changed the README: %q, %v", got, err)
	}
}

func TestInitWritesReadme(t *testing.T) {
	got, err := os.ReadFile(filepath.Join(newRepo(t), ReadmeFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{KnowledgeDir, EventsDir, SuggestDir} {
		if !strings.Contains(string(got), name+"/") {
			t.Errorf("README does not explain %s", name)
		}
	}
}

func TestInitRefuses(t *testing.T) {
	if err := Init(t.TempDir()); !errors.Is(err, ErrNotGitRepo) {
		t.Errorf("a plain folder: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, EventsDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err == nil {
		t.Error("a file where a folder belongs was accepted")
	}
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ReadmeFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err == nil {
		t.Error("a folder where the README belongs was accepted")
	}
}

func TestForkedChronology(t *testing.T) {
	dir := newRepo(t)
	root, err := Append(dir, Input{Kind: KindTaskCreated, Actor: "maintainer", Task: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Parents) != 0 {
		t.Fatalf("the first event has parents: %v", root.Parents)
	}
	north, err := Append(dir, Input{Kind: KindTaskStarted, Actor: "north", Parents: []string{root.ID}, Repos: []string{"core"}})
	if err != nil {
		t.Fatal(err)
	}
	south, err := Append(dir, Input{Kind: KindTaskStarted, Actor: "south", Parents: []string{root.ID}})
	if err != nil {
		t.Fatal(err)
	}
	southNext, err := Append(dir, Input{Kind: KindNote, Actor: "south", Parents: []string{south.ID}, Body: "still going"})
	if err != nil {
		t.Fatal(err)
	}
	join, err := Append(dir, Input{Kind: KindMergeLanded, Actor: "maintainer", Commits: map[string]string{"core": "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{north.ID, southNext.ID}
	slices.Sort(want)
	if !slices.Equal(join.Parents, want) {
		t.Fatalf("the join did not name both branch tips: %v, want %v", join.Parents, want)
	}
	report, err := Check(dir)
	if err != nil || len(report.Problems) != 0 || report.Events != 5 {
		t.Fatalf("check: %+v, %v", report, err)
	}
	c, err := Log(dir, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Events) != 5 || !slices.Equal(c.Heads, []string{join.ID}) {
		t.Fatalf("log: %d events, heads %v", len(c.Events), c.Heads)
	}
	seen := map[string]bool{}
	for _, e := range c.Events {
		for _, p := range e.Parents {
			if !seen[p] {
				t.Errorf("%s came before its parent %s", e.ID, p)
			}
		}
		seen[e.ID] = true
	}
}

func TestAppendStaysMonotoneWhenTheClockGoesBack(t *testing.T) {
	dir := newRepo(t)
	late := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	first, err := Append(dir, Input{Kind: KindNote, Actor: "a", Now: late})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Append(dir, Input{Kind: KindNote, Actor: "a", Now: late.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID <= first.ID || second.At.Before(first.At) {
		t.Fatalf("not monotone: %s %v then %s %v", first.ID, first.At, second.ID, second.At)
	}
	other, err := Append(dir, Input{Kind: KindNote, Actor: "b", Now: late.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if other.At.Equal(first.At) {
		t.Error("one actor's clock floor reached another actor")
	}
	if report, err := Check(dir); err != nil || len(report.Problems) != 0 {
		t.Fatalf("check: %+v, %v", report, err)
	}
}

func TestConcurrentAppends(t *testing.T) {
	dir := newRepo(t)
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			_, errs[i] = Append(dir, Input{Kind: KindNote, Actor: "same"})
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := Check(dir)
	if err != nil || report.Events != n || len(report.Problems) != 0 {
		t.Fatalf("check: %+v, %v", report, err)
	}
}

func TestAppendRefuses(t *testing.T) {
	dir := newRepo(t)
	cases := map[string]Input{
		"no actor":     {Kind: KindNote},
		"blank actor":  {Kind: KindNote, Actor: "  "},
		"bad kind":     {Kind: "Not A Kind", Actor: "a"},
		"bad parent":   {Kind: KindNote, Actor: "a", Parents: []string{"nope"}},
		"absent":       {Kind: KindNote, Actor: "a", Parents: []string{idA}},
		"twice":        {Kind: KindNote, Actor: "a", Parents: []string{idA, idA}},
		"bad extra":    {Kind: KindNote, Actor: "a", Extra: map[string]string{"Bad Key": "x"}},
		"known extra":  {Kind: KindNote, Actor: "a", Extra: map[string]string{"actor": "x"}},
		"multi extra":  {Kind: KindNote, Actor: "a", Extra: map[string]string{"k": "x\ny"}},
		"empty commit": {Kind: KindNote, Actor: "a", Commits: map[string]string{"": "x"}},
		"empty repo":   {Kind: KindNote, Actor: "a", Repos: []string{""}},
		"comment":      {Kind: KindNote, Actor: "a", Extra: map[string]string{"k": "x #y"}},
	}
	for name, in := range cases {
		if _, err := Append(dir, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Append(t.TempDir(), Input{Kind: KindNote, Actor: "a"}); !errors.Is(err, ErrNotContextRepo) {
		t.Errorf("no layout: %v", err)
	}
	put(t, dir, idA+".md", "not an event")
	if _, err := Append(dir, Input{Kind: KindNote, Actor: "a"}); !errors.Is(err, ErrMalformed) {
		t.Errorf("beside a malformed file: %v", err)
	}
}

func TestAppendReportsAFolderItCannotWrite(t *testing.T) {
	dir := newRepo(t)
	events := filepath.Join(dir, EventsDir)
	if err := os.Chmod(events, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(events, 0o755) })
	if _, err := Append(dir, Input{Kind: KindNote, Actor: "a"}); err == nil {
		t.Fatal("the write failed but Append reported success")
	}
}

func TestAppendQuotesAwkwardValues(t *testing.T) {
	dir := newRepo(t)
	in := Input{Kind: KindNote, Actor: "Ada, the #1: [x]", Task: "t", Repos: []string{"a b", `q"r`},
		Commits: map[string]string{"x,y": "1 2"}, Extra: map[string]string{"ticket": "42"}, Body: "no newline"}
	got, err := Append(dir, in)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Log(dir, Query{})
	if err != nil {
		t.Fatal(err)
	}
	e := c.Events[0]
	if e.ID != got.ID || e.Actor != in.Actor || !slices.Equal(e.Repos, in.Repos) || e.Commits["x,y"] != "1 2" ||
		e.Extra["ticket"] != "42" || e.Body != "no newline\n" {
		t.Fatalf("did not read back: %+v", e)
	}
}

func TestCheckRefuses(t *testing.T) {
	const at = "2026-01-01T00:00:00Z"
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"cycle", map[string]string{
			idA + ".md": event(idA, "a", at, idB),
			idB + ".md": event(idB, "b", at, idA),
			idC + ".md": event(idC, "c", at, idA+", "+idD),
			idD + ".md": event(idD, "d", at, ""),
		}, []string{CodeCycle + " " + idA + ".md", CodeCycle + " " + idB + ".md"}},
		{"self parent", map[string]string{idA + ".md": event(idA, "a", at, idA)}, []string{CodeCycle + " " + idA + ".md"}},
		{"missing parent", map[string]string{idA + ".md": event(idA, "a", at, idB)}, []string{CodeMissingParent + " " + idA + ".md"}},
		{"name differs", map[string]string{idB + ".md": event(idA, "a", at, "")}, []string{CodeMalformed + " " + idB + ".md"}},
		{"lower case name", map[string]string{strings.ToLower(idA) + ".md": event(idA, "a", at, "")}, []string{CodeMalformed + " " + strings.ToLower(idA) + ".md"}},
		{"not markdown", map[string]string{"notes.txt": "x"}, []string{CodeMalformed + " notes.txt"}},
		{"merge markers", map[string]string{idA + ".md": "<<<<<<< ours\n" + event(idA, "a", at, "") + "=======\n>>>>>>> theirs\n"}, []string{CodeMalformed + " " + idA + ".md"}},
		{"clock runs back", map[string]string{
			idA + ".md": event(idA, "a", "2026-01-02T00:00:00Z", ""),
			idB + ".md": event(idB, "a", at, ""),
		}, []string{CodeNotMonotone + " " + idB + ".md"}},
		{"parent is newer, same actor", map[string]string{
			idA + ".md": event(idA, "a", at, idB),
			idB + ".md": event(idB, "a", at, ""),
		}, []string{CodeNotMonotone + " " + idA + ".md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			for name, text := range tc.files {
				put(t, dir, name, text)
			}
			report, err := Check(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := codes(report); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckRefusesWhatIsNotAFile(t *testing.T) {
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, EventsDir, idA+".md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, ReadmeFile), filepath.Join(dir, EventsDir, idB+".md")); err != nil {
		t.Fatal(err)
	}
	report, err := Check(dir)
	if err != nil || len(report.Problems) != 2 {
		t.Fatalf("%+v, %v", report, err)
	}
}

func TestCheckIgnoresDotFiles(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, ".tmp-1", "half written")
	report, err := Check(dir)
	if err != nil || report.Events != 0 || len(report.Problems) != 0 {
		t.Fatalf("%+v, %v", report, err)
	}
}

func TestUnreadableFolders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, EventsDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(dir); err == nil || errors.Is(err, ErrNotContextRepo) {
		t.Errorf("check: %v", err)
	}
	if _, err := Log(dir, Query{}); err == nil {
		t.Error("log of an unreadable folder succeeded")
	}
	if _, err := Append(dir, Input{Kind: KindNote, Actor: "a"}); err == nil {
		t.Error("append to an unreadable folder succeeded")
	}
}

func TestCheckReportsAFileItCannotRead(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, idA+".md", event(idA, "a", "2026-01-01T00:00:00Z", ""))
	if err := os.Chmod(filepath.Join(dir, EventsDir, idA+".md"), 0); err != nil {
		t.Fatal(err)
	}
	report, err := Check(dir)
	if err != nil || len(report.Problems) != 1 || report.Problems[0].Code != CodeMalformed {
		t.Fatalf("%+v, %v", report, err)
	}
}

func TestInitRefusesAFolderAtThePlaceholder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, KnowledgeDir, keepFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir); err == nil {
		t.Fatal("a folder at the placeholder was accepted")
	}
}

func TestCheckNeedsTheLayout(t *testing.T) {
	if _, err := Check(t.TempDir()); !errors.Is(err, ErrNotContextRepo) {
		t.Fatal(err)
	}
}

func TestParseRefuses(t *testing.T) {
	ok := "id: " + idA + "\nkind: note\nat: 2026-01-01T00:00:00Z\nactor: a\nparents: []\n"
	wrap := func(header string) string { return "---\n" + header + "---\n" }
	cases := map[string]string{
		"no opening":       ok,
		"no closing":       "---\n" + ok,
		"no colon":         wrap(ok + "stray\n"),
		"bad key":          wrap(ok + "Bad Key: 1\n"),
		"repeated key":     wrap(ok + "actor: b\n"),
		"no id":            wrap(strings.Replace(ok, "id: "+idA+"\n", "", 1)),
		"no kind":          wrap(strings.Replace(ok, "kind: note\n", "", 1)),
		"no at":            wrap(strings.Replace(ok, "at: 2026-01-01T00:00:00Z\n", "", 1)),
		"no actor":         wrap(strings.Replace(ok, "actor: a\n", "", 1)),
		"no parents":       wrap(strings.Replace(ok, "parents: []\n", "", 1)),
		"bad id":           wrap(strings.Replace(ok, idA, "01KB", 1)),
		"bad kind":         wrap(strings.Replace(ok, "kind: note", "kind: NOTE", 1)),
		"bad time":         wrap(strings.Replace(ok, "2026-01-01T00:00:00Z", "yesterday", 1)),
		"empty actor":      wrap(strings.Replace(ok, "actor: a", `actor: ""`, 1)),
		"bad quote":        wrap(strings.Replace(ok, "actor: a", `actor: "a`, 1)),
		"parents scalar":   wrap(strings.Replace(ok, "parents: []", "parents: x", 1)),
		"parents unclosed": wrap(strings.Replace(ok, "parents: []", "parents: [x", 1)),
		"parent bad":       wrap(strings.Replace(ok, "parents: []", "parents: [x]", 1)),
		"parent twice":     wrap(strings.Replace(ok, "parents: []", "parents: ["+idB+", "+idB+"]", 1)),
		"repos scalar":     wrap(ok + "repos: x\n"),
		"repos empty item": wrap(ok + "repos: [a,,b]\n"),
		"repos bad quote":  wrap(ok + "repos: [\"a]\n"),
		"commits scalar":   wrap(ok + "commits: x\n"),
		"commits no colon": wrap(ok + "commits: { a }\n"),
		"commits empty":    wrap(ok + "commits: { a: }\n"),
		"commits repeated": wrap(ok + "commits: { a: 1, a: 2 }\n"),
		"commits key":      wrap(ok + "commits: { \"a: 1 }\n"),
		"commits bad key":  wrap(ok + "commits: { \"a\\q\": 1 }\n"),
		"commits unclosed": wrap(ok + "commits: { a: \"1 }\n"),
		"commits bad item": wrap(ok + "commits: { a: 1,, b: 2 }\n"),
		"task bad":         wrap(ok + "task: \"x\n"),
	}
	for name, text := range cases {
		if _, err := parseEvent([]byte(text)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseKeepsWhatItDoesNotKnow(t *testing.T) {
	text := "---\n# a comment\nid: " + idA + "\nkind: some.future.kind\nat: 2026-01-01T06:00:00+06:00 # local\nactor: a\nparents: []\nreviewer: \"kim #1\"\n\n---\n"
	e, err := parseEvent([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "some.future.kind" || e.Extra["reviewer"] != `"kim #1"` || e.At.Location() != time.UTC || e.Body != "" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(string(formatEvent(e)), "reviewer: \"kim #1\"\n") {
		t.Fatalf("an unknown key did not survive a write: %s", formatEvent(e))
	}
}

func TestFixtureBytesRoundTrip(t *testing.T) {
	files, err := filepath.Glob("testdata/forked/events/*.md")
	if err != nil || len(files) != 4 {
		t.Fatalf("%v, %v", files, err)
	}
	for _, file := range files {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		e, err := parseEvent(want)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatEvent(e); !bytes.Equal(got, want) {
			t.Errorf("%s:\n%s\nwant:\n%s", file, got, want)
		}
	}
}

func TestLogGolden(t *testing.T) {
	c, err := Log("testdata/forked", Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file   string
		render func(*bytes.Buffer) error
	}{
		{"testdata/forked.log.txt", func(b *bytes.Buffer) error { return c.WriteText(b) }},
		{"testdata/forked.log.json", func(b *bytes.Buffer) error { return c.WriteJSON(b) }},
	} {
		want, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := tc.render(&got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s:\n%s", tc.file, got.String())
		}
	}
}

func TestLogFilters(t *testing.T) {
	c, err := Log("testdata/forked", Query{Task: "ledger-sync"})
	if err != nil || len(c.Events) != 2 {
		t.Fatalf("task: %d, %v", len(c.Events), err)
	}
	c, err = Log("testdata/forked", Query{Since: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(c.Events) != 1 || c.Events[0].Kind != KindMergeLanded {
		t.Fatalf("since: %+v, %v", c.Events, err)
	}
}

func TestLogRefusesABrokenGraph(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, idA+".md", event(idA, "a", "2026-01-01T00:00:00Z", idB))
	var invalid *InvalidError
	if _, err := Log(dir, Query{}); !errors.As(err, &invalid) || len(invalid.Report.Problems) != 1 || invalid.Error() == "" {
		t.Fatalf("%v", err)
	}
	if _, err := Log(t.TempDir(), Query{}); !errors.Is(err, ErrNotContextRepo) {
		t.Fatal(err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWritersReportFailure(t *testing.T) {
	c, err := Log("testdata/forked", Query{})
	if err != nil {
		t.Fatal(err)
	}
	if c.WriteText(failWriter{}) == nil || c.WriteJSON(failWriter{}) == nil {
		t.Fatal("a failed write went unreported")
	}
}

func TestULID(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	id := newULID(at)
	if !validULID(id) || !ulidTime(id).Equal(at) {
		t.Fatalf("%s decodes to %v", id, ulidTime(id))
	}
	for _, bad := range []string{"", idA[:25], strings.ToLower(idA), "8" + idA[1:], "I" + idA[1:], idA[:25] + "U"} {
		if validULID(bad) {
			t.Errorf("%q passed", bad)
		}
	}
	if got := nextULID("", at); !validULID(got) {
		t.Errorf("first id %q", got)
	}
	later := at.Add(time.Second)
	if got := nextULID(id, later); !ulidTime(got).Equal(later) {
		t.Errorf("a later clock was not used: %s", got)
	}
	got := nextULID(id, at.Add(-time.Hour))
	if got <= id || !ulidTime(got).Equal(at) {
		t.Errorf("an earlier clock: %s after %s", got, id)
	}
	if got := successor("0000000000" + strings.Repeat("Z", 16)); got != "0000000001"+strings.Repeat("0", 16) {
		t.Errorf("the random part did not carry into the time: %s", got)
	}
}
