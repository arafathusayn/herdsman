package route_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// When the run state is not the file's last section, or has no header,
// the page comes from a whole read; a missing file is an error.
func TestPageRunStateFallsBack(t *testing.T) {
	dir := t.TempDir()
	body := head + "- 10:00 one\n- 10:01 two\n- 10:02 three\n\n## Notes\n\n- 10:03 not a run line\n"
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := route.PageRunState(dir, 3, 2, 3)
	if err != nil || len(lines) != 2 || lines[0].Text != "two" || lines[1].Text != "three" {
		t.Fatalf("%+v %v", lines, err)
	}
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte("- 10:00 one\n- 10:01 two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lines, err := route.PageRunState(dir, 2, 5, 2); err != nil || len(lines) != 0 {
		t.Fatalf("no header: %+v %v", lines, err)
	}
	if lines, err := route.PageRunState(dir, 0, 5, 2); err != nil || len(lines) != 0 {
		t.Fatalf("before 0: %+v %v", lines, err)
	}
	if _, err := route.PageRunState(filepath.Join(dir, "none"), 1, 5, 1); err == nil {
		t.Error("a missing file paged")
	}
	// A file whose last line has no newline: that line is not counted.
	body = head + "- 10:00 one\n- 10:01 two\n- 10:02 partial"
	if err := os.WriteFile(filepath.Join(dir, route.RouteFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if lines, _ := route.PageRunState(dir, 2, 1, 2); len(lines) != 1 || !strings.HasPrefix(lines[0].Text, "two") {
		t.Fatalf("partial: %+v", lines)
	}
}
