package gitrefs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// assertSameAnswer compares the reader with git on what both say about dir,
// whether the reader read directly or fell back: the same data, or an error
// from both.
func assertSameAnswer(t *testing.T, dir string, reader *Reader) {
	t.Helper()
	assertSameRefs(t, dir, reader)
	assertSameHeads(t, dir, reader)
}

func assertSameRefs(t *testing.T, dir string, reader *Reader) {
	t.Helper()
	got, err := reader.Refs(context.Background())
	out, gitErr := runGitErr(dir, "for-each-ref", forEachRefFormat)
	if (err == nil) != (gitErr == nil) {
		t.Fatalf("refs: reader %+v, %v; git %q, %v", got, err, out, gitErr)
	}
	if err != nil {
		return
	}
	want, parseErr := parseForEachRef(out)
	if parseErr != nil || !sameRefs(got, want) {
		t.Fatalf("refs differ (%v)\n got %+v\nwant %+v", parseErr, got, want)
	}
}

func assertSameHeads(t *testing.T, dir string, reader *Reader) {
	t.Helper()
	got, err := reader.Heads(context.Background())
	out, gitErr := runGitErr(dir, "worktree", "list", "--porcelain", "-z")
	if (err == nil) != (gitErr == nil) {
		t.Fatalf("heads: reader %+v, %v; git %q, %v", got, err, out, gitErr)
	}
	if err != nil {
		return
	}
	want, parseErr := parseWorktreeList(out)
	if parseErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("heads differ (%v)\n got %+v\nwant %+v", parseErr, got, want)
	}
}

func headOID(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// Item 1: a directory git refuses must not be answered by a repository around it.
func TestFallbackDoesNotDiscoverAnEnclosingRepository(t *testing.T) {
	outer := newRepo(t, "sha1")
	nested := filepath.Join(outer, "nested")
	writeFile(t, filepath.Join(nested, "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(nested, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := runGitErr(outer, "--git-dir="+nested, "for-each-ref"); err == nil {
		t.Fatalf("git accepted the folder: %q", out)
	}
	reader := openRepo(t, nested)
	if refs, err := reader.Refs(context.Background()); err == nil {
		t.Fatalf("refs = %+v, want git's refusal", refs)
	}
	if heads, err := reader.Heads(context.Background()); err == nil {
		t.Fatalf("heads = %+v, want git's refusal", heads)
	}
}

// Item 2.
func TestEmptySymref(t *testing.T) {
	for _, format := range objectFormats {
		for _, content := range []string{"ref:", "ref:   \n"} {
			t.Run(format+"/"+strconv.Quote(content), func(t *testing.T) {
				dir := newRepo(t, format)
				runGit(t, dir, "branch", "shadow")
				runGit(t, dir, "pack-refs", "--all", "--prune")
				for _, name := range []string{"empty", "shadow"} {
					writeFile(t, filepath.Join(dir, ".git/refs/heads", name), content)
				}
				assertSameAnswer(t, dir, openRepo(t, dir))
				writeFile(t, filepath.Join(dir, ".git/HEAD"), "ref: refs/heads/empty\n")
				assertSameAnswer(t, dir, openRepo(t, dir))
			})
		}
	}
}

// Item 3.
func TestSharedRootRefsNamedLikePrivateFolders(t *testing.T) {
	for _, format := range objectFormats {
		t.Run(format, func(t *testing.T) {
			dir := newRepo(t, format)
			wt := filepath.Join(filepath.Dir(dir), "linked")
			runGit(t, dir, "worktree", "add", "-q", "--detach", wt)
			oid := headOID(t, dir)
			for _, name := range perWorktreeRoots {
				runGit(t, dir, "update-ref", name, oid)
			}
			assertMatches(t, wt)
			assertMatches(t, dir)
		})
	}
}

// Item 4.
func TestPackedSyntax(t *testing.T) {
	const header = "# pack-refs with: peeled fully-peeled sorted \n"
	for _, format := range objectFormats {
		cases := map[string]func(oid, entry string) string{
			"nonhex peel":                         func(oid, entry string) string { return header + entry + "^" + strings.Repeat("z", len(oid)) + "\n" },
			"short peel":                          func(oid, entry string) string { return header + entry + "^abc\n" },
			"other hash width":                    func(oid, entry string) string { return header + entry + "^" + strings.Repeat("a", 104-len(oid)) + "\n" },
			"peel at end of file without newline": func(oid, entry string) string { return header + entry + "^" + oid },
			"orphan peel":                         func(oid, entry string) string { return header + "^" + oid + "\n" + entry },
			"second peel":                         func(oid, entry string) string { return header + entry + "^" + oid + "\n^" + oid + "\n" },
			"body comment":                        func(oid, entry string) string { return header + entry + "# comment\n" },
			"bad header":                          func(oid, entry string) string { return "# arbitrary header\n" + entry },
			"bad name":                            func(oid, entry string) string { return header + oid + " refs/heads/bad..name\n" },
			"tab separator":                       func(oid, entry string) string { return header + oid + "\trefs/heads/tabbed\n" },
			"null object":                         func(oid, entry string) string { return header + strings.Repeat("0", len(oid)) + " refs/heads/null\n" },
			"valid with peel":                     func(oid, entry string) string { return header + entry + "^" + oid + "\n" },
		}
		for name, build := range cases {
			t.Run(format+"/"+name, func(t *testing.T) {
				dir := newRepo(t, format)
				oid := headOID(t, dir)
				writeFile(t, filepath.Join(dir, ".git/packed-refs"), build(oid, oid+" refs/heads/packed\n"))
				assertSameRefs(t, dir, openRepo(t, dir))
			})
		}
	}
}

func TestLooseWhitespaceIsGits(t *testing.T) {
	for _, format := range objectFormats {
		for _, tail := range []string{"\v", "\f", " \n", "\t", "\r\n"} {
			t.Run(format+"/"+strconv.Quote(tail), func(t *testing.T) {
				dir := newRepo(t, format)
				oid := headOID(t, dir)
				writeFile(t, filepath.Join(dir, ".git/refs/heads/odd"), oid+tail)
				writeFile(t, filepath.Join(dir, ".git/refs/heads/odd-symref"), "ref: refs/heads/main"+tail)
				assertSameRefs(t, dir, openRepo(t, dir))
			})
		}
	}
}

func TestV1OnlyExtensionsAtVersionZero(t *testing.T) {
	for _, key := range []string{"extensions.refStorage=files", "extensions.relativeWorktrees=true"} {
		t.Run(key, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			name, value, _ := strings.Cut(key, "=")
			runGit(t, dir, "config", "core.repositoryformatversion", "0")
			runGit(t, dir, "config", name, value)
			assertSameAnswer(t, dir, openRepo(t, dir))
		})
	}
}

// Item 5: a file that claims sorted is searched by git, not repaired.
func TestPackedOrdering(t *testing.T) {
	headers := map[string]string{
		"claims sorted": "# pack-refs with: peeled fully-peeled sorted \n",
		"no claim":      "# pack-refs with: peeled fully-peeled \n",
		"no header":     "",
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			oid := headOID(t, dir)
			writeFile(t, filepath.Join(dir, ".git/packed-refs"), header+oid+" refs/heads/z\n"+oid+" refs/heads/a\n")
			writeFile(t, filepath.Join(dir, ".git/HEAD"), "ref: refs/heads/z\n")
			writeFile(t, filepath.Join(dir, ".git/refs/heads/alias"), "ref: refs/heads/z\n")
			assertSameAnswer(t, dir, openRepo(t, dir))
		})
	}
	t.Run("duplicate name", func(t *testing.T) {
		dir := newRepo(t, "sha1")
		oid := headOID(t, dir)
		writeFile(t, filepath.Join(dir, ".git/packed-refs"), oid+" refs/heads/a\n"+oid+" refs/heads/a\n")
		assertSameRefs(t, dir, openRepo(t, dir))
	})
}

// Item 6.
func TestStorageMigrationOnAnOpenReader(t *testing.T) {
	dir := newRepo(t, "sha1")
	runGit(t, dir, "branch", "side")
	reader := openRepo(t, dir)
	assertSameAnswer(t, dir, reader)
	runGit(t, dir, "refs", "migrate", "--ref-format=reftable")
	assertSameAnswer(t, dir, reader)
	if !strings.Contains(reader.FallbackReason(), "reftable") {
		t.Fatalf("reason = %q", reader.FallbackReason())
	}
	runGit(t, dir, "refs", "migrate", "--ref-format=files")
	assertMatches(t, dir)
	withoutForks(t, func() {
		if _, err := reader.Refs(context.Background()); err != nil {
			t.Fatalf("back on files, the same reader must read directly: %v", err)
		}
	})
}

// Item 7.
func TestLinkedEntries(t *testing.T) {
	setups := map[string]func(t *testing.T, entry string){
		"empty gitdir": func(t *testing.T, entry string) {
			writeFile(t, filepath.Join(entry, "gitdir"), "")
		},
		"gitdir with no .git suffix": func(t *testing.T, entry string) {
			writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(filepath.Dir(entry), "elsewhere", "notgit")+"\n")
		},
		"gitdir with only whitespace": func(t *testing.T, entry string) {
			writeFile(t, filepath.Join(entry, "gitdir"), " \n")
		},
		"registered entry with no head": func(t *testing.T, entry string) {
			if err := os.Remove(filepath.Join(entry, "HEAD")); err != nil {
				t.Fatal(err)
			}
		},
		"broken linked head": func(t *testing.T, entry string) {
			writeFile(t, filepath.Join(entry, "HEAD"), "garbage\n")
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t, "sha1")
			runGit(t, dir, "worktree", "add", "-q", "--detach", filepath.Join(filepath.Dir(dir), "linked"))
			setup(t, filepath.Join(dir, ".git/worktrees/linked"))
			assertSameHeads(t, dir, openRepo(t, dir))
		})
	}
}
