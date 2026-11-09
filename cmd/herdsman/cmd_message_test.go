package main

import (
	"bytes"
	"context"
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
	home := t.TempDir() // A daemon running on this machine must not be seen.
	for _, c := range cases {
		env := map[string]string{"HERDSMAN_PRIVATE_HOME": ph, "HOME": home}
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

// TestCLINamesOtherHome (F5): a route missing from the selected private
// home's registry, with a daemon-owned HERDSMAN_INBOX or with none at all,
// takes the daemon path while another private home's daemon runs, so the
// real CLI names that home.
func TestCLINamesOtherHome(t *testing.T) {
	for _, inbox := range []bool{true, false} {
		t.Run(map[bool]string{true: "inbox", false: "no_inbox"}[inbox], func(t *testing.T) { cliNamesOtherHome(t, inbox) })
	}
}

func cliNamesOtherHome(t *testing.T, withInbox bool) {
	base, err := os.MkdirTemp("", "hm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	home, live, selected, owned := filepath.Join(base, "h"), filepath.Join(base, "live"), filepath.Join(base, "other"), filepath.Join(base, "inbox")
	for _, dir := range []string{home, filepath.Join(owned, "state")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owned, "state", "owner.json"), []byte(`{"owner":"daemon","epoch":"0123456789abcdef0123456789abcdef","contract":2,"protocol":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := daemon.New(daemon.Config{Home: home, PrivateHome: live, Version: daemonVersion()})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	if !withInbox {
		owned = ""
	}
	for k, v := range map[string]string{"HOME": home, "HERDSMAN_PRIVATE_HOME": selected, "HERDSMAN_INBOX": owned,
		"HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": "orch", "HERDR_PANE_ID": "p2"} {
		t.Setenv(k, v)
	}
	var out, errOut bytes.Buffer
	code := runMessage([]string{"authority"}, nil, &out, &errOut)
	want := "the running daemon uses " + live + "; set HERDSMAN_PRIVATE_HOME"
	if code != 75 || !strings.Contains(errOut.String(), want) {
		t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, out.String(), errOut.String(), want)
	}
}

// TestNoInboxNoDaemonStaysFileMode: with no inbox and no live daemon of
// another private home, a route-only party keeps the file channel's
// answer.
func TestNoInboxNoDaemonStaysFileMode(t *testing.T) {
	base := t.TempDir()
	for k, v := range map[string]string{"HOME": filepath.Join(base, "h"), "HERDSMAN_PRIVATE_HOME": filepath.Join(base, "p"),
		"HERDSMAN_INBOX": "", "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": "orch", "HERDR_PANE_ID": "p2"} {
		t.Setenv(k, v)
	}
	if _, _, _, ok := daemonPath([]string{"authority"}, os.Getenv); ok {
		t.Fatal("daemon path taken with no inbox and no live daemon")
	}
}
