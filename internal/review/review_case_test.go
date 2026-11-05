package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// P2: names compare case-folded only where git says the disk ignores case;
// with core.ignorecase false the preflight leaves local-notes and
// LOCAL-NOTES apart (git's own guard still decides at checkout).
func TestCaseFoldFollowsTheFilesystem(t *testing.T) {
	_, wt, _, tracking := ignoredRepo(t, "local-notes\nLOCAL-NOTES\n", "LOCAL-NOTES")
	if err := os.WriteFile(filepath.Join(wt, "local-notes"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, wt, "config", "core.ignorecase", "true")
	if got, err := ignoredCollisions(context.Background(), wt, tracking); err != nil || len(got) != 1 {
		t.Fatalf("folded: %v %v", got, err)
	}
	gitT(t, wt, "config", "core.ignorecase", "false")
	if got, err := ignoredCollisions(context.Background(), wt, tracking); err != nil || len(got) != 0 {
		t.Fatalf("exact: %v %v", got, err)
	}
}
