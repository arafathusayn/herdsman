package gitrefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// shimGit puts a git on PATH that logs each call and runs the real one, and
// returns a function that reports the logged calls.
func shimGit(t *testing.T) func() []string {
	t.Helper()
	dir := tempDir(t)
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\nexec '" + gitBinary + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return func() []string {
		data, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func callsContaining(calls []string, word string) int {
	count := 0
	for _, call := range calls {
		if strings.Contains(call, word) {
			count++
		}
	}
	return count
}

func openRepo(t *testing.T, dir string) *Reader {
	t.Helper()
	reader, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// assertFallback checks that the reader reports reason and that one fork each
// gives the same answer as git.
func assertFallback(t *testing.T, dir, reason string) {
	t.Helper()
	want := wantRefs(t, dir)
	reader := openRepo(t, dir)
	if got := reader.FallbackReason(); !strings.Contains(got, reason) {
		t.Fatalf("reason = %q, want %q", got, reason)
	}
	calls := shimGit(t)
	refs, err := reader.Refs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sameRefs(refs, want) {
		t.Fatalf("refs differ\n got %+v\nwant %+v", refs, want)
	}
	if _, err := reader.Heads(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 2 || callsContaining(got, "for-each-ref") != 1 || callsContaining(got, "worktree list") != 1 {
		t.Fatalf("forks = %q, want one for-each-ref and one worktree list", got)
	}
}

func TestReftableTakesTheFallback(t *testing.T) {
	for _, format := range objectFormats {
		t.Run(format, func(t *testing.T) {
			dir := initRepo(t, format, "--ref-format=reftable")
			oid := commit(t, dir, "first")
			runGit(t, dir, "branch", "side")
			runGit(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
			wt := filepath.Join(filepath.Dir(dir), "wt")
			runGit(t, dir, "worktree", "add", "-q", "--detach", wt)
			runGit(t, wt, "update-ref", "refs/worktree/only-here", oid)
			assertFallback(t, dir, "reftable")
			assertFallback(t, wt, "reftable")

			heads, err := openRepo(t, wt).Heads(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := []Head{
				{Path: dir, State: HeadAttached, Branch: "refs/heads/main", Object: oid},
				{Path: wt, State: HeadDetached, Object: oid},
			}
			if !reflect.DeepEqual(heads, want) {
				t.Fatalf("heads = %+v, want %+v", heads, want)
			}
		})
	}
}

func TestReftableUnbornHead(t *testing.T) {
	dir := initRepo(t, "sha1", "--ref-format=reftable")
	heads, err := openRepo(t, dir).Heads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Head{{Path: dir, State: HeadUnborn, Branch: "refs/heads/main"}}
	if !reflect.DeepEqual(heads, want) {
		t.Fatalf("heads = %+v, want %+v", heads, want)
	}
}

func TestRecognizedConfigurationsStayDirect(t *testing.T) {
	dir := newRepo(t, "sha1")
	for _, args := range [][]string{
		{"config", "extensions.preciousObjects", "true"},
		{"config", "extensions.partialClone", "origin"},
		{"config", "core.repositoryformatversion", "1"},
		{"config", "extensions.refStorage", "files"},
		{"config", "extensions.noop", "x"},
	} {
		runGit(t, dir, args...)
	}
	assertMatches(t, dir)
}

func TestUnrecognizedRepositoriesFallBack(t *testing.T) {
	cases := map[string]struct {
		setup  func(t *testing.T, dir string)
		reason string
	}{
		"worktree config": {func(t *testing.T, dir string) {
			runGit(t, dir, "config", "extensions.worktreeConfig", "true")
		}, "worktreeconfig"},
		"core.worktree": {func(t *testing.T, dir string) {
			runGit(t, dir, "config", "core.worktree", dir)
		}, "core.worktree"},
		"include": {func(t *testing.T, dir string) {
			runGit(t, dir, "config", "include.path", "other")
		}, "include"},
		"unset bare": {func(t *testing.T, dir string) {
			runGit(t, dir, "config", "--unset", "core.bare")
		}, "core.bare"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			tc.setup(t, dir)
			assertFallback(t, dir, tc.reason)
		})
	}
}

func bareSkeleton(t *testing.T, config string) string {
	t.Helper()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(dir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "config"), config)
	return dir
}

func TestConfigClassification(t *testing.T) {
	const bare = "[core]\n\tbare = true\n"
	cases := []struct{ name, config, reason string }{
		{"plain", bare, ""},
		{"version one", "[core]\nrepositoryformatversion=1\nbare=yes\n", ""},
		{"same line key", "[core] bare = on\n", ""},
		{"bare key without value", "[core]\n\tbare\n", ""},
		{"false value", "[core]\nbare=off\n", ""},
		{"zero and one", "[core]\nbare=0\nbare=1\n", ""},
		{"comments, subsections and other sections", "# c\n; d\n[core \"x\"]\nbare=maybe\n[user]\nname = \"a;b\"\n" + bare, ""},
		{"sha256", "[core]\nrepositoryformatversion=1\nbare=true\n[extensions]\nobjectformat=sha256\nrelativeworktrees=true\n", ""},
		{"sha1 named", "[core]\nrepositoryformatversion=1\nbare=true\n[extensions]\nobjectformat=sha1\n", ""},
		{"continuation", bare + "x = a\\\n", "continuation"},
		{"unterminated section", "[core\nbare=true\n", "section"},
		{"includeIf", bare + "[includeIf \"gitdir:x\"]\npath=y\n", "include"},
		{"quoted value", "[core]\nbare=\"true\"\n", "core.bare"},
		{"odd bool", "[core]\nbare=maybe\n", "core.bare"},
		{"vertical tab is not git whitespace", "[core]\nbare=true\v\n", "core.bare"},
		{"future version", "[core]\nrepositoryformatversion=2\nbare=true\n", "repositoryformatversion"},
		{"unknown extension", bare + "[extensions]\nfuture=1\n", "extensions.future"},
		{"reftable", bare + "[extensions]\nrefstorage=reftable\n", "reftable"},
		{"odd format", "[core]\nrepositoryformatversion=1\nbare=true\n[extensions]\nobjectformat=md5\n", "objectformat"},
		{"format at version zero", bare + "[extensions]\nobjectformat=sha256\n", "objectformat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openRepo(t, bareSkeleton(t, tc.config)).FallbackReason()
			if tc.reason == "" && got != "" || !strings.Contains(got, tc.reason) {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
	t.Run("config missing", func(t *testing.T) {
		dir := bareSkeleton(t, bare)
		if err := os.Remove(filepath.Join(dir, "config")); err != nil {
			t.Fatal(err)
		}
		if got := openRepo(t, dir).FallbackReason(); !strings.Contains(got, "config") {
			t.Fatalf("reason = %q", got)
		}
	})
}

func TestReadRacingAConfigChangeFallsBack(t *testing.T) {
	dir := newRepo(t, "sha1")
	reader := openRepo(t, dir)
	read := func(*repoState) (string, error) {
		writeFile(t, filepath.Join(dir, ".git/config.new"), "[core]\nbare = false\n\n")
		if err := os.Rename(filepath.Join(dir, ".git/config.new"), filepath.Join(dir, ".git/config")); err != nil {
			t.Fatal(err)
		}
		return "read", nil
	}
	fork := func(context.Context) (string, error) { return "forked", nil }
	if got, err := through(context.Background(), reader, read, fork); err != nil || got != "forked" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestOpen(t *testing.T) {
	if _, err := Open(filepath.Join(tempDir(t), "absent")); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("absent path: %v", err)
	}
	if _, err := Open(tempDir(t)); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("empty dir: %v", err)
	}
	bad := tempDir(t)
	writeFile(t, filepath.Join(bad, ".git"), "not a git file\n")
	if _, err := Open(bad); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("bad git file: %v", err)
	}
	dangling := tempDir(t)
	writeFile(t, filepath.Join(dangling, ".git"), "gitdir: nowhere\n")
	if _, err := Open(dangling); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("dangling git file: %v", err)
	}
	real := newRepo(t, "sha1")
	link := filepath.Join(tempDir(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	heads, err := openRepo(t, link).Heads(context.Background())
	if err != nil || heads[0].Path != real {
		t.Fatalf("heads = %+v, %v", heads, err)
	}
}

// A read that meets something it does not understand answers through git and
// stays on git for the rest of the reader's life.
func TestReadTimeFallback(t *testing.T) {
	gitDir := func(dir string) string { return filepath.Join(dir, ".git") }
	cases := map[string]struct {
		setup  func(t *testing.T, dir string)
		reason string
	}{
		"symlinked loose ref": {func(t *testing.T, dir string) {
			if err := os.Symlink("main", filepath.Join(gitDir(dir), "refs/heads/link")); err != nil {
				t.Fatal(err)
			}
		}, "symlink"},
		"symref into another worktree": {func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(gitDir(dir), "refs/heads/cross"), "ref: main-worktree/HEAD\n")
		}, "worktree"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			tc.setup(t, dir)
			want := wantRefs(t, dir)
			reader := openRepo(t, dir)
			calls := shimGit(t)
			refs, err := reader.Refs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !sameRefs(refs, want) {
				t.Fatalf("refs differ\n got %+v\nwant %+v", refs, want)
			}
			if got := reader.FallbackReason(); !strings.Contains(got, tc.reason) {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
			if _, err := reader.Heads(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := calls(); len(got) != 2 {
				t.Fatalf("forks = %q", got)
			}
		})
	}
}

func TestHeadReadFallsBack(t *testing.T) {
	t.Run("broken head", func(t *testing.T) {
		dir := newRepo(t, "sha1")
		writeFile(t, filepath.Join(dir, ".git/refs/heads/main"), "garbage\n")
		reader := openRepo(t, dir)
		assertSameHeads(t, dir, reader)
		calls := shimGit(t)
		if _, err := reader.Heads(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := calls(); len(got) != 1 {
			t.Fatalf("forks = %q", got)
		}
	})
	t.Run("symlinked head", func(t *testing.T) {
		dir := newRepo(t, "sha1")
		head := filepath.Join(dir, ".git/HEAD")
		if err := os.Remove(head); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("refs/heads/main", head); err != nil {
			t.Fatal(err)
		}
		reader := openRepo(t, dir)
		assertSameHeads(t, dir, reader)
		if !strings.Contains(reader.FallbackReason(), "symlink") {
			t.Fatalf("reason = %q", reader.FallbackReason())
		}
	})
	t.Run("malformed packed-refs under a packed head", func(t *testing.T) {
		dir := newRepo(t, "sha1")
		if err := os.Remove(filepath.Join(dir, ".git/refs/heads/main")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, ".git/packed-refs"), "garbage\n")
		reader := openRepo(t, dir)
		assertSameHeads(t, dir, reader)
		if !strings.Contains(reader.FallbackReason(), "packed-refs") {
			t.Fatalf("reason = %q", reader.FallbackReason())
		}
	})
	t.Run("unreadable worktrees folder", func(t *testing.T) {
		dir := newRepo(t, "sha1")
		listing := filepath.Join(dir, ".git/worktrees")
		if err := os.Mkdir(listing, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(listing, 0o755) })
		reader := openRepo(t, dir)
		calls := shimGit(t)
		reader.Heads(context.Background())
		if got := calls(); len(got) != 1 {
			t.Fatalf("forks = %q", got)
		}
	})
}

func TestUnreadableFilesFallBack(t *testing.T) {
	cases := map[string]string{
		"loose directory": ".git/refs/heads",
		"loose file":      ".git/refs/heads/main",
		"packed-refs":     ".git/packed-refs",
	}
	for name, relative := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			runGit(t, dir, "branch", "side")
			if name == "packed-refs" {
				runGit(t, dir, "pack-refs", "--all")
			}
			path := filepath.Join(dir, relative)
			if err := os.Chmod(path, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o755) })
			reader := openRepo(t, dir)
			calls := shimGit(t)
			reader.Refs(context.Background())
			if got := calls(); len(got) != 1 {
				t.Fatalf("forks = %q", got)
			}
		})
	}
}

func TestUnreadablePerWorktreeRefsFallBack(t *testing.T) {
	dir := newRepo(t, "sha1")
	wt := filepath.Join(filepath.Dir(dir), "wt")
	runGit(t, dir, "worktree", "add", "-q", "--detach", wt)
	private := filepath.Join(dir, ".git/worktrees/wt/refs/bisect")
	if err := os.MkdirAll(private, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(private, 0o755) })
	reader := openRepo(t, wt)
	calls := shimGit(t)
	reader.Refs(context.Background())
	if got := calls(); len(got) != 1 {
		t.Fatalf("forks = %q", got)
	}
}

func TestMalformedPackedRefsSurfacesGitsError(t *testing.T) {
	dir := newRepo(t, "sha1")
	writeFile(t, filepath.Join(dir, ".git/packed-refs"), "this is not packed-refs\n")
	reader := openRepo(t, dir)
	shimGit(t)
	if _, err := reader.Refs(context.Background()); err == nil {
		t.Fatal("git rejects this file, so the reader must report an error")
	}
	if !strings.Contains(reader.FallbackReason(), "packed-refs") {
		t.Fatalf("reason = %q", reader.FallbackReason())
	}
}

func TestPackedCacheReloadsOnlyWhenTheFileChanges(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "packed-refs")
	cache := &packedCache{path: path, hexLen: 40}
	refs, err := cache.current()
	if err != nil || refs != nil {
		t.Fatalf("absent file: %v, %v", refs, err)
	}
	first := strings.Repeat("1", 40) + " refs/heads/a\n"
	writeFile(t, path, first)
	refs, err = cache.current()
	if err != nil || len(refs) != 1 {
		t.Fatalf("first read: %v, %v", refs, err)
	}
	again, err := cache.current()
	if err != nil || &again[0] != &refs[0] {
		t.Fatalf("second read parsed again: %v", err)
	}
	next := filepath.Join(dir, "packed-refs.new")
	writeFile(t, next, first+strings.Repeat("2", 40)+" refs/heads/b\n")
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	refs, err = cache.current()
	if err != nil || len(refs) != 2 {
		t.Fatalf("after rename: %v, %v", refs, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if refs, err = cache.current(); err != nil || refs != nil {
		t.Fatalf("after removal: %v, %v", refs, err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = cache.current(); err == nil {
		t.Fatal("a directory in place of packed-refs must be an error")
	}
}

func TestParsePacked(t *testing.T) {
	data, err := os.ReadFile("testdata/packed-refs.sha1")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := parsePacked(string(data), 40)
	if err != nil {
		t.Fatal(err)
	}
	want := []packedRef{
		{"outside/refs", strings.Repeat("5", 40)},
		{"refs/heads/main", strings.Repeat("1", 40)},
		{"refs/heads/upper", strings.Repeat("a", 40)},
		{"refs/tags/annotated", strings.Repeat("2", 40)},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v", refs)
	}
	oid := strings.Repeat("1", 40)
	for name, input := range map[string]string{
		"no newline": oid + " refs/heads/a",
		"short line": "abc\n",
		"no space":   oid + "xrefs/heads/a\n",
		"not hex":    strings.Repeat("g", 40) + " refs/heads/a\n",
		"null":       strings.Repeat("0", 40) + " refs/heads/a\n",
		"empty line": "\n",
	} {
		if _, err := parsePacked(input, 40); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidRefname(t *testing.T) {
	for name, want := range map[string]bool{
		"HEAD": true, "refs/heads/a": true, "refs/heads/ünï": true, "refs/@x": true,
		"": false, "@": false, "refs/a.": false, "refs/a..b": false, "refs/a@{b": false,
		"refs/a b": false, "refs/a\x7f": false, "refs/a~": false, "refs/a^": false, "refs/a:": false,
		"refs/a?": false, "refs/a*": false, "refs/a[": false, "refs/a\\": false, "refs//a": false,
		"refs/a/": false, "refs/.a": false, "refs/a.lock": false, "refs/a.lock/b": false,
	} {
		if got := validRefname(name); got != want {
			t.Errorf("%q: got %v", name, got)
		}
	}
}

func TestForkOutputParsers(t *testing.T) {
	if refs, err := parseForEachRef("a\x00b\x00c\n"); err != nil || refs[0] != (Ref{"a", "b", "c"}) {
		t.Fatalf("for-each-ref: %+v, %v", refs, err)
	}
	if _, err := parseForEachRef("only one field\n"); err == nil {
		t.Fatal("for-each-ref: expected an error")
	}
	zero := strings.Repeat("0", 40)
	oid := strings.Repeat("1", 40)
	input := strings.Join([]string{
		"worktree /bare", "bare", "",
		"worktree /a", "HEAD " + oid, "branch refs/heads/a", "locked why", "",
		"worktree /b", "HEAD " + oid, "detached", "prunable why", "",
		"worktree /c", "HEAD " + zero, "branch refs/heads/c", "",
		"worktree /d", "HEAD " + zero, "detached", "",
	}, "\x00")
	got, err := parseWorktreeList(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []Head{
		{Path: "/bare", State: HeadBare},
		{Path: "/a", State: HeadAttached, Branch: "refs/heads/a", Object: oid},
		{Path: "/b", State: HeadDetached, Object: oid},
		{Path: "/c", State: HeadUnborn, Branch: "refs/heads/c"},
		{Path: "/d", State: HeadDetached},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree list = %+v", got)
	}
	if _, err := parseWorktreeList("HEAD " + oid + "\x00\x00"); err == nil {
		t.Fatal("worktree list: expected an error for a record with no path")
	}
	if got, err := parseAheadBehind("refs/heads/a\x001 2\n"); err != nil || got["refs/heads/a"] != (Divergence{1, 2}) {
		t.Fatalf("ahead-behind: %+v, %v", got, err)
	}
	for _, input := range []string{"no separator\n", "a\x00x 2\n", "a\x001 x\n", "a\x001\n"} {
		if _, err := parseAheadBehind(input); err == nil {
			t.Errorf("%q: expected an error", input)
		}
	}
}

func TestForkIgnoresTheCallersRepositoryEnvironment(t *testing.T) {
	dir := initRepo(t, "sha1", "--ref-format=reftable")
	commit(t, dir, "first")
	want := wantRefs(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(tempDir(t), "elsewhere"))
	refs, err := openRepo(t, dir).Refs(context.Background())
	if err != nil || !sameRefs(refs, want) {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
}

func TestForkFailureReportsGitsMessage(t *testing.T) {
	dir := bareSkeleton(t, "[core]\nrepositoryformatversion=9\n")
	reader := openRepo(t, dir)
	shimGit(t)
	if _, err := reader.Refs(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := reader.Heads(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Refs(ctx); err == nil {
		t.Fatal("expected a cancelled fork to fail")
	}
}

func TestAheadBehindForksOncePerCall(t *testing.T) {
	for _, format := range objectFormats {
		t.Run(format, func(t *testing.T) {
			dir := newRepo(t, format)
			names := []string{"one", "two", "three", "four"}
			for _, name := range names {
				runGit(t, dir, "branch", name)
			}
			runGit(t, dir, "checkout", "-q", "two")
			commit(t, dir, "two ahead 1")
			commit(t, dir, "two ahead 2")
			runGit(t, dir, "checkout", "-q", "main")
			commit(t, dir, "main moves")

			reader := openRepo(t, dir)
			calls := shimGit(t)
			got, err := reader.AheadBehind(context.Background(), "main")
			if err != nil {
				t.Fatal(err)
			}
			if forks := calls(); len(forks) != 1 {
				t.Fatalf("forks = %q", forks)
			}
			if len(got) != len(names)+1 {
				t.Fatalf("got %+v", got)
			}
			for branch, divergence := range got {
				out := strings.Fields(runGit(t, dir, "rev-list", "--left-right", "--count", branch+"...main"))
				ahead, _ := strconv.Atoi(out[0])
				behind, _ := strconv.Atoi(out[1])
				if divergence != (Divergence{ahead, behind}) {
					t.Errorf("%s: %+v, want %d %d", branch, divergence, ahead, behind)
				}
			}
		})
	}
}

func TestAheadBehindRejectsBadBases(t *testing.T) {
	dir := newRepo(t, "sha1")
	reader := openRepo(t, dir)
	for _, base := range []string{"", "a)b", "a%b", "a\nb"} {
		if _, err := reader.AheadBehind(context.Background(), base); err == nil {
			t.Errorf("%q: expected an error", base)
		}
	}
	if _, err := reader.AheadBehind(context.Background(), "no-such-branch"); err == nil {
		t.Fatal("expected git's error for an unknown base")
	}
}

func TestUnreadableHeadTargetFallsBack(t *testing.T) {
	dir := newRepo(t, "sha1")
	path := filepath.Join(dir, ".git/refs/heads/main")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	reader := openRepo(t, dir)
	calls := shimGit(t)
	reader.Heads(context.Background())
	if got := calls(); len(got) != 1 {
		t.Fatalf("forks = %q", got)
	}
	if reader.FallbackReason() == "" {
		t.Fatal("expected a fallback")
	}
}
