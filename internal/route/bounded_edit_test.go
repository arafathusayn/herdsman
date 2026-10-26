package route_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

func editedRoute(title, mark string, lines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Route %s\n\n## Checklist\n\n- [%s] A1 first item\n\n## Run state\n\n", title, mark)
	for i := range lines {
		fmt.Fprintf(&b, "- 10:00 line %d %s\n", i, strings.Repeat("x", 200))
	}
	return b.String()
}

// A24: an edit before the old end is read, whether the file kept its size
// or also grew; only bytes appended to an unchanged file are read alone.
func TestEditedMetadataIsReparsed(t *testing.T) {
	for _, grow := range []bool{false, true} {
		dir := t.TempDir()
		file := filepath.Join(dir, route.RouteFile)
		if err := os.WriteFile(file, []byte(editedRoute("Before", " ", 100)), 0o600); err != nil {
			t.Fatal(err)
		}
		prev, err := route.LoadBounded(dir, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		next := editedRoute("Behind", "x", 100)
		if grow {
			next += "- 10:01 appended\n"
		}
		if err := os.WriteFile(file, []byte(next), 0o600); err != nil {
			t.Fatal(err)
		}
		tail, err := route.LoadBounded(dir, 100, prev)
		if err != nil {
			t.Fatal(err)
		}
		r := tail.Route
		if r.Title != "Route Behind" || len(r.Checklist) != 1 || !r.Checklist[0].Done {
			t.Fatalf("grow %v: title %q, checklist %+v", grow, r.Title, r.Checklist)
		}
	}
}
