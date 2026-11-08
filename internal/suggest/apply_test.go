package suggest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/hooks"
)

func commandEntry(command string) Entry {
	return Entry{
		ID: "sample", Kind: KindTip, Title: "Sample", Body: "text",
		Install: &Install{Command: command, Touches: []string{"a.txt"}},
	}
}

type applyFixture struct {
	applier *Applier
	runs    *[]Recipe
	store   *hooks.Approvals
}

func newApplyFixture(t *testing.T) applyFixture {
	t.Helper()
	store := &hooks.Approvals{Path: filepath.Join(t.TempDir(), "approvals.json")}
	runs := &[]Recipe{}
	return applyFixture{
		store: store,
		runs:  runs,
		applier: &Applier{Store: store, Exec: func(_ context.Context, r Recipe, _ string, _ Terminal) error {
			*runs = append(*runs, r)
			return nil
		}},
	}
}

func terminal(input string) (Terminal, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return Terminal{In: strings.NewReader(input), Out: out, Err: out, IsTerminal: true}, out
}

func TestRecipeOfDescribesEachForm(t *testing.T) {
	cases := []struct {
		install Install
		kind    RecipeKind
		text    string
	}{
		{Install{Command: "make it", Touches: []string{"x"}}, RecipeCommand, "make it"},
		{Install{MCP: "{}", Touches: []string{"x"}}, RecipeMCP, "{}"},
		{Install{SkillRepo: "https://example.com/a/b", Touches: []string{"x"}}, RecipeSkillRepo, "https://example.com/a/b"},
	}
	for _, c := range cases {
		e := Entry{ID: "one", Install: &c.install}
		r, ok := RecipeOf(e)
		if !ok || r.Kind != c.kind || r.Text != c.text || r.ID != "one" || !strings.HasPrefix(r.Checksum, "sha256:") {
			t.Errorf("RecipeOf(%+v) = %+v, %v", c.install, r, ok)
		}
	}
	if _, ok := RecipeOf(Entry{ID: "none"}); ok {
		t.Error("an entry with no install block has no recipe")
	}
}

func TestChecksumFollowsTheRecipeOnly(t *testing.T) {
	base := commandEntry("echo one")
	sum := func(e Entry) string {
		r, _ := RecipeOf(e)
		return r.Checksum
	}
	reworded := base
	reworded.Title, reworded.Body = "Other", "other"
	if sum(base) != sum(reworded) {
		t.Error("rewording an entry must keep its approval")
	}
	for name, e := range map[string]Entry{
		"command": commandEntry("echo two"),
		"touches": {ID: "sample", Install: &Install{Command: "echo one", Touches: []string{"b.txt"}}},
		"id":      {ID: "other", Install: &Install{Command: "echo one", Touches: []string{"a.txt"}}},
		"field":   {ID: "sample", Install: &Install{MCP: "echo one", Touches: []string{"a.txt"}}},
	} {
		if sum(base) == sum(e) {
			t.Errorf("a changed %s must change the checksum", name)
		}
	}
	joined := Entry{ID: "sample", Install: &Install{Command: "a", Touches: []string{"b", "c"}}}
	split := Entry{ID: "sample", Install: &Install{Command: "a", Touches: []string{"b\x00c"}}}
	if sum(joined) == sum(split) {
		t.Error("field boundaries must be part of the checksum")
	}
}

func TestRunRefusesWithoutATerminal(t *testing.T) {
	f := newApplyFixture(t)
	e := commandEntry("echo one")
	r, _ := RecipeOf(e)
	if err := f.store.Approve(ApprovalScope, []string{r.Checksum}); err != nil {
		t.Fatal(err)
	}
	err := f.applier.Run(context.Background(), e, t.TempDir(), Terminal{In: strings.NewReader("yes\n"), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("err = %v, want ErrNoTerminal", err)
	}
	if len(*f.runs) != 0 {
		t.Fatal("a recipe ran without a terminal, even though it was approved")
	}
}

func TestRunAsksOnceThenRemembersTheChecksum(t *testing.T) {
	f := newApplyFixture(t)
	e := commandEntry("echo one")
	term, out := terminal("yes\n")
	if err := f.applier.Run(context.Background(), e, t.TempDir(), term); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"echo one", "a.txt", "sha256:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the prompt must show %q: %s", want, out)
		}
	}
	again, _ := terminal("")
	if err := f.applier.Run(context.Background(), e, t.TempDir(), again); err != nil {
		t.Fatal(err)
	}
	if len(*f.runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(*f.runs))
	}
}

func TestRunStopsWhenTheAnswerIsNotYes(t *testing.T) {
	for _, input := range []string{"no\n", "\n", "", "y\n", "yesyes\n"} {
		f := newApplyFixture(t)
		e := commandEntry("echo one")
		term, _ := terminal(input)
		if err := f.applier.Run(context.Background(), e, t.TempDir(), term); !errors.Is(err, ErrDeclined) {
			t.Fatalf("input %q: err = %v, want ErrDeclined", input, err)
		}
		r, _ := RecipeOf(e)
		ok, err := f.store.Approved(ApprovalScope, r.Checksum)
		if err != nil || ok || len(*f.runs) != 0 {
			t.Fatalf("input %q: approved=%v err=%v runs=%d", input, ok, err, len(*f.runs))
		}
	}
}

func TestChangedRecipeNeedsANewApproval(t *testing.T) {
	f := newApplyFixture(t)
	term, _ := terminal("yes\n")
	if err := f.applier.Run(context.Background(), commandEntry("echo one"), t.TempDir(), term); err != nil {
		t.Fatal(err)
	}
	second, _ := terminal("")
	err := f.applier.Run(context.Background(), commandEntry("echo two"), t.TempDir(), second)
	if !errors.Is(err, ErrDeclined) || len(*f.runs) != 1 {
		t.Fatalf("err = %v, runs = %d", err, len(*f.runs))
	}
}

func TestRunWithoutARunnableRecipe(t *testing.T) {
	f := newApplyFixture(t)
	term, _ := terminal("yes\n")
	if err := f.applier.Run(context.Background(), Entry{ID: "bare"}, t.TempDir(), term); !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("no recipe: %v", err)
	}
	snippet := Entry{ID: "snip", Install: &Install{MCP: "{}", Touches: []string{"x"}}}
	if err := f.applier.Run(context.Background(), snippet, t.TempDir(), term); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("snippet: %v", err)
	}
	if len(*f.runs) != 0 {
		t.Fatal("nothing should have run")
	}
}

func TestRunReportsAStoreFailure(t *testing.T) {
	folder := t.TempDir()
	bad := filepath.Join(folder, "approvals.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &Applier{Store: &hooks.Approvals{Path: bad}, Exec: func(context.Context, Recipe, string, Terminal) error {
		t.Fatal("must not run")
		return nil
	}}
	term, _ := terminal("yes\n")
	if err := a.Run(context.Background(), commandEntry("echo one"), t.TempDir(), term); err == nil {
		t.Fatal("a broken approvals file must stop the run")
	}

	blocked := filepath.Join(folder, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.Store = &hooks.Approvals{Path: filepath.Join(blocked, "approvals.json")}
	term, _ = terminal("yes\n")
	if err := a.Run(context.Background(), commandEntry("echo one"), t.TempDir(), term); err == nil {
		t.Fatal("an approval that cannot be saved must stop the run")
	}
}

type failingStore struct{ err error }

func (f failingStore) Approved(string, string) (bool, error) { return false, nil }
func (f failingStore) Approve(string, []string) error        { return f.err }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestRunStopsWhenTheAnswerOrApprovalFails(t *testing.T) {
	refused := func(store ApprovalStore, in io.Reader) error {
		a := &Applier{Store: store, Exec: func(context.Context, Recipe, string, Terminal) error {
			t.Fatal("must not run")
			return nil
		}}
		return a.Run(context.Background(), commandEntry("echo one"), t.TempDir(), Terminal{In: in, Out: io.Discard, Err: io.Discard, IsTerminal: true})
	}
	want := errors.New("cannot save")
	if err := refused(failingStore{want}, strings.NewReader("yes\n")); !errors.Is(err, want) {
		t.Fatalf("save failure: %v", err)
	}
	if err := refused(failingStore{}, failingReader{}); !errors.Is(err, ErrDeclined) {
		t.Fatalf("read failure: %v", err)
	}
}

func TestExecRecipeRunsACommandInTheFolder(t *testing.T) {
	dir := t.TempDir()
	r, _ := RecipeOf(commandEntry("echo done > made.txt"))
	term, _ := terminal("")
	if err := ExecRecipe(context.Background(), r, dir, term); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "made.txt")); err != nil || strings.TrimSpace(string(data)) != "done" {
		t.Fatalf("made.txt: %q, %v", data, err)
	}
	failing, _ := RecipeOf(commandEntry("exit 3"))
	if err := ExecRecipe(context.Background(), failing, dir, term); err == nil {
		t.Fatal("a failing recipe must return its error")
	}
}

func TestExecRecipeClonesASkillToItsTarget(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	record := filepath.Join(bin, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + record + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	e := Entry{ID: "s", Install: &Install{SkillRepo: "https://example.com/a/b", Touches: []string{"~/.claude/skills/b"}}}
	r, _ := RecipeOf(e)
	term, _ := terminal("")
	if err := ExecRecipe(context.Background(), r, t.TempDir(), term); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "clone\n--\nhttps://example.com/a/b\n" + filepath.Join(home, ".claude/skills/b") + "\n"
	if string(data) != want {
		t.Fatalf("git args:\n%s\nwant:\n%s", data, want)
	}
}

func TestExecRecipeRefusesWhatItCannotDo(t *testing.T) {
	term, _ := terminal("")
	snippet := Recipe{Kind: RecipeMCP, Text: "{}", Touches: []string{"x"}}
	if err := ExecRecipe(context.Background(), snippet, t.TempDir(), term); !errors.Is(err, ErrNotRunnable) {
		t.Fatalf("snippet: %v", err)
	}
	noTarget := Recipe{Kind: RecipeSkillRepo, Text: "https://example.com/a/b"}
	if err := ExecRecipe(context.Background(), noTarget, t.TempDir(), term); err == nil {
		t.Fatal("a skill with no target must be refused")
	}
	t.Setenv("HOME", "")
	homeless := Recipe{Kind: RecipeSkillRepo, Text: "https://example.com/a/b", Touches: []string{"~/x"}}
	if err := ExecRecipe(context.Background(), homeless, t.TempDir(), term); err == nil {
		t.Fatal("a home path with no home must be refused")
	}
}

func TestExpandHomeLeavesAPlainPathAlone(t *testing.T) {
	if got, err := expandHome("/a/b"); err != nil || got != "/a/b" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestExpandHomeReportsAMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := expandHome("~/x"); err == nil {
		t.Fatal("a path under a missing home must fail")
	}
}
