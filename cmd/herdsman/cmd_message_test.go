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

// transportFixture is a private home whose routes.json maps route r to a
// daemon-owned inbox, and a file-mode inbox next to it.
func transportFixture(t *testing.T) (ph, owned, file string) {
	t.Helper()
	base := t.TempDir()
	ph, owned, file = filepath.Join(base, "p"), filepath.Join(base, "owned"), filepath.Join(base, "file")
	for _, d := range []string{filepath.Join(ph, "daemon"), filepath.Join(owned, "state"), filepath.Join(file, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(owned, "state", "owner.json"), []byte(`{"owner":"daemon","epoch":"e"}`), 0o600)
	b, _ := json.Marshal(daemon.RoutesFile{Version: 1, Routes: map[string]daemon.RouteEntry{"r": {Inbox: owned}}})
	_ = os.WriteFile(filepath.Join(ph, "daemon", "routes.json"), b, 0o600)
	return ph, owned, file
}

// TestTransportByInboxOwner: the daemon path is taken when the inbox's
// owner record says daemon, the file path otherwise, whatever
// HERDSMAN_ROUTE says (design 12.1).
func TestTransportByInboxOwner(t *testing.T) {
	ph, owned, file := transportFixture(t)
	cases := []struct {
		name  string
		env   map[string]string
		args  []string
		route string
		ok    bool
	}{
		{"file inbox with a route", map[string]string{"HERDSMAN_ROUTE": "r2", "HERDSMAN_INBOX": file}, []string{"status"}, "r2", false},
		{"owned inbox without a route", map[string]string{"HERDSMAN_INBOX": owned}, []string{"status"}, "r", true},
		{"route of an owned inbox", map[string]string{"HERDSMAN_ROUTE": "r"}, []string{"send", "a", "b", "c"}, "r", true},
		{"--route of an owned inbox", nil, []string{"--route", "r", "read"}, "r", true},
		{"file-only action", map[string]string{"HERDSMAN_INBOX": owned}, []string{"courier"}, "", false},
		{"unknown route, no inbox", map[string]string{"HERDSMAN_ROUTE": "nope"}, []string{"status"}, "nope", false},
		{"enroll always", map[string]string{"HERDSMAN_ROUTE": "new"}, []string{"enroll", "a", "adjutant"}, "new", true},
	}
	for _, c := range cases {
		env := map[string]string{"HERDSMAN_PRIVATE_HOME": ph}
		for k, v := range c.env {
			env[k] = v
		}
		route, _, _, ok := daemonPath(c.args, func(k string) string { return env[k] })
		if ok != c.ok || route != c.route {
			t.Errorf("%s: route %q daemon %v, want %q %v", c.name, route, ok, c.route, c.ok)
		}
	}
}

// TestMessageHelpOnDaemonPath: help on a daemon-owned route prints the
// daemon grammar and its exit table, exit 0, also after an action.
func TestMessageHelpOnDaemonPath(t *testing.T) {
	ph, _, _ := transportFixture(t)
	t.Setenv("HERDSMAN_PRIVATE_HOME", ph)
	t.Setenv("HERDSMAN_INBOX", "")
	for _, args := range [][]string{{"--route", "r", "--help"}, {"--route", "r", "wait", "--help"}} {
		var out, errb bytes.Buffer
		if code := runMessage(args, nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "wait [--after <cursor>]") {
			t.Fatalf("%v: %d %q %q", args, code, out.String(), errb.String())
		}
	}
}
