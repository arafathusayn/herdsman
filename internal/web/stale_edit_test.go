package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// One edit far from the end of a large route file, then no more writes:
// the refreshes keep checking the file until every part of it was checked
// once, so the edit shows within a bounded number of refreshes.
func TestOneEditShowsWithoutMoreWrites(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(f.routeDir, route.RouteFile)
	old := "ordinary note padded.........."
	repl := "Adjutant: pane wS:p9"
	repl += strings.Repeat(" ", len(old)-len(repl))
	row := strings.Repeat("y", 1023) + "\n"
	var b strings.Builder
	b.WriteString("# Route t\n\n## Notes\n")
	for range 5 << 10 {
		b.WriteString(row)
	}
	b.WriteString(old + "\n")
	for range 3 << 10 {
		b.WriteString(row)
	}
	b.WriteString("## Run state\n\n- 10:00 started\n")
	body := b.String()
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	fh, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteAt([]byte(repl), int64(strings.Index(body, old))); err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteAt([]byte("- 10:01 more\n"), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	// A cycle is the file's 1 MiB blocks, two per refresh.
	bound := len(body)/(1<<20)/2 + 2
	for range bound {
		f.hub.refresh(context.Background())
	}
	if got := f.hub.Current().Routes[0].AdjutantPane; got != "wS:p9" {
		t.Errorf("after %d refreshes and no more writes, the adjutant pane is %q", bound, got)
	}
}
