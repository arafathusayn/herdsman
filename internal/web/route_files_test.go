package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A file written into the route folder shows on the next refresh, while
// the route file itself is unchanged.
func TestReviewerRouteFilesUpdateWithoutRouteLogChange(t *testing.T) {
	f := newFixture(t)
	has := func(name string) bool {
		for _, file := range f.hub.Current().Routes[0].Files {
			if filepath.Base(file.Path) == name {
				return true
			}
		}
		return false
	}
	if has("review-1.md") {
		t.Fatal("present before it was written")
	}
	if err := os.WriteFile(filepath.Join(f.routeDir, "review-1.md"), []byte("# review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	if !has("review-1.md") {
		t.Fatal("the new document is missing")
	}
}
