package web

import (
	"fmt"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// A24: scroll marks live only while the pane does and while it is scrolled
// back, so pane churn never grows the map.
func TestScrollMarksArePruned(t *testing.T) {
	m := map[string]bool{}
	live := State{Snapshot: herdr.Snapshot{Panes: []herdr.Pane{{ID: "keep"}}}}
	for i := range 1000 {
		markScrolled(m, fmt.Sprintf("dead-%d", i), true, live)
	}
	markScrolled(m, "keep", true, live)
	if len(m) != 1 || !m["keep"] {
		t.Fatalf("marks %v", len(m))
	}
	markScrolled(m, "keep", false, live)
	if len(m) != 0 {
		t.Fatalf("a pane back at the bottom stays marked: %v", m)
	}
}
