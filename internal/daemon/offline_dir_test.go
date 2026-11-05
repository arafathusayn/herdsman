package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfflineStatusEmptyDirIgnoresCwd: a route missing from routes.json
// has no inbox, and status with no daemon reports an empty daemon-down
// route; it never reads a state/owner.json of the working folder.
func TestOfflineStatusEmptyDirIgnoresCwd(t *testing.T) {
	ph, dir, _ := s16Route(t, true)
	b, _ := encode(RoutesFile{Version: 1, Routes: map[string]RouteEntry{}})
	if err := os.WriteFile(filepath.Join(ph, "daemon", "routes.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, errOut, code := s16Status(t, ph)
	if code != 1 || !strings.Contains(out, "route r epoch  last_seq 0 status daemon-down\n") || strings.Contains(out, "peer ") ||
		strings.Contains(errOut, "owner.json") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want an empty daemon-down route", code, out, errOut)
	}
	rep, store, err := offlineStatus("r", "", ph)
	if store || err != nil || rep.Inbox != "" || rep.Status != "daemon-down" {
		t.Errorf("offlineStatus(r, \"\") = %+v store=%v %v, want the empty file report", rep, store, err)
	}
}
