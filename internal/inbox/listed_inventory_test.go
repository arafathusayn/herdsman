package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// A listed inventory (the daemon's, for a herdr-core route) names as
// unknown every listed agent the boundary does not know, and a failed list
// is a failure, never an empty one.
func TestListedInventoryFiltersByTheBoundary(t *testing.T) {
	member := map[string]any{"pane": "exec:e-1", "session": map[string]any{"agent": "claude", "kind": "id", "source": "herdr-core", "value": "e-1"}}
	boundary := map[string]any{"pair": map[string]any{"orch": map[string]any{"identity": member}}, "courier_panes": []any{}, "lower_trust": []any{}}
	sess, _ := json.Marshal(member["session"])
	inv := ListedInventory{List: func(context.Context) ([]Unknown, error) {
		return []Unknown{{Pane: "exec:e-1", Agent: "claude", Session: sess}, {Pane: "t-9", Agent: "codex"}}, nil
	}}
	got, err := inv.Unknown(context.Background(), boundary)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Pane != "t-9" || string(got[0].Session) != "null" {
		t.Fatalf("unknown: %+v", got)
	}
	failed := ListedInventory{List: func(context.Context) ([]Unknown, error) { return nil, errors.New("down") }}
	if _, err := failed.Unknown(context.Background(), boundary); err == nil {
		t.Fatal("a failed list read as no unknown agent")
	}
	if _, err := (ListedInventory{}).Unknown(context.Background(), boundary); err == nil {
		t.Fatal("no list read as no unknown agent")
	}
}
