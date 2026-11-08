package commitmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden file")

type parsed struct {
	Input   string   `json:"input"`
	Message *Message `json:"message,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func TestParseGolden(t *testing.T) {
	inputs := []string{
		"feat: add login",
		"fix(api): close the socket\n",
		"refactor(store/sql)!: drop the old table",
		"docs(a.b_c-d): explain the flag\n\nThe flag surprised people.",
		"chore: bump\n\nFirst paragraph.\n\nSecond paragraph.\n\nRefs #12\nReviewed-by: someone",
		"ci: only footers\n\nBREAKING CHANGE: the job is gone",
		"build: other spelling\n\nBREAKING-CHANGE: the target moved",
		"perf: keep prose\n\nthis is not: a footer list\nbecause of this line",
		"test: empty token line\n\n: value",
		"test: bare token\n\nToken:",
		"feat: é accented start",
		"Feat: capital type",
		"feat add login",
		"feat:no space",
		"wip: not a type",
		"feat(): empty scope",
		"feat(API): upper scope",
		"feat(api: not closed",
		"feat: ",
		"feat:  leading space",
		"feat: Capital subject",
		"feat: ends with a period.",
		"feat: tab\tinside",
		"feat: carriage\r\n",
		"feat: no blank line\nbody",
		"feat: " + strings.Repeat("x", MaxHeader),
	}
	results := make([]parsed, 0, len(inputs))
	for _, input := range inputs {
		result := parsed{Input: input}
		message, err := Parse(input)
		if err != nil {
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("%q: %T", input, err)
			}
			result.Error = err.Error()
		} else {
			result.Message = &message
		}
		results = append(results, result)
	}
	got, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	file := filepath.Join("testdata", "parse.golden.json")
	if *update {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got\n%s", got)
	}
}

func TestMessageRoundTrips(t *testing.T) {
	for _, text := range []string{
		"feat(x)!: a\n\nwhy\n\nRefs #1\nAcked-by: b",
		"fix: a\n\nBREAKING CHANGE: c",
		"docs: a",
	} {
		message, err := Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		if message.String() != text {
			t.Fatalf("%q != %q", message.String(), text)
		}
	}
}

func TestBranchParts(t *testing.T) {
	for _, c := range []struct{ branch, kind, scope string }{
		{"feat/login", "feat", "login"},
		{"fix/a/B c", "fix", "a-b-c"},
		{"main", "chore", ""},
		{"", "chore", ""},
		{"wild/one", "chore", ""},
		{"feat/" + strings.Repeat("x", MaxScope+1), "feat", ""},
		{"feat/---", "feat", ""},
	} {
		kind, scope := BranchParts(c.branch)
		if kind != c.kind || scope != c.scope {
			t.Errorf("%q: %q %q", c.branch, kind, scope)
		}
	}
}

func TestFallbackIsAlwaysAValidMessage(t *testing.T) {
	long := strings.Repeat("n", 90) + ".go"
	for _, c := range []struct {
		name, branch string
		files        []string
		pin          Pin
		want         string
	}{
		{"one file", "feat/one", []string{"dir/a.txt"}, Pin{}, "feat(one): update a.txt"},
		{"no type", "main", []string{"a.txt"}, Pin{}, "chore: update a.txt"},
		{"two", "fix/x", []string{"a", "b"}, Pin{}, "fix(x): update a and b"},
		{"many", "main", []string{"a", "b", "c", "d"}, Pin{}, "chore: update a, b and 2 more files"},
		{"pin", "main", []string{"a"}, Pin{Type: "docs", Scope: "s"}, "docs(s): update a"},
		{"long name", "main", []string{long}, Pin{}, "chore: update 1 files"},
		{"newline in name", "main", []string{"a\nb"}, Pin{}, "chore: update 1 files"},
		{"period name", "main", []string{"a."}, Pin{}, "chore: update 1 files"},
		{"nothing", "main", nil, Pin{}, "chore: update 0 files"},
	} {
		message := Fallback(c.branch, c.files, c.pin)
		if message.Header() != c.want {
			t.Errorf("%s: %q", c.name, message.Header())
		}
		if _, err := Parse(message.String()); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestPinValidate(t *testing.T) {
	for _, pin := range []Pin{{Type: "nope"}, {Scope: "UP"}, {Scope: strings.Repeat("x", MaxScope+1)}} {
		if pin.Validate() == nil {
			t.Errorf("%+v passed", pin)
		}
	}
	if err := (Pin{Type: "feat", Scope: "x"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPromptHoldsEachPartAndCapsTheDiff(t *testing.T) {
	big := strings.Repeat("line\n", DiffBudget)
	prompt := Prompt(Input{
		Branch: "feat/x", Target: "main", Diff: big, Stat: strings.Repeat("s\n", statBudget),
		Subjects: []string{"fix: older"}, Append: "Mention the ticket.", Pin: Pin{Type: "fix", Scope: "api"},
	})
	for _, want := range []string{"feat/x", "squashed onto: main", "fix: older", "Mention the ticket.", "Use the type fix", "Use the scope api", "[diff cut here]", "[diffstat cut here]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("no %q", want)
		}
	}
	if len(prompt) > DiffBudget+statBudget+4096 {
		t.Fatalf("prompt is %d bytes", len(prompt))
	}
	if strings.Contains(Prompt(Input{Diff: "short\n"}), "cut here") {
		t.Fatal("a short diff was cut")
	}
	if got := capText(strings.Repeat("x", 20), 10, "!"); got != strings.Repeat("x", 10)+"!\n" {
		t.Fatalf("%q", got)
	}
}

func TestIsLockFile(t *testing.T) {
	if !IsLockFile("web/bun.lock") || !IsLockFile("go.sum") || IsLockFile("lock.go") {
		t.Fatal("wrong classification")
	}
}

func TestResolveFindsHarnessesInOrder(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := (&Generator{}).Resolve(); !errors.Is(err, ErrNoGenerator) {
		t.Fatal(err)
	}
	if _, err := (&Generator{}).Generate(context.Background(), "p", Pin{}); !errors.Is(err, ErrNoGenerator) {
		t.Fatal(err)
	}
	for _, c := range []struct{ program, want string }{{"opencode", "opencode run"}, {"codex", "codex exec"}, {"claude", "claude -p"}} {
		if err := os.WriteFile(filepath.Join(bin, c.program), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := (&Generator{Command: "auto"}).Resolve()
		if err != nil || got != c.want {
			t.Fatalf("%q %v", got, err)
		}
	}
	if got, _ := (&Generator{Command: "my tool"}).Resolve(); got != "my tool" {
		t.Fatal(got)
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "gen")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestGenerateReadsThePromptAndCleansTheAnswer(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	command := script(t, "cat > "+seen+"\nprintf '```\\nfeat: add x\\n\\nbecause\\n```\\n'\n")
	message, err := (&Generator{Command: command}).Generate(context.Background(), "the prompt", Pin{Type: "fix", Scope: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if message.String() != "fix(s): add x\n\nbecause" {
		t.Fatalf("%q", message.String())
	}
	if data, _ := os.ReadFile(seen); string(data) != "the prompt" {
		t.Fatalf("%q", data)
	}
}

func TestGenerateRetriesOnceThenRefusesAnUnparsableAnswer(t *testing.T) {
	dir := t.TempDir()
	command := script(t, "cat >> "+dir+"/prompts\necho x >> "+dir+"/count\necho 'not a message'\n")
	_, err := (&Generator{Command: command}).Generate(context.Background(), "p", Pin{})
	var refused *UnparsableError
	if !errors.As(err, &refused) || !strings.Contains(refused.Error(), "after one retry") {
		t.Fatal(err)
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dir + "/count"); string(data) != "x\nx\n" {
		t.Fatalf("%q", data)
	}
	if data, _ := os.ReadFile(dir + "/prompts"); !strings.Contains(string(data), "previous answer was refused") {
		t.Fatalf("%q", data)
	}
}

func TestGenerateRecoversOnTheRetry(t *testing.T) {
	dir := t.TempDir()
	command := script(t, "cat > /dev/null\nif [ -f "+dir+"/once ]; then echo 'feat: second'; else touch "+dir+"/once; exit 3; fi\n")
	message, err := (&Generator{Command: command}).Generate(context.Background(), "p", Pin{})
	if err != nil || message.Subject != "second" {
		t.Fatalf("%+v %v", message, err)
	}
}

func TestGenerateReportsAFailureToRun(t *testing.T) {
	_, err := (&Generator{Command: "exit 5"}).Generate(context.Background(), "p", Pin{})
	var refused *UnparsableError
	if err == nil || errors.As(err, &refused) || !strings.Contains(err.Error(), "failed") {
		t.Fatal(err)
	}
}

func TestGenerateStopsARunawayCommandAndItsChildren(t *testing.T) {
	started := time.Now()
	g := &Generator{Command: "sleep 30 & sleep 30", Timeout: 100 * time.Millisecond}
	_, err := g.Generate(context.Background(), "p", Pin{})
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("took %s", elapsed)
	}
}

func TestGenerateStopsWhenTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&Generator{Command: "echo 'feat: x'"}).Generate(ctx, "p", Pin{}); err == nil {
		t.Fatal("no error")
	}
}

func TestGenerateRefusesTooMuchOutput(t *testing.T) {
	g := &Generator{Command: "echo 'feat: a long enough answer'", MaxOutput: 5}
	if _, err := g.Generate(context.Background(), "p", Pin{}); err == nil || !strings.Contains(err.Error(), "more than 5 bytes") {
		t.Fatal(err)
	}
}
