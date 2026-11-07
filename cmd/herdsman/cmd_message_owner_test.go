package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/daemon"
)

// TestRefusedOwnerRecordNotFileMode: with HERDSMAN_ROUTE and
// HERDSMAN_INBOX set, an owner record that is present but malformed or
// has a duplicate key takes the daemon path, never the file reader; for
// the route routes.json lists with that inbox, status reports
// owner-refused and reads nothing. An inbox with no
// owner record stays in file mode.
func TestRefusedOwnerRecordNotFileMode(t *testing.T) {
	for _, c := range []struct{ name, record string }{
		{"malformed", `{"owner":"daemon",`},
		{"duplicate key", `{"owner":"daemon","owner":"daemon","epoch":"0123456789abcdef0123456789abcdef","contract":2,"protocol":1}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(base, "inbox")
			if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "state", "owner.json"), []byte(c.record), 0o600); err != nil {
				t.Fatal(err)
			}
			ph := filepath.Join(base, "p")
			for k, v := range map[string]string{"HOME": filepath.Join(base, "h"), "HERDSMAN_PRIVATE_HOME": ph,
				"HERDSMAN_INBOX": dir, "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": "orch", "HERDR_PANE_ID": "p2"} {
				t.Setenv(k, v)
			}
			if _, _, _, ok := daemonPath([]string{"status"}, os.Getenv); !ok {
				t.Error("a refused owner record takes the file path")
			}
			// A route routes.json does not list has no inbox on the daemon
			// path: an empty daemon-down report, never the file reader's.
			var out, errb bytes.Buffer
			code := runMessage([]string{"--json", "status"}, nil, &out, &errb)
			if code != 1 || !strings.Contains(out.String(), `"inbox":""`) || strings.Contains(out.String(), `"mailboxes"`) {
				t.Fatalf("unlisted route: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
			}
			// The route listed with this inbox: the record is refused and
			// nothing is read.
			if err := os.MkdirAll(filepath.Join(ph, "daemon"), 0o700); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(daemon.RoutesFile{Version: 1, Routes: map[string]daemon.RouteEntry{"r": {Inbox: dir}}})
			if err := os.WriteFile(filepath.Join(ph, "daemon", "routes.json"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			errb.Reset()
			code = runMessage([]string{"--json", "status"}, nil, &out, &errb)
			if code != 1 || !strings.Contains(out.String(), `"owner-refused"`) || !strings.Contains(errb.String(), "owner record refused") || strings.Contains(out.String(), `"mailboxes"`) {
				t.Fatalf("listed route: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
			}
		})
	}
	t.Run("absent", func(t *testing.T) {
		base := t.TempDir()
		dir := filepath.Join(base, "inbox")
		if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{"HERDSMAN_PRIVATE_HOME": filepath.Join(base, "p"), "HERDSMAN_INBOX": dir, "HERDSMAN_ROUTE": "r"}
		if _, _, _, ok := daemonPath([]string{"status"}, func(k string) string { return env[k] }); ok {
			t.Error("an inbox with no owner record takes the daemon path")
		}
	})
}
