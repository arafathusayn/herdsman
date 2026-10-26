package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMessageCLIArguments: every message action refuses a wrong argument
// list with its grammar and exit 2, and the read-only actions print their
// JSON and text forms.
func TestMessageCLIArguments(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	body := writeFile(t, h.dir, "body.txt", "hello\n")
	empty := writeFile(t, h.dir, "empty.txt", "")
	usage := [][]string{
		{"status", "x"}, {"read", "a", "b"}, {"release"}, {"ack", "a"}, {"send", "a"},
		{"hold"}, {"unhold"}, {"enroll", "x"}, {"authority", "x"},
		{"wait", "--after"}, {"wait", "--after", "nope"}, {"wait", "--types", "bogus"}, {"wait", "--bogus"},
	}
	for _, a := range usage {
		if _, errOut, code := h.msg(adj, "r", a...); code != 2 || errOut == "" {
			t.Errorf("%v: %d %q", a, code, errOut)
		}
	}
	// No route named.
	if _, errOut, code := h.msg(adj, "", "status"); code != 2 || !strings.Contains(errOut, "HERDSMAN_ROUTE") {
		t.Errorf("no route: %d %q", code, errOut)
	}
	// No enrolled name.
	if _, errOut, code := h.msg(&party{pane: adj.pane}, "r", "read"); code == 0 || !strings.Contains(errOut, "HERDSMAN_NAME") {
		t.Errorf("no name: %d %q", code, errOut)
	}
	// Bodies and processing records that are not bounded regular files.
	if _, _, code := h.msg(adj, "r", "send", orch.name, filepath.Join(h.dir, "missing"), "brief"); code != 2 {
		t.Errorf("missing body: %d", code)
	}
	if _, _, code := h.msg(adj, "r", "send", orch.name, h.dir, "brief"); code != 2 {
		t.Errorf("folder body: %d", code)
	}
	bad := writeFile(t, h.dir, "bad.txt", "\xff\xfe")
	if _, _, code := h.msg(adj, "r", "send", orch.name, bad, "brief"); code != 2 {
		t.Errorf("non-UTF-8 body: %d", code)
	}
	out, _, code := h.msg(adj, "r", "send", orch.name, body, "brief")
	if code != 0 {
		t.Fatalf("send: %d %q", code, out)
	}
	id := strings.Fields(out)[0]
	out, _, code = h.msg(adj, "r", "send", orch.name, body, "result", id)
	if code != 0 {
		t.Fatalf("reply: %d %q", code, out)
	}
	if _, _, code := h.msg(orch, "r", "ack", "tok", id, empty); code != 2 {
		t.Errorf("empty processing record: %d", code)
	}
	if _, _, code := h.msg(orch, "r", "ack", "tok", id, filepath.Join(h.dir, "missing")); code != 2 {
		t.Errorf("missing processing record: %d", code)
	}
	// A wrong claim token is the daemon's refusal.
	if _, errOut, code := h.msg(orch, "r", "release", "nope"); code != 2 || !strings.Contains(errOut, "no such active claim") {
		t.Errorf("release: %d %q", code, errOut)
	}
	if _, _, code := h.msg(orch, "r", "read", "nope"); code != 2 {
		t.Errorf("extend a wrong claim: %d", code)
	}
	// Holds, in both forms.
	if out, _, code := h.msg(adj, "r", "--json", "hold", orch.name, "a reason"); code != 0 || !strings.Contains(out, `"holds"`) {
		t.Errorf("hold json: %d %q", code, out)
	}
	if out, _, code := h.msg(orch, "r", "hold", orch.name, "mine too"); code != 0 {
		t.Errorf("hold: %d %q", code, out)
	}
	if out, _, code := h.msg(adj, "r", "unhold", orch.name); code != 0 || !strings.Contains(out, "still held by: "+orch.name) {
		t.Errorf("unhold: %d %q", code, out)
	}
	if out, _, code := h.msg(adj, "r", "status"); code != 0 || !strings.Contains(out, "hold "+orch.name+" by "+orch.name) {
		t.Errorf("status with a hold: %d %q", code, out)
	}
	if _, _, code := h.msg(adj, "r", "unhold", "nobody"); code != 2 {
		t.Errorf("unhold of nothing: %d", code)
	}
	// Authority, in both forms: HUMAN exits 3.
	if out, _, code := h.msg(adj, "r", "authority"); code != 3 || out == "" {
		t.Errorf("authority: %d %q", code, out)
	}
	if out, _, code := h.msg(adj, "r", "--json", "authority"); code != 3 || !strings.Contains(out, `"authority":"HUMAN"`) {
		t.Errorf("authority json: %d %q", code, out)
	}
	// Enrollment's refusal in JSON form.
	if out, _, code := h.msg(&party{pane: "p3"}, "r", "--json", "enroll", "x-1", "adjutant"); code != 75 || !strings.Contains(out, `"identity_unavailable"`) {
		t.Errorf("enroll of an unknown pane: %d %q", code, out)
	}
	_ = dir
}

// TestDaemonAdminCLIArguments: the daemon commands refuse wrong argument
// lists with their grammar and exit 2, drive the supervisor, and print
// their JSON forms.
func TestDaemonAdminCLIArguments(t *testing.T) {
	h, f, sup := newSup(t, "linux")
	run := func(args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		a := &AdminEnv{Getenv: func(k string) string {
			if k == "HERDSMAN_PRIVATE_HOME" {
				return h.ph
			}
			return ""
		}, Stdin: strings.NewReader("n\n"), Stdout: &out, Stderr: &errb, Version: testVersion, Supervisor: sup}
		code := RunDaemon(a, args)
		return out.String(), errb.String(), code
	}
	usage := [][]string{
		{}, {"--private-home"}, {"run", "x"}, {"bogus"},
		{"route"}, {"route", "bogus"}, {"route", "create"}, {"route", "adopt"}, {"route", "remove"}, {"route", "reset-events"},
		{"setup"}, {"setup", "--rule", "x"}, {"setup", "r", "--nope"},
		{"pair"}, {"pair", "bogus"}, {"pair", "approve", "r"}, {"pair", "approve", "r", "s", "--nope"},
		{"pair", "approve", "r", "s", "--credential", "noequals"}, {"pair", "retire"},
		{"credential"}, {"credential", "x", "r", "n"},
	}
	for _, a := range usage {
		if _, errOut, code := run(a...); code != 2 || errOut == "" {
			t.Errorf("%v: %d %q", a, code, errOut)
		}
	}
	if out, _, code := run("--help"); code != 0 || !strings.Contains(out, "usage") {
		t.Errorf("help: %d %q", code, out)
	}
	// JSON forms against the running daemon.
	if out, _, code := run("--json", "route", "create", "--repo", "repo", "--route", "j"); code != 0 || !strings.Contains(out, `"inbox"`) {
		t.Errorf("create json: %d %q", code, out)
	}
	if out, _, code := run("--json", "status"); code != 1 || !strings.Contains(out, `"routes"`) {
		t.Errorf("status json: %d %q", code, out)
	}
	if out, _, code := run("route", "list"); code != 0 || !strings.Contains(out, "j held") {
		t.Errorf("list: %d %q", code, out)
	}
	if _, errOut, code := run("--json", "route", "remove", "nope"); code != 2 || errOut == "" {
		t.Errorf("remove json error: %d %q", code, errOut)
	}
	if _, _, code := run("credential", "revoke", "j", "nobody"); code != 2 {
		t.Errorf("revoke: %d", code)
	}
	if _, _, code := run("route", "adopt", "--inbox", "/nope", "--route", "a", "--confirm", "x"); code != 2 {
		t.Errorf("adopt: %d", code)
	}
	if _, _, code := run("pair", "retire", "j", "nobody"); code != 2 {
		t.Errorf("retire: %d", code)
	}
	// Setup files that are not JSON.
	notJSON := writeFile(t, h.dir, "nj", "nope")
	for _, flag := range []string{"--boundary", "--watch-targets", "--resume-targets"} {
		var out, errb bytes.Buffer
		a := &AdminEnv{Getenv: func(k string) string {
			if k == "HERDSMAN_PRIVATE_HOME" {
				return h.ph
			}
			return ""
		}, Stdin: strings.NewReader("y\n"), Stdout: &out, Stderr: &errb, Version: testVersion, Supervisor: sup, Attended: true}
		if code := RunDaemon(a, []string{"setup", "j", "--rule", h.rules, flag, notJSON}); code == 0 {
			t.Errorf("setup %s not JSON: %d %q", flag, code, out.String())
		}
	}
	// The supervisor commands.
	h.stop()
	if out, _, code := run("--json", "install"); code != 0 || !strings.Contains(out, `"installed":true`) {
		t.Fatalf("install: %d %q %v", code, out, f.calls)
	}
	if out, _, code := run("restart"); code != 0 || !strings.HasPrefix(out, "restarted herdsman "+testVersion) {
		t.Errorf("restart: %d %q", code, out)
	}
	if out, _, code := run("stop"); code != 0 || out != "stopped\n" {
		t.Errorf("stop: %d %q", code, out)
	}
	if out, _, code := run("status"); code != 1 || out != "daemon down\n" {
		t.Errorf("status down: %d %q", code, out)
	}
	if out, _, code := run("start"); code != 0 || !strings.HasPrefix(out, "started herdsman") {
		t.Errorf("start: %d %q", code, out)
	}
	if out, _, code := run("uninstall"); code != 0 || out != "uninstalled\n" {
		t.Errorf("uninstall: %d %q", code, out)
	}
	if exists(sup.UnitPath()) {
		t.Error("unit left")
	}
	_ = os.Remove(notJSON)
}
