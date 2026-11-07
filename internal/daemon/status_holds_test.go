package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestStatusFilesOnlyShowsHolds: after the daemon stops, `message status`
// reads the saved state/hold-* records as the live reader does, so a hold
// and its owners stay visible; a record not in the owner format, or not
// strict JSON, is listed with no owners.
func TestStatusFilesOnlyShowsHolds(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	if _, _, code := h.msg(adj, "r", "hold", orch.name, "pause"); code != 0 {
		t.Fatal("hold")
	}
	if _, _, code := h.msg(orch, "r", "hold", orch.name, "mine too"); code != 0 {
		t.Fatal("second hold")
	}
	live, _, _ := h.msg(adj, "r", "status")
	want := "hold " + orch.name + " by " + strings.Join(slices.Sorted(slices.Values([]string{adj.name, orch.name})), " ") + "\n"
	if !strings.Contains(live, want) {
		t.Fatalf("live status: %s", live)
	}
	h.stop()
	for name, text := range map[string]string{"hold-other": `{"holds":7}`, "hold-broken": `{"holds":`} {
		if err := os.WriteFile(filepath.Join(dir, "state", name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, _, code := h.msg(adj, "r", "status")
	if code != 1 || !strings.HasPrefix(out, "daemon=down") || !strings.Contains(out, want) || !strings.Contains(out, "hold other by \n") || !strings.Contains(out, "hold broken by \n") {
		t.Fatalf("file status hides the saved holds: %d %s", code, out)
	}
	out, _, _ = h.msg(adj, "r", "--json", "status")
	var st StatusReport
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, x := range st.Holds {
		got[x.Target] = x.Owners
	}
	if len(got) != 3 || len(got[orch.name]) != 2 || got["other"] == nil || len(got["other"]) != 0 || len(got["broken"]) != 0 {
		t.Fatalf("json holds: %s", out)
	}
	// An inbox with no state folder has no holds.
	if st := FileStatus("r", t.TempDir()); len(st.Holds) != 0 {
		t.Fatalf("holds without a state folder: %v", st.Holds)
	}
}
