package wtree

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon/store"
	"github.com/arafathusayn/herdsman/internal/gitrefs"
	"github.com/arafathusayn/herdsman/internal/gitrun"
)

// --- templates

func baseVars() Vars {
	return Vars{
		Values: map[string]string{"task": "fix-login", "branch": "feat/fix-login", "repo": "acme", "repo_path": "/src/acme", "type": "feat"},
		Custom: map[string]string{"region": "eu"},
		Port:   12340,
		Ports:  map[string]int{"web": 12341},
	}
}

func TestRenderVariablesAndFilters(t *testing.T) {
	cases := []struct{ text, want string }{
		{"{{ branch }}", "feat/fix-login"},
		{"{{ branch | sanitize }}", "feat-fix-login"},
		{"{{ task | sanitize_db }}", "fix_login_" + hashText("fix-login")[:3]},
		{"{{ repo_path | basename }}", "acme"},
		{"{{ repo_path | dirname }}", "/src"},
		{"{{ task | hash }}", hashText("fix-login")},
		{"{{ task | tojson }}", `"fix-login"`},
		{"{{ vars.region }}", "eu"},
		{"{{ port }}", "12340"},
		{`{{ port "web" }}`, "12341"},
		{"{{ compose }}", "acme-fix-login"},
		{"{{ task | hash_port }}", fmt.Sprint(HashPort("fix-login"))},
		{"{{ task | codename }}", Codename("fix-login")},
	}
	for _, c := range cases {
		got, err := Render("t", c.text, baseVars())
		if err != nil {
			t.Errorf("%s: %v", c.text, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestAnExplicitComposeWins(t *testing.T) {
	v := baseVars()
	v.Values["compose"] = "other"
	got, err := Render("t", "{{ compose }}", v)
	if err != nil || got != "other" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestUndefinedVariableSuggestsTheNearestName(t *testing.T) {
	_, err := Render("t", "{{ brnach }}", baseVars())
	var unknown *UnknownVariableError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
	if unknown.Name != "brnach" || unknown.Suggestion != "branch" {
		t.Fatalf("%+v", unknown)
	}
	if !strings.Contains(err.Error(), `did you mean "branch"`) {
		t.Fatalf("message: %v", err)
	}
}

func TestAnUnrelatedNameHasNoSuggestion(t *testing.T) {
	_, err := Render("t", "{{ zzzzzzzzzzzz }}", baseVars())
	var unknown *UnknownVariableError
	if !errors.As(err, &unknown) || unknown.Suggestion != "" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("message: %v", err)
	}
}

func TestAKnownVariableWithNoValueIsAnError(t *testing.T) {
	_, err := Render("t", "{{ pr_number }}", baseVars())
	var unknown *UnknownVariableError
	if !errors.As(err, &unknown) || unknown.Name != "pr_number" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "no value") {
		t.Fatalf("message: %v", err)
	}
}

func TestAMissingCustomVariableSuggestsAKey(t *testing.T) {
	_, err := Render("t", "{{ vars.regoin }}", baseVars())
	var unknown *UnknownVariableError
	if !errors.As(err, &unknown) || unknown.Name != "vars.regoin" || unknown.Suggestion != "vars.region" {
		t.Fatalf("err = %v", err)
	}
}

func TestPortErrors(t *testing.T) {
	v := baseVars()
	if _, err := Render("t", `{{ port "db" }}`, v); err == nil || !strings.Contains(err.Error(), `"db"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Render("t", `{{ port "web" "db" }}`, v); err == nil {
		t.Fatal("two names accepted")
	}
	v.Port = 0
	if _, err := Render("t", "{{ port }}", v); err == nil {
		t.Fatal("no block accepted")
	}
}

func TestRenderReportsASyntaxError(t *testing.T) {
	var unknown *UnknownVariableError
	if _, err := Render("t", "{{ branch ", baseVars()); err == nil || errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
}

func TestSanitizeDBShape(t *testing.T) {
	cases := map[string]string{
		"My-Task":                "my_task_" + hashText("My-Task")[:3],
		"9lives":                 "_9lives_" + hashText("9lives")[:3],
		"a b.c":                  "a_b_c_" + hashText("a b.c")[:3],
		"日本":                     "__" + "_" + hashText("日本")[:3],
		strings.Repeat("a", 100): strings.Repeat("a", 59) + "_" + hashText(strings.Repeat("a", 100))[:3],
	}
	for in, want := range cases {
		if got := SanitizeDB(in); got != want {
			t.Errorf("SanitizeDB(%q) = %q, want %q", in, got, want)
		}
		if got := SanitizeDB(in); len(got) > 63 {
			t.Errorf("SanitizeDB(%q) is %d long", in, len(got))
		}
	}
}

func TestSanitizeLeavesNoPathSeparator(t *testing.T) {
	for in, want := range map[string]string{`a/b\c`: "a-b-c", "plain": "plain", "../up": "..-up"} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHashPortStaysInAlignedBlocks(t *testing.T) {
	seen := map[int]bool{}
	for i := range 2000 {
		p := HashPort(fmt.Sprintf("task-%d", i))
		if p < 10000 || p > 59990 || p%10 != 0 {
			t.Fatalf("port %d out of range or unaligned", p)
		}
		seen[p] = true
	}
	if len(seen) < 1000 {
		t.Fatalf("only %d distinct blocks", len(seen))
	}
	first, second := HashPort("x"), HashPort("x")
	if first != second {
		t.Fatal("not deterministic")
	}
}

func TestCodenameIsStableAndTwoWords(t *testing.T) {
	a, b := Codename("fix-login"), Codename("fix-login")
	if a != b || strings.Count(a, "-") != 1 {
		t.Fatalf("%q %q", a, b)
	}
	if Codename("fix-login") == Codename("other-task-name") && Codename("a") == Codename("b") {
		t.Fatal("codenames do not vary")
	}
}

func TestToJSONRejectsNothingItCanEncode(t *testing.T) {
	got, err := toJSON(map[string]int{"a": 1})
	if err != nil || got != `{"a":1}` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := toJSON(make(chan int)); err == nil {
		t.Fatal("channel encoded")
	}
}

// --- resolve

func TestDefaultNamingResolvesASiblingFolderAndBranch(t *testing.T) {
	v := baseVars()
	p, err := DefaultNaming().ResolvePath(v)
	if err != nil || p != "/src/acme.worktrees/fix-login" {
		t.Fatalf("%q %v", p, err)
	}
	b, err := DefaultNaming().ResolveBranch(v)
	if err != nil || b != "feat/fix-login" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestResolvePathRefusals(t *testing.T) {
	v := baseVars()
	for _, tmpl := range []string{"relative/{{ task }}", "{{ repo_path }}", "{{ repo_path }}/nested/.."} {
		if _, err := (Naming{WorktreePath: tmpl}).ResolvePath(v); err == nil {
			t.Errorf("%q accepted", tmpl)
		}
	}
	if _, err := (Naming{WorktreePath: "{{ nope }}"}).ResolvePath(v); err == nil {
		t.Error("bad template accepted")
	}
}

func TestResolveBranchRefusesAnInvalidName(t *testing.T) {
	v := baseVars()
	for _, tmpl := range []string{"a..b", "a b", "-x", "a.lock", "a/", "/a", "a//b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "@", "a@{b", ".a", "a/.b", "a.", "x.lock/y", "a\x7fb"} {
		if _, err := (Naming{BranchName: tmpl}).ResolveBranch(v); err == nil {
			t.Errorf("%q accepted", tmpl)
		}
	}
	if _, err := (Naming{BranchName: "{{ nope }}"}).ResolveBranch(v); err == nil {
		t.Error("bad template accepted")
	}
}

func TestBranchValidationAgreesWithGit(t *testing.T) {
	names := []string{"main", "feat/x", "a..b", "a b", "-x", "a.lock", "a/", "/a", "a//b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "@", "a@{b", ".a", "a/.b", "a.", "x.lock/y", "a@b", "ünï/cödé", "HEAD"}
	for _, name := range names {
		err := exec.Command("git", "check-ref-format", "--branch", name).Run()
		// git expands these two as shorthand here; neither is usable as a name,
		// and @ is also a worktree selector.
		if name == "HEAD" || name == "@" {
			if ValidateBranch(name) == nil {
				t.Errorf("%q accepted", name)
			}
			continue
		}
		if ours := ValidateBranch(name); (ours == nil) != (err == nil) {
			t.Errorf("%q: ours %v, git %v", name, ours, err)
		}
	}
	if ValidateBranch("") == nil {
		t.Error("empty name accepted")
	}
}

func TestValidateTask(t *testing.T) {
	for _, ok := range []string{"a", "fix-login", "v2-api"} {
		if err := ValidateTask(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "a/b", "..", "-a", "a-", "a--b", "a b", "a_b", strings.Repeat("a", 61)} {
		if ValidateTask(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// --- shortcuts

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in   string
		kind SelectorKind
		num  int
		repo string
	}{
		{"^", SelectDefault, 0, ""},
		{"@", SelectCurrent, 0, ""},
		{"-", SelectPrevious, 0, ""},
		{"pr:12", SelectPullRequest, 12, ""},
		{"mr:7", SelectMergeRequest, 7, ""},
		{"https://github.com/acme/app/pull/34", SelectPullRequest, 34, "acme/app"},
		{"https://github.com/acme/app/pull/34/files", SelectPullRequest, 34, "acme/app"},
		{"https://gitlab.com/acme/group/app/-/merge_requests/9", SelectMergeRequest, 9, "acme/group/app"},
		{"fix-login", SelectName, 0, ""},
		{"feat/fix-login", SelectName, 0, ""},
		{"/src/acme", SelectName, 0, ""},
	}
	for _, c := range cases {
		got, err := ParseSelector(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got.Kind != c.kind || got.Number != c.num || got.Repo != c.repo {
			t.Errorf("%s: %+v", c.in, got)
		}
	}
}

func TestParseSelectorRefusals(t *testing.T) {
	for _, bad := range []string{"", "pr:", "pr:0", "pr:-1", "pr:x", "mr:1.5", "https://example.com/a/b", "https://github.com/acme/app/pull/abc", "https://github.com/acme/app/issues/1", "http://github.com/acme/app/pull/1x"} {
		if got, err := ParseSelector(bad); err == nil {
			t.Errorf("%q parsed: %+v", bad, got)
		}
	}
}

func selectorRows() []Row {
	return []Row{
		{Task: "main", Branch: "main", Path: "/src/acme"},
		{Task: "fix-login", Branch: "feat/fix-login", Path: "/src/acme.worktrees/fix-login"},
		{Task: "api", Branch: "fix-login", Path: "/src/acme.worktrees/api"},
	}
}

func TestResolveSelector(t *testing.T) {
	ctx := SelectContext{Rows: selectorRows(), Default: "main", Current: "/src/acme.worktrees/api/sub", Previous: "fix-login"}
	cases := map[string]string{
		"^":                             "/src/acme",
		"@":                             "/src/acme.worktrees/api",
		"-":                             "/src/acme.worktrees/fix-login",
		"fix-login":                     "/src/acme.worktrees/fix-login", // the task wins over api's branch
		"feat/fix-login":                "/src/acme.worktrees/fix-login",
		"/src/acme.worktrees/api":       "/src/acme.worktrees/api",
		"/src/acme.worktrees/api/sub/x": "/src/acme.worktrees/api",
	}
	for in, want := range cases {
		sel, err := ParseSelector(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ResolveSelector(sel, ctx)
		if err != nil || got.Path != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
}

func TestResolveSelectorFailures(t *testing.T) {
	rows := selectorRows()
	cases := []struct {
		in  string
		ctx SelectContext
	}{
		{"nothing", SelectContext{Rows: rows}},
		{"^", SelectContext{Rows: rows}},
		{"^", SelectContext{Rows: rows, Default: "develop"}},
		{"-", SelectContext{Rows: rows}},
		{"-", SelectContext{Rows: rows, Previous: "gone"}},
		{"@", SelectContext{Rows: rows, Current: "/elsewhere"}},
		{"pr:3", SelectContext{Rows: rows}},
		{"mr:3", SelectContext{Rows: rows}},
	}
	for _, c := range cases {
		sel, err := ParseSelector(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := ResolveSelector(sel, c.ctx); err == nil {
			t.Errorf("%s resolved: %+v", c.in, got)
		}
	}
	sel, _ := ParseSelector("pr:3")
	if _, err := ResolveSelector(sel, SelectContext{}); !errors.Is(err, ErrNeedsFetch) {
		t.Fatalf("pr: %v", err)
	}
}

func TestAPathPrefixMatchesWholeComponentsOnly(t *testing.T) {
	rows := []Row{{Task: "a", Path: "/src/ab"}}
	sel, _ := ParseSelector("@")
	if _, err := ResolveSelector(sel, SelectContext{Rows: rows, Current: "/src/abc"}); err == nil {
		t.Fatal("/src/abc matched /src/ab")
	}
}

func TestTheDeepestWorktreeWinsForANestedPath(t *testing.T) {
	rows := []Row{{Task: "outer", Path: "/src"}, {Task: "inner", Path: "/src/nested"}}
	sel, _ := ParseSelector("@")
	got, err := ResolveSelector(sel, SelectContext{Rows: rows, Current: "/src/nested/deep"})
	if err != nil || got.Task != "inner" {
		t.Fatalf("%+v %v", got, err)
	}
}

// --- ecosystem

func detectFiles(t *testing.T, names ...string) Plan {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDetectEcosystems(t *testing.T) {
	cases := []struct {
		files   []string
		share   []string
		install []string
	}{
		{[]string{"bun.lock"}, []string{"node_modules"}, []string{"bun install --frozen-lockfile"}},
		{[]string{"pnpm-lock.yaml"}, []string{"node_modules"}, []string{"pnpm install --frozen-lockfile"}},
		{[]string{"package-lock.json"}, []string{"node_modules"}, []string{"npm ci"}},
		{[]string{"yarn.lock"}, []string{"node_modules"}, []string{"yarn install --frozen-lockfile"}},
		{[]string{"yarn.lock", ".yarnrc.yml"}, []string{"node_modules"}, []string{"yarn install --immutable"}},
		{[]string{"Cargo.lock"}, []string{"target"}, nil},
		{[]string{"go.sum"}, nil, nil},
		{[]string{"uv.lock"}, nil, []string{"uv sync"}},
		{[]string{"poetry.lock"}, nil, []string{"poetry install"}},
		{[]string{"bun.lock", "package-lock.json"}, []string{"node_modules"}, []string{"bun install --frozen-lockfile"}},
		{[]string{"bun.lock", "Cargo.lock", "uv.lock"}, []string{"node_modules", "target"}, []string{"bun install --frozen-lockfile", "uv sync"}},
		{nil, nil, nil},
	}
	for _, c := range cases {
		plan := detectFiles(t, c.files...)
		if !slices.Equal(plan.Share, c.share) || !slices.Equal(plan.Install, c.install) {
			t.Errorf("%v: %+v", c.files, plan)
		}
	}
}

func TestDetectFlags(t *testing.T) {
	if !detectFiles(t, "go.sum").GoCachePerTask {
		t.Error("go.sum: no per-task cache")
	}
	if detectFiles(t, "bun.lock").GoCachePerTask {
		t.Error("bun: per-task go cache")
	}
	for _, name := range []string{"compose.yaml", "docker-compose.yml"} {
		if !detectFiles(t, name).Compose {
			t.Errorf("%s: no compose", name)
		}
	}
	if got := detectFiles(t, ".env.example", ".env.test", ".envelope", "x").EnvFiles; !slices.Equal(got, []string{".env.example", ".env.test"}) {
		t.Errorf("env files = %v", got)
	}
	if got := detectFiles(t, ".env.test").EnvFiles; got != nil {
		t.Errorf("no example, env files = %v", got)
	}
}

func TestDetectFailsOnAMissingFolder(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("no error")
	}
}

// --- state

func openState(t *testing.T) (*State, string) {
	t.Helper()
	common := t.TempDir()
	st, err := OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, common
}

func TestOpenStateCreatesPrivateFilesAtSchemaOne(t *testing.T) {
	st, common := openState(t)
	dir := filepath.Join(common, "herdsman")
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", info, err)
	}
	info, err = os.Lstat(filepath.Join(dir, "state.db"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	if v, err := st.SchemaVersion(context.Background()); err != nil || v != 1 {
		t.Fatalf("version %d, %v", v, err)
	}
}

func TestOpenStateHasEveryTable(t *testing.T) {
	st, _ := openState(t)
	for _, table := range []string{"worktrees", "tasks", "resources", "hook_runs", "caches", "archives", "meta"} {
		var n int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: %d %v", table, n, err)
		}
	}
}

func TestReopenKeepsRows(t *testing.T) {
	common := t.TempDir()
	st, err := OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.PutWorktree(ctx, Worktree{Task: "a", Branch: "b", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.Worktrees(ctx)
	if err != nil || len(rows) != 1 || rows[0].Task != "a" || rows[0].CreatedAt.IsZero() {
		t.Fatalf("%+v %v", rows, err)
	}
}

func setUserVersion(t *testing.T, common string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(common, "herdsman", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
}

func TestANewerSchemaIsRefusedAndLeftAlone(t *testing.T) {
	st, common := openState(t)
	st.Close()
	setUserVersion(t, common, 99)
	_, err := OpenState(common)
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("err = %v", err)
	}
	db, _ := sql.Open("sqlite3", filepath.Join(common, "herdsman", "state.db"))
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 99 {
		t.Fatalf("version %d, %v", v, err)
	}
}

func TestMigrationsRunForwardFromTheStoredVersion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	two := []string{"CREATE TABLE one(a)", "CREATE TABLE two(a)"}
	if err := migrate(ctx, db, two[:1]); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, two); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("version %d %v", v, err)
	}
	if err := migrate(ctx, db, two); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestAFailedMigrationLeavesTheVersion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	err = migrate(ctx, db, []string{"CREATE TABLE ok(a); THIS IS NOT SQL"})
	if err == nil {
		t.Fatal("no error")
	}
	var v, tables int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 0 {
		t.Fatalf("version %d %v", v, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='ok'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("tables %d %v", tables, err)
	}
}

func TestOpenStateRefusesWrongModesAndLinks(t *testing.T) {
	t.Run("folder mode", func(t *testing.T) {
		common := t.TempDir()
		if err := os.Mkdir(filepath.Join(common, "herdsman"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenState(common); err == nil || !strings.Contains(err.Error(), "700") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("file mode", func(t *testing.T) {
		common := t.TempDir()
		if err := os.Mkdir(filepath.Join(common, "herdsman"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(common, "herdsman", "state.db"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenState(common); err == nil || !strings.Contains(err.Error(), "600") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("file link", func(t *testing.T) {
		common := t.TempDir()
		if err := os.Mkdir(filepath.Join(common, "herdsman"), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(common, "real.db")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(common, "herdsman", "state.db")); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenState(common); err == nil {
			t.Fatal("link accepted")
		}
	})
	t.Run("folder link", func(t *testing.T) {
		common := t.TempDir()
		real := filepath.Join(common, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(common, "herdsman")); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenState(common); err == nil {
			t.Fatal("link accepted")
		}
	})
	t.Run("missing common dir", func(t *testing.T) {
		if _, err := OpenState(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("no error")
		}
	})
}

// The daemon's store replaces SQLite's file open for the whole process; the
// state has to keep working once it has.
func TestStateWorksBesideTheDaemonStore(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "acct")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	conn, err := store.Open(filepath.Join(folder, "account.db"), store.Writer, store.KindAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	common := t.TempDir()
	st, err := OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.PutWorktree(ctx, Worktree{Task: "a", Branch: "b", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenState(common)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if rows, err := st.Worktrees(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	if _, err := conn.Exec("CREATE TABLE IF NOT EXISTS beside(a)"); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeRowsUpdateAndDelete(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	if err := st.PutWorktree(ctx, Worktree{Task: "a", Branch: "b", Path: "/p"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorktree(ctx, Worktree{Task: "a", Branch: "b2", Path: "/p2"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Worktrees(ctx)
	if err != nil || len(rows) != 1 || rows[0].Branch != "b2" || rows[0].Path != "/p2" {
		t.Fatalf("%+v %v", rows, err)
	}
	if err := st.DeleteWorktree(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.Worktrees(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestPreviousIsRemembered(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	if got, err := st.Previous(ctx); err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	for _, task := range []string{"one", "two"} {
		if err := st.SetPrevious(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := st.Previous(ctx); err != nil || got != "two" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestClosedStateReportsErrors(t *testing.T) {
	st, _ := openState(t)
	st.Close()
	ctx := context.Background()
	if _, err := st.Worktrees(ctx); err == nil {
		t.Error("Worktrees")
	}
	if err := st.PutWorktree(ctx, Worktree{Task: "a"}); err == nil {
		t.Error("PutWorktree")
	}
	if err := st.DeleteWorktree(ctx, "a"); err == nil {
		t.Error("DeleteWorktree")
	}
	if _, err := st.Previous(ctx); err == nil {
		t.Error("Previous")
	}
	if err := st.SetPrevious(ctx, "a"); err == nil {
		t.Error("SetPrevious")
	}
	if _, err := st.SchemaVersion(ctx); err == nil {
		t.Error("SchemaVersion")
	}
	if _, err := st.Cached(ctx, CacheDivergence, []string{"k"}); err == nil {
		t.Error("Cached")
	}
	if err := st.PutCached(ctx, CacheDivergence, map[string]string{"k": "v"}); err == nil {
		t.Error("PutCached")
	}
	if err := st.Register(ctx, Resource{Task: "t", Execution: "e", Kind: "scratch", ID: "/x"}); err == nil {
		t.Error("Register")
	}
	if _, err := st.Resources(ctx, "t"); err == nil {
		t.Error("Resources")
	}
}

// --- cache

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestCacheKeyTakesOnlyObjectIDs(t *testing.T) {
	key, err := CacheKey(shaA, shaB)
	if err != nil || key != shaA+":"+shaB {
		t.Fatalf("%q %v", key, err)
	}
	for _, bad := range [][]string{{}, {""}, {"main"}, {shaA, "HEAD"}, {strings.ToUpper(shaA)}, {shaA[:39]}, {shaA + "a"}} {
		if key, err := CacheKey(bad...); err == nil {
			t.Errorf("%v accepted: %q", bad, key)
		}
	}
	sha256 := strings.Repeat("c", 64)
	if _, err := CacheKey(sha256); err != nil {
		t.Errorf("sha256: %v", err)
	}
}

func TestCacheRoundTripIsPerKind(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	if err := st.PutCached(ctx, CacheDivergence, map[string]string{"k1": "v1", "k2": "v2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutCached(ctx, CacheCommit, map[string]string{"k1": "other"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Cached(ctx, CacheDivergence, []string{"k1", "k2", "k3"})
	if err != nil || len(got) != 2 || got["k1"] != "v1" || got["k2"] != "v2" {
		t.Fatalf("%v %v", got, err)
	}
	if err := st.PutCached(ctx, CacheDivergence, map[string]string{"k1": "new"}); err != nil {
		t.Fatal(err)
	}
	got, err = st.Cached(ctx, CacheDivergence, []string{"k1"})
	if err != nil || got["k1"] != "new" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := st.Cached(ctx, CacheDivergence, nil); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if err := st.PutCached(ctx, CacheDivergence, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCacheReadsManyKeys(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	values := map[string]string{}
	var keys []string
	for i := range 2500 {
		k := fmt.Sprintf("k%d", i)
		values[k] = "v"
		keys = append(keys, k)
	}
	if err := st.PutCached(ctx, CacheCommit, values); err != nil {
		t.Fatal(err)
	}
	got, err := st.Cached(ctx, CacheCommit, keys)
	if err != nil || len(got) != 2500 {
		t.Fatalf("%d %v", len(got), err)
	}
}

// --- register

func TestRegisterStoresAndIsIdempotent(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	res := Resource{Task: "t", Execution: "e1", Kind: KindProcess, ID: "123", Start: "Sat Nov  7 10:00:00 2026", Source: "hook"}
	for range 2 {
		if err := st.Register(ctx, res); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Register(ctx, Resource{Task: "other", Execution: "e2", Kind: KindScratch, ID: "/tmp/x"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Resources(ctx, "t")
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if got[0].Start != res.Start || got[0].Source != "hook" || got[0].RegisteredAt.IsZero() {
		t.Fatalf("%+v", got[0])
	}
	got[0].RegisteredAt = time.Time{}
	if got[0] != res {
		t.Fatalf("%+v", got[0])
	}
}

func TestRegisterKeepsAReusedIdentitySeparate(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	for _, start := range []string{"first", "second"} {
		if err := st.Register(ctx, Resource{Task: "t", Execution: "e", Kind: KindProcess, ID: "123", Start: start}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Resources(ctx, "t")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRegisterRefusesAnIncompleteIdentity(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	bad := []Resource{
		{Execution: "e", Kind: KindScratch, ID: "/x"},
		{Task: "t", Kind: KindScratch, ID: "/x"},
		{Task: "t", Execution: "e", ID: "/x"},
		{Task: "t", Execution: "e", Kind: KindScratch},
		{Task: "t", Execution: "e", Kind: "toaster", ID: "/x"},
		{Task: "t", Execution: "e", Kind: KindProcess, ID: "123"},
		{Task: "t", Execution: "e", Kind: KindProcess, ID: "abc", Start: "s"},
		{Task: "t", Execution: "e", Kind: KindProcess, ID: "0", Start: "s"},
		{Task: "t", Execution: "e", Kind: KindVolume, ID: "v"},
		{Task: "t", Execution: "e", Kind: KindScratch, ID: "relative"},
	}
	for _, res := range bad {
		if err := st.Register(ctx, res); err == nil {
			t.Errorf("%+v accepted", res)
		}
	}
	got, err := st.Resources(ctx, "t")
	if err != nil || len(got) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRegisterAcceptsEveryKind(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	good := []Resource{
		{Kind: KindProcess, ID: "1", Start: "s"},
		{Kind: KindContainer, ID: "abc"},
		{Kind: KindCompose, ID: "proj"},
		{Kind: KindVolume, ID: "v", Start: "s"},
		{Kind: KindNetwork, ID: "n", Start: "s"},
		{Kind: KindScratch, ID: "/x"},
		{Kind: KindDatabase, ID: "db"},
	}
	for _, res := range good {
		res.Task, res.Execution = "t", "e"
		if err := st.Register(ctx, res); err != nil {
			t.Errorf("%+v: %v", res, err)
		}
	}
	if got, err := st.Resources(ctx, "t"); err != nil || len(got) != len(good) {
		t.Fatalf("%d %v", len(got), err)
	}
}

// --- status parsing

func TestParseStatus(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "status-all.z"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseStatus(data)
	if err != nil {
		t.Fatal(err)
	}
	want := Status{Staged: true, Modified: true, Untracked: true, Conflicted: true, HasUpstream: true, Ahead: 2, Behind: 3}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got.Symbols() != "+!?✘" {
		t.Fatalf("symbols %q", got.Symbols())
	}
}

func TestParseStatusEntries(t *testing.T) {
	cases := []struct {
		in   string
		want Status
	}{
		{"", Status{}},
		{"1 M. N... 100644 100644 100644 a b f\x00", Status{Staged: true}},
		{"1 .M N... 100644 100644 100644 a b f\x00", Status{Modified: true}},
		{"1 MM N... 100644 100644 100644 a b f\x00", Status{Staged: true, Modified: true}},
		{"2 R. N... 100644 100644 100644 a b R100 new\x00old\x00", Status{Staged: true}},
		{"2 .R N... 100644 100644 100644 a b R100 new\x00? not-a-status-line\x00", Status{Modified: true}},
		{"u UU N... 100644 100644 100644 100644 a b c f\x00", Status{Conflicted: true}},
		{"? f\x00", Status{Untracked: true}},
		{"! ignored\x00", Status{}},
		{"# branch.oid abc\x00# branch.head main\x00", Status{}},
		{"# branch.upstream origin/main\x00", Status{HasUpstream: true}},
		{"# branch.ab +0 -0\x00", Status{}},
	}
	for _, c := range cases {
		got, err := ParseStatus([]byte(c.in))
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: %+v", c.in, got)
		}
	}
}

func TestParseStatusRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{
		"X something\x00",
		"1 M\x00",
		"2 R. N... 100644 100644 100644 a b R100 new\x00",
		"# branch.ab +x -0\x00",
		"# branch.ab +1 -y\x00",
		"# branch.ab +1\x00",
		"1 M. N... 100644",
	} {
		if got, err := ParseStatus([]byte(bad)); err == nil {
			t.Errorf("%q parsed: %+v", bad, got)
		}
	}
}

func TestSymbolsOrderAndEmpty(t *testing.T) {
	if got := (Status{}).Symbols(); got != "" {
		t.Fatalf("%q", got)
	}
	if got := (Status{Untracked: true, Staged: true}).Symbols(); got != "+?" {
		t.Fatalf("%q", got)
	}
}

// --- collection on a real repository

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(testGitEnv(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testGitEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return strings.HasPrefix(name, "GIT_")
	})
}

type fixture struct {
	repo  string
	trees string
	state *State
	trace *countTrace
	coll  *Collector
}

type countTrace struct {
	mu    sync.Mutex
	calls [][]string
}

func (c *countTrace) Trace(e gitrun.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, e.Args)
}

func (c *countTrace) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *countTrace) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = nil
}

// countingRefs counts the calls that fork, around a real reader.
type countingRefs struct {
	RefSource
	aheadBehind atomic.Int32
}

func (c *countingRefs) AheadBehind(ctx context.Context, base string) (map[string]gitrefs.BranchDivergence, error) {
	c.aheadBehind.Add(1)
	return c.RefSource.AheadBehind(ctx, base)
}

func newFixture(t *testing.T, worktrees int) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "nested.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "first subject")
	trees := filepath.Join(root, "trees")
	for i := range worktrees {
		name := fmt.Sprintf("t%02d", i)
		git(t, repo, "worktree", "add", "-q", "-b", "feat/"+name, filepath.Join(trees, name))
	}
	st, err := OpenState(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reader, err := gitrefs.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	trace := &countTrace{}
	return &fixture{repo: repo, trees: trees, state: st, trace: trace, coll: &Collector{
		Refs: reader, State: st, Git: &gitrun.Runner{Tracer: trace},
	}}
}

func rowByTask(t *testing.T, rows []Row, task string) Row {
	t.Helper()
	for _, row := range rows {
		if row.Task == task {
			return row
		}
	}
	t.Fatalf("no row %q in %+v", task, rows)
	return Row{}
}

func TestSkeletonForksNothing(t *testing.T) {
	f := newFixture(t, 3)
	rows, err := f.coll.Skeleton(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	if f.trace.count() != 0 {
		t.Fatalf("forks: %v", f.trace.calls)
	}
	if reason := f.coll.Refs.(*gitrefs.Reader).FallbackReason(); reason != "" {
		t.Fatalf("the reader fell back: %s", reason)
	}
	row := rowByTask(t, rows, "t01")
	if row.Branch != "feat/t01" || row.Path != filepath.Join(f.trees, "t01") || row.Head == "" || row.Detached || row.Locked {
		t.Fatalf("%+v", row)
	}
	if got := rowByTask(t, rows, "repo"); !got.Main || got.Branch != "main" {
		t.Fatalf("%+v", got)
	}
}

// The first paint target of 20 ms with twenty worktrees, taken as the best of
// several runs so that one scheduling pause on a busy machine does not count.
// Shared CI runners are skipped: the quiet-machine benchmark is the gate there.
func TestSkeletonOfTwentyWorktreesMeetsTheFirstPaintTarget(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("timing target is measured on a quiet machine, not a shared runner")
	}
	f := newFixture(t, 20)
	const target = 20 * time.Millisecond
	best := time.Hour
	for range 15 {
		start := time.Now()
		rows, err := f.coll.Skeleton(context.Background())
		elapsed := time.Since(start)
		if err != nil || len(rows) != 21 {
			t.Fatalf("%d rows, %v", len(rows), err)
		}
		best = min(best, elapsed)
	}
	if best >= target {
		t.Fatalf("the fastest of 15 skeletons missed the %v target", target)
	}
	if f.trace.count() != 0 {
		t.Fatalf("forks: %v", f.trace.calls)
	}
}

func TestSkeletonUsesRecordedTaskNames(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	if err := f.state.PutWorktree(ctx, Worktree{Task: "named-task", Branch: "feat/t00", Path: filepath.Join(f.trees, "t00")}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rowByTask(t, rows, "named-task")
}

func TestSkeletonReportsDetachedAndLocked(t *testing.T) {
	f := newFixture(t, 2)
	git(t, filepath.Join(f.trees, "t00"), "checkout", "-q", "--detach")
	git(t, f.repo, "worktree", "lock", filepath.Join(f.trees, "t01"))
	rows, err := f.coll.Skeleton(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); !row.Detached || row.Branch != "" {
		t.Fatalf("%+v", row)
	}
	if row := rowByTask(t, rows, "t01"); !row.Locked {
		t.Fatalf("%+v", row)
	}
	if row := rowByTask(t, rows, "t00"); row.Marks() != "⊘" {
		t.Fatalf("%q", row.Marks())
	}
	if row := rowByTask(t, rows, "t01"); row.Marks() != "⊞" {
		t.Fatalf("%q", row.Marks())
	}
}

func TestSkeletonReportsAReaderFailure(t *testing.T) {
	f := newFixture(t, 0)
	f.coll.Refs = failingRefs{}
	if _, err := f.coll.Skeleton(context.Background()); err == nil {
		t.Fatal("no error")
	}
	f.coll.Refs = failingRefs{headsOK: true}
	if _, err := f.coll.Skeleton(context.Background()); err == nil {
		t.Fatal("no error from refs")
	}
}

type failingRefs struct{ headsOK bool }

func (f failingRefs) Heads(context.Context) ([]gitrefs.Head, error) {
	if f.headsOK {
		return nil, nil
	}
	return nil, errors.New("heads failed")
}
func (failingRefs) Refs(context.Context) ([]gitrefs.Ref, error) {
	return nil, errors.New("refs failed")
}
func (failingRefs) AheadBehind(context.Context, string) (map[string]gitrefs.BranchDivergence, error) {
	return nil, errors.New("ahead-behind failed")
}

func TestDefaultBranchPrefersTheRemoteHead(t *testing.T) {
	refs := []gitrefs.Ref{
		{Name: "refs/heads/main"},
		{Name: "refs/heads/trunk"},
		{Name: "refs/remotes/origin/HEAD", Symref: "refs/remotes/origin/trunk"},
	}
	if got := defaultBranch(refs); got != "trunk" {
		t.Fatalf("%q", got)
	}
	if got := defaultBranch(refs[:2]); got != "main" {
		t.Fatalf("%q", got)
	}
	if got := defaultBranch([]gitrefs.Ref{{Name: "refs/heads/master"}, {Name: "refs/heads/x"}}); got != "master" {
		t.Fatalf("%q", got)
	}
	if got := defaultBranch([]gitrefs.Ref{{Name: "refs/heads/x"}}); got != "" {
		t.Fatalf("%q", got)
	}
	// A remote head with no local branch of that name says nothing locally.
	if got := defaultBranch([]gitrefs.Ref{{Name: "refs/heads/main"}, {Name: "refs/remotes/origin/HEAD", Symref: "refs/remotes/origin/gone"}}); got != "main" {
		t.Fatalf("%q", got)
	}
}

func TestFillReportsEveryCell(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	t0 := filepath.Join(f.trees, "t00")
	if err := os.WriteFile(filepath.Join(t0, "file.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t0, "new.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t1 := filepath.Join(f.trees, "t01")
	if err := os.WriteFile(filepath.Join(t1, "extra.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, t1, "add", ".")
	git(t, t1, "commit", "-q", "-m", "second subject")
	rows, err := f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r0 := rowByTask(t, rows, "t00")
	if r0.Err != nil || !r0.Status.Modified || !r0.Status.Untracked || r0.Status.Staged {
		t.Fatalf("%+v", r0)
	}
	r1 := rowByTask(t, rows, "t01")
	if r1.Divergence != (gitrefs.Divergence{Ahead: 1}) || !r1.HasDivergence {
		t.Fatalf("%+v", r1)
	}
	if r1.Subject != "second subject" || r1.CommitTime.IsZero() {
		t.Fatalf("%+v", r1)
	}
	if r0.Subject != "first subject" {
		t.Fatalf("%+v", r0)
	}
}

func TestStatusForksOncePerRowWithNoOptionalLocks(t *testing.T) {
	f := newFixture(t, 2)
	if _, err := f.coll.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	statuses := 0
	for _, args := range f.trace.calls {
		if slices.Contains(args, "status") {
			statuses++
			if args[0] != "--no-optional-locks" {
				t.Fatalf("%v", args)
			}
		}
	}
	if statuses != 3 {
		t.Fatalf("%d status calls: %v", statuses, f.trace.calls)
	}
}

// A file changed in a nested folder moves neither the index nor the root
// folder's mtime; the next collection still has to see it.
func TestANestedEditIsSeenOnTheNextCollection(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	rows, err := f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); row.Status != (Status{}) {
		t.Fatalf("%+v", row.Status)
	}
	path := filepath.Join(f.trees, "t00")
	rootBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "dir", "nested.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rootBefore.ModTime().Equal(rootAfter.ModTime()) {
		t.Fatal("the fixture moved the root mtime")
	}
	rows, err = f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); !row.Status.Modified {
		t.Fatalf("%+v", row.Status)
	}
}

func TestAnUnchangedTreeUsesTheCaches(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	counting := &countingRefs{RefSource: f.coll.Refs}
	f.coll.Refs = counting
	if _, err := f.coll.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if counting.aheadBehind.Load() != 1 {
		t.Fatalf("first: %d", counting.aheadBehind.Load())
	}
	f.trace.reset()
	rows, err := f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counting.aheadBehind.Load() != 1 {
		t.Fatalf("second collection forked ahead-behind: %d", counting.aheadBehind.Load())
	}
	for _, args := range f.trace.calls {
		if !slices.Contains(args, "status") {
			t.Fatalf("uncached call: %v", args)
		}
	}
	if row := rowByTask(t, rows, "t00"); row.Subject != "first subject" || !row.HasDivergence {
		t.Fatalf("%+v", row)
	}
}

func TestANewCommitMissesTheCache(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	if _, err := f.coll.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	t0 := filepath.Join(f.trees, "t00")
	if err := os.WriteFile(filepath.Join(t0, "more.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, t0, "add", ".")
	git(t, t0, "commit", "-q", "-m", "newer")
	rows, err := f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := rowByTask(t, rows, "t00")
	if row.Subject != "newer" || row.Divergence.Ahead != 1 {
		t.Fatalf("%+v", row)
	}
}

func TestStatusPoolStaysBounded(t *testing.T) {
	f := newFixture(t, 8)
	f.coll.Workers = 2
	var running, peak atomic.Int32
	f.coll.statusHook = func() func() {
		n := running.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return func() { running.Add(-1) }
	}
	if _, err := f.coll.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak %d, want 2", got)
	}
}

func TestDefaultPoolIsTheCoreCount(t *testing.T) {
	if got := (&Collector{}).workers(); got < 1 {
		t.Fatalf("%d", got)
	}
	if got := (&Collector{Workers: 3}).workers(); got != 3 {
		t.Fatalf("%d", got)
	}
}

func TestFillEmitsOneUpdatePerRowSerially(t *testing.T) {
	f := newFixture(t, 3)
	ctx := context.Background()
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var inside atomic.Int32
	seen := map[int]int{}
	err = f.coll.Fill(ctx, rows, func(index int, row Row) {
		if inside.Add(1) != 1 {
			t.Error("emit ran concurrently")
		}
		seen[index]++
		inside.Add(-1)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(rows) {
		t.Fatalf("%v", seen)
	}
}

func TestABrokenRowKeepsItsErrorAndOthersFill(t *testing.T) {
	f := newFixture(t, 2)
	ctx := context.Background()
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].Task == "t00" {
			rows[i].Path = filepath.Join(f.trees, "missing")
		}
	}
	if err := f.coll.Fill(ctx, rows, nil); err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); row.Err == nil {
		t.Fatalf("%+v", row)
	}
	if row := rowByTask(t, rows, "t01"); row.Err != nil {
		t.Fatalf("%+v", row)
	}
}

func TestDivergenceFailureMarksRowsAndKeepsStatus(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	f.coll.Refs = headsAndRefsOnly{f.coll.Refs}
	rows, err := f.coll.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	row := rowByTask(t, rows, "t00")
	if row.Err == nil || row.HasDivergence {
		t.Fatalf("%+v", row)
	}
	if row.Subject != "first subject" {
		t.Fatalf("%+v", row)
	}
}

type headsAndRefsOnly struct{ RefSource }

func (headsAndRefsOnly) AheadBehind(context.Context, string) (map[string]gitrefs.BranchDivergence, error) {
	return nil, errors.New("ahead-behind failed")
}

func TestACancelledFillReturnsTheContextError(t *testing.T) {
	f := newFixture(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := f.coll.Fill(ctx, rows, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnUnbornBranchHasNoCommitCells(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	st, err := OpenState(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reader, err := gitrefs.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	coll := &Collector{Refs: reader, State: st, Git: &gitrun.Runner{}}
	rows, err := coll.Collect(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows[0].Err != nil || rows[0].Head != "" || rows[0].HasDivergence || rows[0].Subject != "" {
		t.Fatalf("%+v", rows[0])
	}
}

func TestParseCommits(t *testing.T) {
	got, err := parseCommits([]byte(shaA + "\x001767323045\x00subject one\x00" + shaB + "\x001767323046\x00two\x00"))
	if err != nil || len(got) != 2 || got[shaA] != "1767323045\x00subject one" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{shaA + "\x001767323045\x00", shaA} {
		if got, err := parseCommits([]byte(bad)); err == nil {
			t.Errorf("%q parsed: %v", bad, got)
		}
	}
}

func TestDecodeCommit(t *testing.T) {
	when, subject, ok := decodeCommit("1767323045\x00a subject")
	if !ok || subject != "a subject" || when.Unix() != 1767323045 {
		t.Fatalf("%v %q %v", when, subject, ok)
	}
	for _, bad := range []string{"", "nonumber\x00s", "123"} {
		if _, _, ok := decodeCommit(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

func TestABareRepositoryCollectsItsLinkedWorktrees(t *testing.T) {
	f := newFixture(t, 0)
	root := filepath.Dir(f.repo)
	bare := filepath.Join(root, "bare.git")
	git(t, root, "clone", "-q", "--bare", f.repo, bare)
	linked := filepath.Join(root, "linked")
	git(t, bare, "worktree", "add", "-q", "-b", "feat/linked", linked)
	st, err := OpenState(bare)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reader, err := gitrefs.Open(bare)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := (&Collector{Refs: reader, State: st, Git: &gitrun.Runner{}}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !rows[0].Bare || rows[0].Err != nil {
		t.Fatalf("%+v", rows)
	}
	if row := rowByTask(t, rows, "linked"); row.Err != nil || row.Subject != "first subject" || !row.HasDivergence {
		t.Fatalf("%+v", row)
	}
}

func TestComposeNameKeepsOnlyWhatComposeAllows(t *testing.T) {
	if got := composeName("My.Repo-fix_1"); got != "my-repo-fix_1" {
		t.Fatalf("%q", got)
	}
}

func TestParseSelectorRefusesAnUnparsableLink(t *testing.T) {
	if got, err := ParseSelector("https://host/%zz/pull/1"); err == nil {
		t.Fatalf("parsed: %+v", got)
	}
}

func TestRegisterChecksPorts(t *testing.T) {
	st, _ := openState(t)
	ctx := context.Background()
	if err := st.Register(ctx, Resource{Task: "t", Execution: "e", Kind: KindPort, ID: "8080"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"0", "65536", "http"} {
		if st.Register(ctx, Resource{Task: "t", Execution: "e", Kind: KindPort, ID: bad}) == nil {
			t.Errorf("port %q accepted", bad)
		}
	}
}

func TestOpenStateRefusesAPlantedSideFileAndAFileAsCommonDir(t *testing.T) {
	common := t.TempDir()
	folder := filepath.Join(common, "herdsman")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(common, "elsewhere"), filepath.Join(folder, "state.db-wal")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenState(common); err == nil {
		t.Fatal("linked -wal accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenState(file); err == nil {
		t.Fatal("a file accepted as the common directory")
	}
}

func TestOpenStateReportsAnUnwritableCommonDir(t *testing.T) {
	common := t.TempDir()
	if err := os.Chmod(common, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(common, 0o700) })
	if _, err := OpenState(common); err == nil {
		t.Fatal("no error")
	}
}

func TestMigrateReportsAClosedDatabase(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := migrate(context.Background(), db, []string{"CREATE TABLE a(b)"}); err == nil {
		t.Fatal("no error")
	}
}

func TestIsLockedOnUnusualFolders(t *testing.T) {
	dir := t.TempDir()
	if isLocked(dir) {
		t.Error("no .git file")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a pointer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isLocked(dir) {
		t.Error("malformed .git file")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isLocked(dir) {
		t.Error("no lock file")
	}
	if err := os.Mkdir(filepath.Join(dir, "admin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin", "locked"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !isLocked(dir) {
		t.Error("relative gitdir with a lock file")
	}
}

func TestCollectReportsASkeletonFailure(t *testing.T) {
	if _, err := (&Collector{Refs: failingRefs{}}).Collect(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

func TestSkeletonReportsAClosedState(t *testing.T) {
	f := newFixture(t, 0)
	f.state.Close()
	if _, err := f.coll.Skeleton(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

// A database that stops answering costs the caches, never the table.
func TestFillWorksWithoutTheCache(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Close()
	if err := f.coll.Fill(ctx, rows, nil); err != nil {
		t.Fatal(err)
	}
	row := rowByTask(t, rows, "t00")
	if row.Err != nil || !row.HasDivergence || row.Subject != "first subject" {
		t.Fatalf("%+v", row)
	}
}

func TestAStatusFormatChangeIsAnErrorNotACleanTree(t *testing.T) {
	f := newFixture(t, 1)
	script := filepath.Join(t.TempDir(), "fake-git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'Z future\\000'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.coll.Git = &gitrun.Runner{Git: script}
	rows, err := f.coll.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); row.Err == nil || row.Status != (Status{}) {
		t.Fatalf("%+v", row)
	}
}

// refsFailingAt fails the nth call to Refs.
type refsFailingAt struct {
	RefSource
	n     int
	calls int
}

func (r *refsFailingAt) Refs(ctx context.Context) ([]gitrefs.Ref, error) {
	r.calls++
	if r.calls == r.n {
		return nil, errors.New("refs failed")
	}
	return r.RefSource.Refs(ctx)
}

func TestFillReportsARefReadFailure(t *testing.T) {
	f := newFixture(t, 1)
	ctx := context.Background()
	rows, err := f.coll.Skeleton(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.coll.Refs = &refsFailingAt{RefSource: f.coll.Refs, n: 1}
	if err := f.coll.Fill(ctx, rows, nil); err != nil {
		t.Fatal(err)
	}
	if row := rowByTask(t, rows, "t00"); row.Err == nil || row.HasDivergence {
		t.Fatalf("%+v", row)
	}
}

func TestDivergenceCodec(t *testing.T) {
	d, ok := decodeDivergence(encodeDivergence(gitrefs.Divergence{Ahead: 3, Behind: 4}))
	if !ok || d != (gitrefs.Divergence{Ahead: 3, Behind: 4}) {
		t.Fatalf("%+v %v", d, ok)
	}
	for _, bad := range []string{"", "3", "x 4", "3 y"} {
		if _, ok := decodeDivergence(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

// movingRefs answers AheadBehind as if the branch stood on another commit when
// git counted, then returns to the commit the row holds.
type movingRefs struct {
	RefSource
	branch, moved string
}

func (m movingRefs) AheadBehind(context.Context, string) (map[string]gitrefs.BranchDivergence, error) {
	return map[string]gitrefs.BranchDivergence{
		headsPrefix + m.branch: {Object: m.moved, Divergence: gitrefs.Divergence{Ahead: 99, Behind: 99}},
	}, nil
}

// A branch that moves away and back during the count must not store counts for
// the other commit under the key of the commit it is back on.
func TestABranchThatMovesAndReturnsDoesNotPoisonTheCache(t *testing.T) {
	f := newFixture(t, 1)
	c := f.coll
	rows, err := c.Skeleton(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := -1
	for i := range rows {
		if !rows[i].IsDefault && rows[i].Branch != "" {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("no branch row in %+v", rows)
	}
	c.Refs = movingRefs{RefSource: c.Refs, branch: rows[row].Branch, moved: strings.Repeat("a", len(rows[row].Head))}
	if err := c.Fill(context.Background(), rows, nil); err != nil {
		t.Fatal(err)
	}
	if rows[row].HasDivergence {
		t.Fatalf("row shows counts for another commit: %+v", rows[row].Divergence)
	}
	base := ""
	for _, r := range rows {
		if r.IsDefault {
			base = r.Head
		}
	}
	key, err := CacheKey(rows[row].Head, base)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := c.State.Cached(context.Background(), CacheDivergence, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 0 {
		t.Fatalf("cache holds %v", cached)
	}
}
