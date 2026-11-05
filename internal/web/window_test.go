package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The route view pages the newest 2,000 run-state records only; older
// ones stay in the route file, and the view says where.
func TestRunStateWindow(t *testing.T) {
	f := newFixture(t)
	writeLongRunState(t, f, 2500, 10, "")
	f.hub.refresh(context.Background())
	b, _ := json.Marshal(f.hub.Current().Routes[0])
	var rv map[string]any
	if err := json.Unmarshal(b, &rv); err != nil {
		t.Fatal(err)
	}
	// 2,501 records: the fixture's "started" and 2,500 more.
	if from, _ := rv["run_state_from"].(float64); from != 501 {
		t.Fatalf("run_state_from %v", rv["run_state_from"])
	}
	code, p := pageAt(t, f, 601)
	if code != http.StatusOK || len(p.Lines) != 100 || p.Older != 501 || !strings.HasPrefix(p.Lines[0].Text, "line 500 ") {
		t.Fatalf("%d %d lines, older %d", code, len(p.Lines), p.Older)
	}
	if code, p := pageAt(t, f, 501); code != http.StatusOK || len(p.Lines) != 0 || p.Older != 501 {
		t.Fatalf("past the window: %d %+v", code, p)
	}
}
