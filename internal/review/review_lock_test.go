package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A checkout that fails for another reason than ignored work (here a held
// index lock) is an error, not a DirtyError, and the worktree stays.
func TestCheckoutFailureIsNotDirty(t *testing.T) {
	dir, first, second := repo(t)
	wt := filepath.Join(filepath.Dir(dir), "rv")
	if _, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: first, CacheRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	gd := gitT(t, wt, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gd, "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Repo: dir, Worktree: wt, Head: second, CacheRoot: t.TempDir()})
	var d *DirtyError
	if err == nil || errors.As(err, &d) || !strings.HasPrefix(err.Error(), "git checkout: ") {
		t.Fatalf("%v", err)
	}
}
