package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/message"
)

// msg runs `herdsman message` on the daemon path as party p.
func (h *harness) msg(p *party, route string, args ...string) (string, string, int) {
	h.t.Helper()
	env := map[string]string{"HERDSMAN_PRIVATE_HOME": h.ph, "HERDSMAN_ROUTE": route, "HERDSMAN_NAME": p.name, "HERDR_PANE_ID": p.pane}
	var out, errb bytes.Buffer
	e := &MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: testVersion}
	if len(args) > 0 && args[0] == "--json" {
		e.JSON, args = true, args[1:]
	}
	code := RunMessage(e, args)
	return out.String(), errb.String(), code
}

func writeFile(t testing.TB, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMessageCLIRoundTrip(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	body := writeFile(t, h.dir, "body.md", "the brief")
	out, errOut, code := h.msg(adj, "r", "send", orch.name, body, "brief")
	if code != 0 || !regexp.MustCompile(`^m000000000001 queued; delivery requires receipt or status confirmation\n$`).MatchString(out) {
		t.Fatalf("send: %d %q %q", code, out, errOut)
	}
	out, _, code = h.msg(orch, "r", "read")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], `{"type":"claim","token":"`) ||
		!strings.Contains(lines[1], `"type":"manifest","id":"m000000000001"`) || !strings.Contains(lines[1], `"authority":"data-only"`) ||
		!strings.HasPrefix(lines[2], `{"type":"summary","pending_new":0,"quarantine":0}`) {
		t.Fatalf("read: %d %s", code, out)
	}
	var claim struct{ Token string }
	_ = json.Unmarshal([]byte(lines[0]), &claim)
	rec := writeFile(t, h.dir, "rec.md", "taken in")
	out, _, code = h.msg(orch, "r", "ack", claim.Token, "m000000000001", rec)
	if code != 0 || out != "acknowledged m000000000001\n" {
		t.Fatalf("ack: %d %q", code, out)
	}
	out, _, code = h.msg(orch, "r", "--json", "ack", claim.Token, "m000000000001", rec)
	if code != 0 || out != `{"type":"acknowledged","id":"m000000000001","already":true}`+"\n" {
		t.Fatalf("ack json: %d %q", code, out)
	}
	out, _, code = h.msg(orch, "r", "release", claim.Token)
	if code != 0 || out != `{"type":"released"}`+"\n" {
		t.Fatalf("release: %d %q", code, out)
	}
	out, _, code = h.msg(adj, "r", "--json", "send", orch.name, body, "result")
	if code != 0 || !strings.HasPrefix(out, `{"type":"queued","id":"m000000000002","seq":`) {
		t.Fatalf("send json: %d %q", code, out)
	}
	out, _, code = h.msg(adj, "r", "hold", orch.name, "pause")
	if code != 0 || out != "HOLD "+orch.name+" by "+adj.name+"\n" {
		t.Fatalf("hold: %d %q", code, out)
	}
	out, _, code = h.msg(adj, "r", "unhold", orch.name)
	if code != 0 || out != "UNHOLD "+orch.name+" by "+adj.name+"; no hold remains\n" {
		t.Fatalf("unhold: %d %q", code, out)
	}
	out, _, code = h.msg(adj, "r", "authority")
	if code != 3 || !strings.HasPrefix(out, "HUMAN ") {
		t.Fatalf("authority: %d %q", code, out)
	}
	_, errOut, code = h.msg(adj, "r", "send", "nobody", body, "brief")
	if code != 2 || !strings.HasPrefix(errOut, "herdsman message send: ") {
		t.Fatalf("refused send: %d %q", code, errOut)
	}
	_, _, code = h.msg(adj, "r", "frobnicate")
	if code != 2 {
		t.Fatalf("unknown action: %d", code)
	}
	out, _, code = h.msg(adj, "r", "--json", "send", "nobody", body, "brief")
	if code != 2 || !strings.HasPrefix(out, `{"error":{"code":"bad_request"`) {
		t.Fatalf("json error: %d %q", code, out)
	}
}

func TestMessageEnrollCLI(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/v1/routes", CreateRequest{Repo: "repo", Route: "r"}, nil)
	h.fx.SetAgent("p1", herdrtest.Agent("p1", "s1"))
	p := &party{name: "", pane: "p1"}
	out, errOut, code := h.msg(p, "r", "enroll", "adj-1", "adjutant")
	if code != 0 || !regexp.MustCompile(`^pending registry [0-9a-f]{64}\n$`).MatchString(out) {
		t.Fatalf("enroll: %d %q %q", code, out, errOut)
	}
}

func TestStatusViaDaemon(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "x")
	out, _, code := h.msg(adj, "r", "status")
	if code != 0 || !strings.HasPrefix(out, "daemon=up version="+testVersion) || !strings.Contains(out, "box "+orch.name+" new 0 ready 1") {
		t.Fatalf("%d %s", code, out)
	}
	out, _, code = h.msg(adj, "r", "--json", "status")
	var st map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st["daemon"].(map[string]any)["state"] != "up" || st["epoch"] == "" {
		t.Fatalf("%d %s", code, out)
	}
}

func TestStatusFilesOnly(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	adj.send(t, "r", orch.name, "x")
	h.stop()
	out, _, code := h.msg(adj, "r", "status")
	if code != 1 || !strings.HasPrefix(out, "daemon=down heartbeat_age=") || !strings.Contains(out, "box "+orch.name+" new 0 ready 1") {
		t.Fatalf("%d %s", code, out)
	}
	if strings.Contains(out, "x\n") {
		t.Fatal("status printed a body")
	}
	out, _, _ = h.msg(adj, "r", "--json", "status")
	var st StatusReport
	if json.Unmarshal([]byte(out), &st) != nil || st.LastSeq == 0 {
		t.Fatalf("last seq from the journal tail: %s", out)
	}
}

func TestStatusSkipsBadBox(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	adj.send(t, "r", orch.name, "x")
	h.stop()
	_ = os.Symlink(filepath.Join(dir, orch.name), filepath.Join(dir, "linked"))
	out, _, code := h.msg(adj, "r", "status")
	if code != 1 || !strings.Contains(out, "box "+orch.name) || strings.Contains(out, "box linked") {
		t.Fatalf("%d %s", code, out)
	}
	st := FileStatus("r", dir)
	found := false
	for _, f := range st.Flags {
		found = found || f == "linked-mailbox:linked"
	}
	if !found {
		t.Fatalf("a linked mailbox is not reported: %v", st.Flags)
	}
}

// waitAsync runs wait in the background.
func (h *harness) waitAsync(p *party, route string, args ...string) chan [3]string {
	ch := make(chan [3]string, 1)
	go func() {
		env := map[string]string{"HERDSMAN_PRIVATE_HOME": h.ph, "HERDSMAN_ROUTE": route, "HERDSMAN_NAME": p.name, "HERDR_PANE_ID": p.pane, "HEALTH_GRACE": "1"}
		var out, errb bytes.Buffer
		e := &MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: testVersion}
		if len(args) > 0 && args[0] == "--json" {
			e.JSON, args = true, args[1:]
		}
		code := RunMessage(e, append([]string{"wait"}, args...))
		ch <- [3]string{out.String(), errb.String(), string(rune('0' + code))}
	}()
	return ch
}

func cursorOf(t testing.TB, out string) string {
	t.Helper()
	m := regexp.MustCompile(`cursor ([0-9a-f]{32}-[0-9]+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no cursor in %q", out)
	}
	return m[1]
}

func TestWaitUnreadFirst(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	m := adj.send(t, "r", orch.name, "x")
	res := <-h.waitAsync(orch, "r")
	if res[2] != "0" || !strings.HasPrefix(res[0], "MESSAGE "+orch.name+" "+m.ID+" cursor ") {
		t.Fatalf("%q", res)
	}
}

func TestWaitOneEvent(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	ch := h.waitAsync(orch, "r", "--deadline", "20")
	time.Sleep(200 * time.Millisecond)
	m := adj.send(t, "r", orch.name, "x")
	res := <-ch
	if res[2] != "0" || !strings.HasPrefix(res[0], "MESSAGE "+orch.name+" "+m.ID+" from "+adj.name+" kind brief cursor ") ||
		!strings.HasSuffix(res[0], ": run herdsman message read\n") {
		t.Fatalf("%q", res)
	}
	// The next wait from that cursor sees the next event only.
	c := cursorOf(t, res[0])
	ch = h.waitAsync(orch, "r", "--after", c, "--deadline", "20")
	m2 := adj.send(t, "r", orch.name, "y")
	res = <-ch
	if !strings.Contains(res[0], m2.ID) {
		t.Fatalf("%q", res)
	}
}

func TestWaitNoLostWake(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	c := mustCursor(t, h, orch)
	m := adj.send(t, "r", orch.name, "x")
	// A waiter killed before its harness saw the line stored nothing: the
	// next wait with the old cursor gets the same event again.
	for range 2 {
		res := <-h.waitAsync(orch, "r", "--after", c, "--deadline", "5")
		if !strings.Contains(res[0], m.ID) {
			t.Fatalf("%q", res)
		}
	}
}

// mustCursor is the party's current snapshot cursor.
func mustCursor(t testing.TB, h *harness, p *party) string {
	t.Helper()
	ev, err := p.c.Events(t.Context(), "/v1/routes/r/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	return waitFrame(t, ev, "snapshot").ID
}

func TestWaitJSON(t *testing.T) {
	h := newHarness(t)
	adj, orch, _ := h.route("r")
	c := mustCursor(t, h, orch)
	m := adj.send(t, "r", orch.name, "x")
	res := <-h.waitAsync(orch, "r", "--json", "--after", c, "--deadline", "5")
	var got map[string]any
	if json.Unmarshal([]byte(res[0]), &got) != nil || got["type"] != "message" || got["id"] != m.ID || got["cursor"] == nil {
		t.Fatalf("%q", res)
	}
	res = <-h.waitAsync(orch, "r", "--json", "--after", cursorOf(t, "cursor "+got["cursor"].(string)), "--deadline", "1")
	if res[0] != `{"type":"tick"}`+"\n" || res[2] != "0" {
		t.Fatalf("tick %q", res)
	}
}

func TestWaitDeadlineOption(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	start := time.Now()
	res := <-h.waitAsync(orch, "r", "--deadline", "1s")
	if res[0] != "TICK message-wait deadline\n" || res[2] != "0" || time.Since(start) > 10*time.Second {
		t.Fatalf("%q", res)
	}
	res = <-h.waitAsync(orch, "r", "--deadline", "soon")
	if res[2] != "2" || !strings.Contains(res[1], `DEADLINE: not a duration`) {
		t.Fatalf("%q", res)
	}
	if d, err := parseDuration("0", time.Second, 24*time.Hour); err != nil || d != time.Second {
		t.Fatalf("clamp low: %v %v", d, err)
	}
	if d, err := parseDuration("9d", time.Second, 24*time.Hour); err != nil || d != 24*time.Hour {
		t.Fatalf("clamp high: %v %v", d, err)
	}
	for _, v := range []string{"", "1x", "-1", "1.5", "s", "9999999999"} {
		if _, err := parseDuration(v, time.Second, time.Hour); err == nil {
			t.Fatalf("%q accepted", v)
		}
	}
}

func TestWaitTickDefault270s(t *testing.T) {
	if DefaultWaitDeadline != 270*time.Second {
		t.Fatal(DefaultWaitDeadline)
	}
	for _, s := range []string{"270", "270s", "4m"} {
		if d, err := parseDuration(s, time.Second, 24*time.Hour); err != nil || (s != "4m" && d != DefaultWaitDeadline) {
			t.Fatalf("%s: %v %v", s, d, err)
		}
	}
}

func TestWaitSettings(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	env := map[string]string{"HERDSMAN_PRIVATE_HOME": h.ph, "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": orch.name, "HERDR_PANE_ID": orch.pane, "HEALTH_GRACE": "never"}
	var out, errb bytes.Buffer
	code := RunMessage(&MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: testVersion}, []string{"wait"})
	if code != 2 || !strings.Contains(errb.String(), "HEALTH_GRACE") {
		t.Fatalf("%d %q", code, errb.String())
	}
	env["HEALTH_GRACE"] = ""
	env["DEADLINE"] = "x"
	code = RunMessage(&MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: testVersion}, []string{"wait"})
	if code != 2 {
		t.Fatalf("DEADLINE: %d", code)
	}
}

func TestWaitDaemonDownHealth(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	h.stop()
	start := time.Now()
	res := <-h.waitAsync(orch, "r", "--deadline", "30")
	if res[0] != "HEALTH daemon-unreachable\n" || res[2] != "1" || time.Since(start) > 15*time.Second {
		t.Fatalf("%q", res)
	}
}

func TestWaitResync(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	c := mustCursor(t, h, orch)
	h.do("POST", "/v1/routes/r/reset-events", ResetRequest{ConfirmRoute: "r"}, nil)
	res := <-h.waitAsync(orch, "r", "--after", c, "--deadline", "5")
	if res[2] != "1" || !strings.HasPrefix(res[0], "HEALTH resync: epoch_changed cursor ") {
		t.Fatalf("%q", res)
	}
}

func TestWaitVersionMismatch(t *testing.T) {
	h := newHarness(t)
	_, orch, _ := h.route("r")
	env := map[string]string{"HERDSMAN_PRIVATE_HOME": h.ph, "HERDSMAN_ROUTE": "r", "HERDSMAN_NAME": orch.name, "HERDR_PANE_ID": orch.pane}
	var out, errb bytes.Buffer
	code := RunMessage(&MessageEnv{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb, Version: "9.0.0"}, []string{"wait"})
	if code != 1 || !strings.HasPrefix(out.String(), "HEALTH version-mismatch: The herdsman daemon (1.0.0) is older") {
		t.Fatalf("%d %q", code, out.String())
	}
}

func TestHelpListsExitCodes(t *testing.T) {
	for _, h := range []string{MessageHelp, DaemonHelp} {
		if !strings.Contains(h, "exit codes:") || !strings.Contains(h, "75") {
			t.Fatalf("help without exit codes:\n%s", h)
		}
	}
	var out bytes.Buffer
	if RunDaemon(&AdminEnv{Stdout: &out, Stderr: &out}, []string{"--help"}) != 0 || !strings.Contains(out.String(), "exit codes:") {
		t.Fatal(out.String())
	}
}

func TestFileModeRefusesDaemonInbox(t *testing.T) {
	h := newHarness(t)
	adj, orch, dir := h.route("r")
	body := writeFile(t, h.dir, "b.md", "x")
	env := map[string]string{"HERDSMAN_INBOX": dir, "HERDSMAN_NAME": adj.name, "HERDSMAN_PREFLIGHT": filepath.Join(dir, "setup.json")}
	var out, errb bytes.Buffer
	e := &message.Env{Getenv: func(k string) string { return env[k] }, Stdout: &out, Stderr: &errb}
	code := e.Main([]string{"send", orch.name, body, "brief"})
	if code != 2 || !strings.Contains(errb.String(), "inbox owned by the herdsman daemon") {
		t.Fatalf("%d %q", code, errb.String())
	}
}

func TestDaemonAdminCLI(t *testing.T) {
	h := newHarness(t)
	run := func(attended bool, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		a := &AdminEnv{Getenv: func(k string) string {
			if k == "HERDSMAN_PRIVATE_HOME" {
				return h.ph
			}
			return ""
		}, Stdin: strings.NewReader("y\ny\ny\ny\ny\ny\n"), Stdout: &out, Stderr: &errb, Version: testVersion, Attended: attended,
			Supervisor: &Supervisor{Home: h.home, PrivateHome: h.ph, Version: testVersion}}
		code := RunDaemon(a, args)
		return out.String(), errb.String(), code
	}
	out, errOut, code := run(false, "route", "create", "--repo", "repo", "--route", "r")
	if code != 0 || out != "Inbox: "+filepath.Join(h.ph, "repo-r")+" (per route)\n" {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	// A route without its setup proof is held: the daemon is degraded.
	out, _, code = run(false, "status")
	if code != 1 || !strings.HasPrefix(out, "daemon degraded version "+testVersion) || !strings.Contains(out, "route r held setup-proof-stale") {
		t.Fatalf("status %d %q", code, out)
	}
	_, errOut, code = run(false, "setup", "r", "--rule", h.rules)
	if code != 2 || !strings.Contains(errOut, "attended terminal") {
		t.Fatalf("setup without a TTY: %d %q", code, errOut)
	}
	out, errOut, code = run(true, "setup", "r", "--rule", h.rules)
	if code != 0 || !strings.Contains(out, "setup recorded; proof ") {
		t.Fatalf("setup: %d %q %q", code, out, errOut)
	}
	out, _, code = run(false, "status")
	if code != 0 || !strings.HasPrefix(out, "daemon ok version "+testVersion) {
		t.Fatalf("status after setup %d %q", code, out)
	}
	_, errOut, code = run(false, "pair", "approve", "r", strings.Repeat("0", 64))
	if code != 2 || !strings.Contains(errOut, "attended terminal or --interaction") {
		t.Fatalf("approve without a TTY: %d %q", code, errOut)
	}
	_, _, code = run(false, "pair", "approve", "r", strings.Repeat("0", 64), "--interaction", "ask-1")
	if code != 2 {
		t.Fatalf("approve a wrong checksum: %d", code)
	}
	out, _, code = run(false, "--json", "route", "list")
	if code != 0 || !strings.Contains(out, `"route":"r"`) {
		t.Fatalf("list %d %q", code, out)
	}
	out, _, code = run(false, "route", "reset-events", "r")
	if code != 0 || !strings.HasPrefix(out, "new epoch ") {
		t.Fatalf("reset %d %q", code, out)
	}
	out, _, code = run(false, "route", "remove", "r")
	if code != 0 || out != "removed r; inbox kept\n" {
		t.Fatalf("remove %d %q", code, out)
	}
	_, _, code = run(false, "frob")
	if code != 2 {
		t.Fatalf("unknown %d", code)
	}
	h.stop()
	out, _, code = run(false, "status")
	if code != 1 || out != "daemon down\n" {
		t.Fatalf("down %d %q", code, out)
	}
}

func TestDaemonRunRefusesSecond(t *testing.T) {
	h := newHarness(t)
	var out, errb bytes.Buffer
	a := &AdminEnv{Getenv: func(k string) string { return map[string]string{"HERDSMAN_PRIVATE_HOME": h.ph}[k] }, Stdout: &out, Stderr: &errb,
		Version: testVersion, JSON: true, Supervisor: &Supervisor{Home: h.home, PrivateHome: h.ph}}
	if code := a.run(h.ph, h.home); code != 2 || !strings.Contains(out.String(), `"code":"daemon_running"`) {
		t.Fatalf("%d %q %q", code, out.String(), errb.String())
	}
}
