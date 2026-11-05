package route_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

func writeRoute(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tailOf(t *testing.T, dir string) *route.Tail {
	t.Helper()
	tail, err := route.LoadBounded(dir, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tail
}

// A page is read the same whatever follows the run state, skips other
// sections' lines and the partial last line, and is refused, not misread,
// when the file no longer holds the snapshot's lines.
func TestPageEveryLayout(t *testing.T) {
	dir := t.TempDir()
	writeRoute(t, dir, head+"- 10:00 one\n- 10:01 two\n\n## Notes\n\n- 10:03 not a run line\n\n## Run state\n\n- 10:04 three\n- 10:05 partial")
	tail := tailOf(t, dir)
	if tail.Total != 3 {
		t.Fatalf("total %d", tail.Total)
	}
	lines, err := tail.Page(3, 5)
	if err != nil || len(lines) != 3 || lines[1].Text != "two" || lines[2].Text != "three" {
		t.Fatalf("%+v %v", lines, err)
	}
	if lines, err := tail.Page(0, 5); err != nil || len(lines) != 0 {
		t.Fatalf("before 0: %+v %v", lines, err)
	}
	// Shrunk: the file ends before the snapshot's lines do.
	writeRoute(t, dir, head+"- 10:00 one\n")
	if _, err := tail.Page(3, 5); !errors.Is(err, route.ErrChanged) {
		t.Fatalf("shrunk: %v", err)
	}
	// Edited before the mark: the mark no longer starts a run-state line.
	writeRoute(t, dir, strings.Replace(head, "# Route", "# A longer route title", 1)+"- 10:00 one\n- 10:01 two\n- 10:02 three\n")
	if _, err := tail.Page(3, 5); !errors.Is(err, route.ErrChanged) {
		t.Fatalf("edited: %v", err)
	}
	// Gone: an error, not a page.
	if err := os.Remove(filepath.Join(dir, route.RouteFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := tail.Page(3, 5); err == nil || errors.Is(err, route.ErrChanged) {
		t.Fatalf("gone: %v", err)
	}
}

// A route "file" that is a folder cannot be read by any of the readers.
func TestRouteFileThatIsAFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, route.RouteFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := route.Load(dir); err == nil {
		t.Error("Load read a folder")
	}
	if _, err := route.LoadBounded(dir, 10, nil); err == nil {
		t.Error("LoadBounded read a folder")
	}
}
