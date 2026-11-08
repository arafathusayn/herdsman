package gitrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type memTracer struct {
	mu      sync.Mutex
	entries []Entry
}

func (m *memTracer) Trace(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
	cmd.Env = cleanTestEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func cleanTestEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "GIT_DIR" || name == "GIT_WORK_TREE" || name == "GIT_INDEX_FILE"
	})
}

func TestRunUsesTheNamedRepositoryAndReturnsStdout(t *testing.T) {
	dir := initRepo(t)
	tr := &memTracer{}
	r := &Runner{Tracer: tr}
	res, err := r.Run(context.Background(), dir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != dir {
		t.Fatalf("toplevel = %q, want %q", got, dir)
	}
	if len(tr.entries) != 1 {
		t.Fatalf("traced %d entries, want 1", len(tr.entries))
	}
	e := tr.entries[0]
	if e.Dir != dir || e.Exit != 0 || !slices.Equal(e.Args, []string{"rev-parse", "--show-toplevel"}) || e.Time.IsZero() || e.DurationMS < 0 {
		t.Fatalf("entry = %+v", e)
	}
}

func TestRunRefusesARelativeDirectory(t *testing.T) {
	r := &Runner{}
	for _, dir := range []string{"", "relative/path"} {
		if _, err := r.Run(context.Background(), dir, "status"); err == nil {
			t.Fatalf("Run(%q) succeeded", dir)
		}
	}
}

func TestExitErrorWithoutStderrStaysShort(t *testing.T) {
	got := (&ExitError{Args: []string{"diff", "--quiet"}, Code: 1}).Error()
	if got != "git diff --quiet: exit 1" {
		t.Fatalf("%q", got)
	}
	got = (&ExitError{Args: []string{"log"}, Code: 128, Stderr: "fatal: bad"}).Error()
	if got != "git log: exit 128: fatal: bad" {
		t.Fatalf("%q", got)
	}
}

func TestATraceThatCannotBeOpenedIsCounted(t *testing.T) {
	logs := t.TempDir()
	if err := os.Mkdir(filepath.Join(logs, TraceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	tr := NewFileTrace(logs)
	tr.Trace(Entry{Args: []string{"status"}})
	if tr.Dropped() != 1 {
		t.Fatalf("dropped = %d", tr.Dropped())
	}
}

func TestRunRefusesNoArguments(t *testing.T) {
	r := &Runner{}
	if _, err := r.Run(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Run with no arguments succeeded")
	}
}

func TestEveryLocalVariableGitNamesIsScrubbed(t *testing.T) {
	cmd := exec.Command("git", "rev-parse", "--local-env-vars")
	cmd.Env = cleanTestEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	names := strings.Fields(string(out))
	if len(names) == 0 {
		t.Fatal("git named no local variables")
	}
	for _, name := range names {
		env = append(env, name+"=/planted")
	}
	env = append(env, "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=true", "KEEP_ME=1", "GIT_AUTHOR_NAME=kept")
	got := scrub(env)
	want := []string{"KEEP_ME=1", "GIT_AUTHOR_NAME=kept"}
	if !slices.Equal(got, want) {
		t.Fatalf("scrub = %v, want %v", got, want)
	}
}

func TestPlantedGitDirDoesNotRedirectTheCall(t *testing.T) {
	dir := initRepo(t)
	other := initRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	res, err := (&Runner{}).Run(context.Background(), dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(res.Stdout)), filepath.Join(dir, ".git"); got != want {
		t.Fatalf("git dir = %q, want %q", got, want)
	}
}

func TestNonZeroExitIsAnExitErrorAndIsTraced(t *testing.T) {
	dir := initRepo(t)
	tr := &memTracer{}
	res, err := (&Runner{Tracer: tr}).Run(context.Background(), dir, "rev-parse", "--verify", "-q", "refs/heads/absent")
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	if exit.Code != 1 || res.Exit != 1 || tr.entries[0].Exit != 1 {
		t.Fatalf("code %d, result %d, trace %d", exit.Code, res.Exit, tr.entries[0].Exit)
	}
	if !strings.Contains(exit.Error(), "rev-parse") {
		t.Fatalf("message %q does not name the command", exit.Error())
	}
}

func TestAStartFailureIsNotAnExitError(t *testing.T) {
	tr := &memTracer{}
	r := &Runner{Tracer: tr, Git: filepath.Join(t.TempDir(), "no-such-git")}
	_, err := r.Run(context.Background(), t.TempDir(), "status")
	var exit *ExitError
	if err == nil || errors.As(err, &exit) {
		t.Fatalf("err = %v", err)
	}
	if tr.entries[0].Exit != -1 {
		t.Fatalf("exit = %d, want -1", tr.entries[0].Exit)
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&Runner{}).Run(ctx, initRepo(t), "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFileTraceAppendsOneJSONLinePerCallInPrivateFiles(t *testing.T) {
	dir := initRepo(t)
	logs := filepath.Join(t.TempDir(), "state", "logs")
	tr := NewFileTrace(logs)
	r := &Runner{Tracer: tr}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Run(context.Background(), dir, "rev-parse", "HEAD"); err == nil {
				t.Error("rev-parse on an unborn branch succeeded")
			}
		}()
	}
	wg.Wait()
	path := filepath.Join(logs, "commands.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := 0
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var e Entry
		if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
			t.Fatalf("line %d: %v: %s", lines, err, scan.Text())
		}
		if e.Dir != dir || e.Args[0] != "rev-parse" {
			t.Fatalf("entry = %+v", e)
		}
		lines++
	}
	if lines != 8 {
		t.Fatalf("%d lines, want 8", lines)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	folder, err := os.Stat(logs)
	if err != nil {
		t.Fatal(err)
	}
	if folder.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode = %o", folder.Mode().Perm())
	}
}

func TestAnUnwritableTraceDoesNotFailTheCall(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tr := NewFileTrace(filepath.Join(blocked, "logs"))
	if _, err := (&Runner{Tracer: tr}).Run(context.Background(), initRepo(t), "rev-parse", "--git-dir"); err != nil {
		t.Fatal(err)
	}
	if tr.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", tr.Dropped())
	}
}

// directExec lists the calls that start a process in the Go source of root,
// tests and testdata left out, as "<file>:<line>".
func directExec(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		local := ""
		for _, imp := range file.Imports {
			if imp.Path.Value == `"os/exec"` {
				local = "exec"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			isExec := local != "" && id.Name == local && strings.HasPrefix(sel.Sel.Name, "Command")
			isStart := id.Name == "os" && sel.Sel.Name == "StartProcess"
			if isExec || isStart {
				pos := fset.Position(sel.Pos())
				rel, _ := filepath.Rel(root, pos.Filename)
				found = append(found, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// The packages that run git, each with the only files allowed to start a
// process. gitrefs keeps its own call until its fallback moves onto this
// helper.
var execRules = []struct {
	dir     string
	allowed []string
}{
	{dir: ".", allowed: []string{"run.go"}},
	{dir: "../wtree"},
	{dir: "../gitrefs", allowed: []string{"reftable_fallback.go"}},
}

func TestEveryGitCallGoesThroughTheHelper(t *testing.T) {
	for _, rule := range execRules {
		for _, file := range directExec(t, rule.dir) {
			if !slices.Contains(rule.allowed, file) {
				t.Errorf("%s/%s starts a process; use gitrun", rule.dir, file)
			}
		}
	}
}

func TestTheGuardCatchesAPlantedCall(t *testing.T) {
	for name, source := range map[string]string{
		"plain.go":   "package p\nimport \"os/exec\"\nvar _ = exec.Command(\"git\", \"status\")\n",
		"context.go": "package p\nimport \"os/exec\"\nimport \"context\"\nvar _ = exec.CommandContext(context.Background(), \"git\")\n",
		"renamed.go": "package p\nimport run \"os/exec\"\nvar _ = run.Command(\"git\")\n",
		"start.go":   "package p\nimport \"os\"\nvar _, _ = os.StartProcess(\"git\", nil, nil)\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := directExec(t, dir); !slices.Equal(got, []string{name}) {
			t.Errorf("%s: found %v", name, got)
		}
	}
}

func TestTheGuardIgnoresTestsAndTestdata(t *testing.T) {
	dir := t.TempDir()
	source := []byte("package p\nimport \"os/exec\"\nvar _ = exec.Command(\"git\")\n")
	if err := os.Mkdir(filepath.Join(dir, "testdata"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a_test.go", filepath.Join("testdata", "b.go")} {
		if err := os.WriteFile(filepath.Join(dir, name), source, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := directExec(t, dir); len(got) != 0 {
		t.Fatalf("found %v", got)
	}
}

func TestRunnerIndexUsesThatIndexFile(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(t.TempDir(), "index")
	other := &Runner{Index: index}
	if _, err := other.Run(context.Background(), dir, "add", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatal(err)
	}
	out, err := (&Runner{}).Run(context.Background(), dir, "ls-files", "--cached")
	if err != nil || len(out.Stdout) != 0 {
		t.Fatalf("the repository's own index changed: %q %v", out.Stdout, err)
	}
}
