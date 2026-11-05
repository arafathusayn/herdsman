package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/route"
)

// A route file edited in place with its time put back is read again: the
// view shows the edit, and a page is from the edited file.
func TestRouteEditedWithItsTimeRestoredIsRead(t *testing.T) {
	f := newFixture(t)
	writeLongRunState(t, f, 300, 10, "")
	f.hub.refresh(context.Background())
	file := filepath.Join(f.routeDir, route.RouteFile)
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "line 299 ", "line 2X9 ", 1)
	if err := os.WriteFile(file, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	f.hub.refresh(context.Background())
	rs := f.hub.Current().Routes[0].RunState
	if got := rs[len(rs)-1].Text; !strings.HasPrefix(got, "line 2X9 ") {
		t.Errorf("the newest line is %q after the edit", got)
	}
	if code, p := pageAt(t, f, 201); code != http.StatusOK || len(p.Lines) != 100 {
		t.Errorf("a page after the edit: %d, %d lines", code, len(p.Lines))
	}
}
