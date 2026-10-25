package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

// routeFixture is the web fixture with a workspace in its snapshot, herdr
// answers for the route calls, the real catalog output, a skill folder with
// a checkpointer script and a home folder of its own.
type routeFixture struct {
	*fixture
	folder, script, home string
	nextTab, nextPane    atomic.Int32
	failAt               string // a method that answers an error
	failCode             string
}

func newRouteFixture(t *testing.T) *routeFixture {
	t.Helper()
	f := &routeFixture{fixture: newFixture(t)}
	f.home = t.TempDir()
	t.Setenv("HOME", f.home)
	f.folder = filepath.Join(t.TempDir(), "My_Repo.git")
	if err := os.MkdirAll(f.folder, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(t.TempDir(), "herdsman")
	if err := os.MkdirAll(filepath.Join(skill, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.script = filepath.Join(skill, "scripts", "checkpointer.sh")
	if err := os.WriteFile(f.script, []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.script = realPath(f.script)
	f.srv.cfg.SkillDir = skill
	f.srv.catalog = newCatalogSource(fakeRunner(realOutputs(t), nil, nil))

	prev := f.herdr
	snapshot := func(map[string]any) (any, string) {
		return map[string]any{"snapshot": map[string]any{
			"version": "0.9.4", "protocol": 22, "focused_tab_id": f.focused.Load(),
			"workspaces": []map[string]any{{"workspace_id": "w1", "label": "main", "focused": true}},
			"panes":      []map[string]any{{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "cwd": f.folder}},
			"agents":     []map[string]any{{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "name": "im-auth"}},
		}}, ""
	}
	prev.Handle("session.snapshot", snapshot)
	answer := func(method string, ok func(map[string]any) any) {
		prev.Handle(method, func(p map[string]any) (any, string) {
			f.focused.Store("w1:t9") // every call moves herdr's focus
			if f.failAt == method {
				return nil, f.failCode
			}
			return ok(p), ""
		})
	}
	answer("tab.create", func(map[string]any) any {
		n := f.nextTab.Add(1)
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": fmt.Sprintf("w1:t%d", 10+n)}, "root_pane": map[string]any{"pane_id": fmt.Sprintf("w1:p%d", 10+n)}}
	})
	answer("agent.start", func(p map[string]any) any {
		return map[string]any{"type": "agent_started", "agent": map[string]any{"pane_id": p["pane_id"], "interactive_ready": true}, "argv": []string{}}
	})
	answer("pane.split", func(map[string]any) any {
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": fmt.Sprintf("w1:p%d", 20+f.nextPane.Add(1))}}
	})
	for _, m := range []string{"agent.prompt", "pane.rename", "pane.send_text", "pane.send_keys"} {
		answer(m, func(map[string]any) any { return map[string]any{"type": "ok"} })
	}
	f.hub.refresh(context.Background())
	return f
}

func (f *routeFixture) orchestrator() RouteRequest {
	return RouteRequest{
		Role: "orchestrator", WorkspaceID: "w1", Folder: f.folder, Kind: "claude", Model: "fable", Effort: "medium",
		Name: "orch-1", Request: `fix "auth" \ now`, Checkpointer: RouteCheckpointer{On: true, Minutes: 15, Limit: 250000},
	}
}

func (f *routeFixture) adjutant() RouteRequest {
	r := f.orchestrator()
	r.Role, r.Name, r.Kind, r.Model, r.Effort = "adjutant", "adj-1", "claude", "opus", "high"
	r.OrchName, r.OrchKind, r.OrchModel, r.OrchEffort = "orch-1", "codex", "gpt-5.6-luna", "low"
	r.RouteName, r.Request = "route-2026-10-25", "ship it"
	return r
}

func (f *routeFixture) post(t *testing.T, req any) (int, RouteResult) {
	t.Helper()
	b, _ := json.Marshal(req)
	res := f.request(t, http.MethodPost, "/api/routes", string(b), f.authed)
	defer res.Body.Close()
	var out RouteResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, out
}

// mutations are the recorded calls minus reads and the focus put-back.
func mutations(s *herdrtest.Server) []herdrtest.Request {
	var out []herdrtest.Request
	for _, r := range s.Requests() {
		switch r.Method {
		case "session.snapshot", "ping", "events.subscribe", "pane.read", "tab.focus":
			continue
		}
		out = append(out, r)
	}
	return out
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRouteOrchestratorCheckpointerOn(t *testing.T) {
	f := newRouteFixture(t)
	code, res := f.post(t, f.orchestrator())
	if code != 200 || !res.OK {
		t.Fatalf("status %d: %+v", code, res)
	}
	folder := realPath(f.folder)
	want := []string{
		`tab.create {"cwd":"` + folder + `","focus":false,"label":"my-repo-git-orchestrator","workspace_id":"w1"}`,
		`agent.start {"args":["--model","fable","--effort","medium"],"kind":"claude","name":"orch-1","pane_id":"w1:p11","timeout_ms":90000}`,
		`agent.prompt {"target":"w1:p11","text":"/herdsman run \"fix \\\"auth\\\" \\\\ now\""}`,
		`pane.split {"cwd":"` + folder + `","direction":"right","focus":false,"target_pane_id":"w1:p11"}`,
		`pane.rename {"label":"checkpointer","pane_id":"w1:p21"}`,
		`pane.send_text {"pane_id":"w1:p21","text":"TARGET=w1:p11 INTERVAL=900 LIMIT=250000 /bin/bash '` + f.script + `'"}`,
		`pane.send_keys {"keys":["enter"],"pane_id":"w1:p21"}`,
	}
	var got []string
	for _, r := range mutations(f.herdr) {
		got = append(got, r.Method+" "+jsonOf(r.Params))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.OrchestratorPane != "w1:p11" || res.CheckpointerPane != "w1:p21" || !slices.Equal(res.Tabs, []string{"w1:t11"}) || len(res.Steps) != 7 {
		t.Fatalf("result = %+v", res)
	}
	// The user's focused tab comes back once at the end.
	if f.focused.Load() != "w1:t1" {
		t.Fatalf("focus = %v", f.focused.Load())
	}
	// The log names the request's length, never its text.
	for _, e := range f.hub.entries {
		if strings.Contains(e.Text, "auth") {
			t.Fatalf("the request text reached the log: %q", e.Text)
		}
	}
	if !slices.ContainsFunc(f.hub.entries, func(e Entry) bool {
		return strings.Contains(e.Text, fmt.Sprintf("request of %d characters", len(`fix "auth" \ now`)))
	}) {
		t.Fatal("no log line with the request length")
	}
}

func TestRouteCheckpointerOff(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Checkpointer.On, req.Request = false, ""
	code, res := f.post(t, req)
	if code != 200 {
		t.Fatalf("status %d: %+v", code, res)
	}
	var methods []string
	for _, r := range mutations(f.herdr) {
		methods = append(methods, r.Method)
	}
	if !slices.Equal(methods, []string{"tab.create", "agent.start", "agent.prompt"}) {
		t.Fatalf("methods = %v", methods)
	}
	if text := mutations(f.herdr)[2].Params["text"]; text != "/herdsman run" {
		t.Fatalf("empty request text = %q", text)
	}
}

func TestRouteCodexUsesItsSkillPrefix(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Kind, req.Model, req.Effort = "codex", "gpt-5.6-luna", "low"
	if code, res := f.post(t, req); code != 200 {
		t.Fatalf("status %d: %+v", code, res)
	}
	m := mutations(f.herdr)
	if a := jsonOf(m[1].Params["args"]); a != `["--model","gpt-5.6-luna","-c","model_reasoning_effort=low"]` {
		t.Fatalf("args = %s", a)
	}
	if text := m[2].Params["text"]; !strings.HasPrefix(text.(string), "$herdsman run ") {
		t.Fatalf("text = %q", text)
	}
}

func TestRouteAdjutant(t *testing.T) {
	f := newRouteFixture(t)
	code, res := f.post(t, f.adjutant())
	if code != 200 || !res.OK {
		t.Fatalf("status %d: %+v", code, res)
	}
	inbox := filepath.Join(f.home, ".herdsman-private", "my-repo-git-route-2026-10-25")
	m := mutations(f.herdr)
	var methods []string
	for _, r := range m {
		methods = append(methods, r.Method)
	}
	want := []string{"tab.create", "agent.start", "tab.create", "agent.start", "agent.prompt", "pane.split", "pane.rename", "pane.send_text", "pane.send_keys"}
	if !slices.Equal(methods, want) {
		t.Fatalf("methods = %v", methods)
	}
	if m[0].Params["label"] != "my-repo-git-adjutant" || m[2].Params["label"] != "my-repo-git-orchestrator" {
		t.Fatalf("labels = %v, %v", m[0].Params["label"], m[2].Params["label"])
	}
	if m[1].Params["name"] != "adj-1" || m[1].Params["pane_id"] != "w1:p11" || jsonOf(m[1].Params["args"]) != `["--model","opus","--effort","high"]` {
		t.Fatalf("adjutant start = %v", m[1].Params)
	}
	if m[3].Params["name"] != "orch-1" || m[3].Params["kind"] != "codex" || m[3].Params["pane_id"] != "w1:p12" {
		t.Fatalf("orchestrator start = %v", m[3].Params)
	}
	if m[4].Params["target"] != "w1:p12" || m[4].Params["text"] != "$herdsman orchestrator --adjutant adj-1 --name orch-1 --inbox "+inbox+` "ship it"` {
		t.Fatalf("prompt = %v", m[4].Params)
	}
	// Nothing is typed or prompted into the adjutant's pane after its start.
	for _, r := range m[2:] {
		if r.Params["target"] == "adj-1" || r.Params["target"] == "w1:p11" || r.Params["pane_id"] == "w1:p11" || r.Params["target_pane_id"] == "w1:p11" {
			t.Fatalf("a call reached the adjutant pane: %s %v", r.Method, r.Params)
		}
	}
	// The checkpointer targets the orchestrator and carries the guard's settings.
	line := m[7].Params["text"]
	wantLine := "TARGET=w1:p12 HERDSMAN_ORCHESTRATOR=orch-1 HERDSMAN_INBOX=" + inbox + " HERDSMAN_PREFLIGHT=" + inbox + "/setup.json INTERVAL=900 LIMIT=250000 /bin/bash '" + f.script + "'"
	if line != wantLine || m[5].Params["target_pane_id"] != "w1:p12" {
		t.Fatalf("checkpointer line = %q\nwant %q", line, wantLine)
	}
	if res.AdjutantPane != "w1:p11" || res.OrchestratorPane != "w1:p12" || res.AttachCommand != "/herdsman adjutant --name adj-1 --orchestrator w1:p12 --inbox "+inbox {
		t.Fatalf("result = %+v", res)
	}
	// The app writes nothing into the inbox: it does not even create it.
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Fatalf("the inbox was created: %v", err)
	}
}

func TestRouteFailsAtEachStep(t *testing.T) {
	order := []string{"tab.create", "agent.start", "agent.prompt", "pane.split", "pane.rename", "pane.send_text", "pane.send_keys"}
	for k, method := range order {
		t.Run(method, func(t *testing.T) {
			f := newRouteFixture(t)
			f.failAt, f.failCode = method, "boom_"+method
			code, res := f.post(t, f.orchestrator())
			if code != http.StatusBadGateway || res.OK || res.Code != "boom_"+method {
				t.Fatalf("status %d: %+v", code, res)
			}
			if len(res.Steps) != len(order) {
				t.Fatalf("steps = %+v", res.Steps)
			}
			for i, s := range res.Steps {
				want := "ok"
				if i == k {
					want = "failed"
				} else if i > k {
					want = "skipped"
				}
				if s.Status != want || s.Method != order[i] {
					t.Fatalf("step %d = %+v, want %s", i, s, want)
				}
			}
			ms := mutations(f.herdr)
			if len(ms) != k+1 {
				t.Fatalf("calls = %d, want %d", len(ms), k+1)
			}
			for _, r := range ms {
				if r.Method == "pane.close" || r.Method == "tab.close" {
					t.Fatal("something was closed after a failure")
				}
			}
		})
	}
}

func TestRouteJobIsAtomic(t *testing.T) {
	f := newRouteFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	f.herdr.Handle("agent.start", func(p map[string]any) (any, string) {
		close(started)
		<-release
		return map[string]any{"type": "agent_started", "agent": map[string]any{"pane_id": p["pane_id"]}, "argv": []string{}}, ""
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := f.orchestrator()
		req.Checkpointer.On = false
		f.post(t, req)
	}()
	<-started
	acted := make(chan struct{})
	go func() {
		defer close(acted)
		f.queue.Do(context.Background(), Action{Type: "keys", Pane: "w1:p2", Keys: []string{"enter"}})
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	<-acted
	var methods []string
	for _, r := range mutations(f.herdr) {
		methods = append(methods, r.Method)
	}
	if !slices.Equal(methods, []string{"tab.create", "agent.start", "agent.prompt", "pane.send_keys"}) {
		t.Fatalf("an action ran inside the job: %v", methods)
	}
}

func TestRouteRefusals(t *testing.T) {
	f := newRouteFixture(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, field string
		edit        func(*RouteRequest)
	}{
		{"unknown role", "role", func(r *RouteRequest) { r.Role = "boss" }},
		{"unknown workspace", "workspace_id", func(r *RouteRequest) { r.WorkspaceID = "w9" }},
		{"relative folder", "folder", func(r *RouteRequest) { r.Folder = "repo" }},
		{"missing folder", "folder", func(r *RouteRequest) { r.Folder = "/nope/not/here" }},
		{"folder is a file", "folder", func(r *RouteRequest) { r.Folder = file }},
		{"bad name", "name", func(r *RouteRequest) { r.Name = "Orch 1" }},
		{"live name", "name", func(r *RouteRequest) { r.Name = "im-auth" }},
		{"model not in catalog", "model", func(r *RouteRequest) { r.Model = "fable 2" }},
		{"effort not in catalog", "model", func(r *RouteRequest) { r.Effort = "ultra" }},
		{"codex model not listed", "model", func(r *RouteRequest) { r.Kind, r.Model, r.Effort = "codex", "gpt-4o", "low" }},
		{"unknown kind", "model", func(r *RouteRequest) { r.Kind = "nope" }},
		{"interval too short", "minutes", func(r *RouteRequest) { r.Checkpointer.Minutes = 4 }},
		{"interval too long", "minutes", func(r *RouteRequest) { r.Checkpointer.Minutes = 241 }},
		{"limit too low", "limit", func(r *RouteRequest) { r.Checkpointer.Limit = 49999 }},
		{"limit too high", "limit", func(r *RouteRequest) { r.Checkpointer.Limit = 2000001 }},
		{"control character", "request", func(r *RouteRequest) { r.Request = "a\x1b[2Jb" }},
		{"request too long", "request", func(r *RouteRequest) { r.Request = strings.Repeat("x", maxRouteRequest+1) }},
		{"checkpointer for another kind", "checkpointer", func(r *RouteRequest) { r.Kind, r.Model, r.Effort = "pi", "", "" }},
		{"adjutant fields on an orchestrator route", "role", func(r *RouteRequest) { r.Inbox = "~/x" }},
	}
	adj := []struct {
		name, field string
		edit        func(*RouteRequest)
	}{
		{"bad route name", "route_name", func(r *RouteRequest) { r.RouteName = "Route 1" }},
		{"inbox outside home", "inbox", func(r *RouteRequest) { r.Inbox = "/tmp/inbox" }},
		{"inbox escapes home", "inbox", func(r *RouteRequest) { r.Inbox = "~/../x" }},
		{"inbox with a space", "inbox", func(r *RouteRequest) { r.Inbox = "~/my inbox" }},
		{"inbox is home", "inbox", func(r *RouteRequest) { r.Inbox = "~" }},
		{"same names", "orch_name", func(r *RouteRequest) { r.OrchName = "adj-1" }},
		{"bad orchestrator name", "orch_name", func(r *RouteRequest) { r.OrchName = "9x" }},
		{"orchestrator model not listed", "orch_model", func(r *RouteRequest) { r.OrchModel = "gpt-reserve" }},
		{"adjutant model not listed", "model", func(r *RouteRequest) { r.Model = "x y" }},
	}
	run := func(name, field string, req RouteRequest) {
		t.Run(name, func(t *testing.T) {
			before := len(mutations(f.herdr))
			code, res := f.post(t, req)
			if code != 400 || res.Code != "invalid_route" || res.Field != field || res.Error == "" {
				t.Fatalf("status %d: %+v", code, res)
			}
			if len(mutations(f.herdr)) != before {
				t.Fatal("a refused request reached herdr")
			}
		})
	}
	for _, c := range cases {
		r := f.orchestrator()
		c.edit(&r)
		run(c.name, c.field, r)
	}
	for _, c := range adj {
		r := f.adjutant()
		c.edit(&r)
		run("adjutant "+c.name, c.field, r)
	}
	t.Run("missing checkpointer script", func(t *testing.T) {
		f.srv.cfg.SkillDir = filepath.Join(t.TempDir(), "none")
		defer func() { f.srv.cfg.SkillDir = filepath.Dir(filepath.Dir(f.script)) }()
		code, res := f.post(t, f.orchestrator())
		if code != 400 || res.Field != "checkpointer" || !strings.Contains(res.Error, "--skill-dir") || len(mutations(f.herdr)) != 0 {
			t.Fatalf("status %d: %+v", code, res)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		res := f.request(t, http.MethodPost, "/api/routes", `{"role":"orchestrator","shell":"rm"}`, f.authed)
		if res.StatusCode != 400 {
			t.Fatalf("status %d", res.StatusCode)
		}
	})
	t.Run("unpaired", func(t *testing.T) {
		b, _ := json.Marshal(f.orchestrator())
		res := f.request(t, http.MethodPost, "/api/routes", string(b), nil)
		if res.StatusCode != 401 || len(mutations(f.herdr)) != 0 {
			t.Fatalf("status %d", res.StatusCode)
		}
	})
}

func TestPromptQuoting(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:              `"plain"`,
		`say "hi"`:           `"say \"hi\""`,
		`a\b`:                `"a\\b"`,
		"line 1\nline 2\tx":  "\"line 1\nline 2\tx\"",
		`end \`:              `"end \\"`,
		`\"`:                 `"\\\""`,
		"unicode ✓ stays as": "\"unicode ✓ stays as\"",
	} {
		if got := quoteArg(in); got != want {
			t.Errorf("quoteArg(%q) = %q, want %q", in, got, want)
		}
	}
	for _, ok := range []string{"", "a\nb\tc", "ünï"} {
		if err := checkRequestText(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\rb", "\x00", "\x7f", "a\u0085b", string([]byte{0xff})} {
		if err := checkRequestText(bad); err == nil {
			t.Errorf("%q: want a refusal", bad)
		}
	}
}

func TestProjectLabel(t *testing.T) {
	for in, want := range map[string]string{
		"/x/herdsman":                         "herdsman",
		"/x/My_Repo.git":                      "my-repo-git",
		"/x/--Weird  Name--":                  "weird-name",
		"/x/a-very-long-repository-name-here": "a-very-long-repository-n",
		"/x/abcdefghijklmnopqrstuvw-xyz":      "abcdefghijklmnopqrstuvw",
		"/x/___":                              "",
		"/x/日本":                               "",
	} {
		if got := projectLabel(in); got != want {
			t.Errorf("projectLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckpointerLineOnlyValidated(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Request = "x' ; rm -rf ~ ; echo '"
	req.Checkpointer.Minutes, req.Checkpointer.Limit = 5, 2000000
	if code, res := f.post(t, req); code != 200 {
		t.Fatalf("status %d: %+v", code, res)
	}
	for _, r := range mutations(f.herdr) {
		if r.Method == "pane.send_text" {
			line := r.Params["text"].(string)
			if line != "TARGET=w1:p11 INTERVAL=300 LIMIT=2000000 /bin/bash '"+f.script+"'" {
				t.Fatalf("line = %q", line)
			}
		}
	}
}

func TestRouteAnswerWithBadPaneID(t *testing.T) {
	f := newRouteFixture(t)
	f.herdr.Handle("tab.create", func(map[string]any) (any, string) {
		return map[string]any{"type": "tab_created", "tab": map[string]any{"tab_id": "w1:t1"}, "root_pane": map[string]any{"pane_id": "w1:p1; rm"}}, ""
	})
	code, res := f.post(t, f.orchestrator())
	if code != http.StatusBadGateway || res.Steps[0].Status != "failed" || res.Code != "herdr_error" {
		t.Fatalf("status %d: %+v", code, res)
	}
}

func TestCatalogEndpoint(t *testing.T) {
	f := newRouteFixture(t)
	res := f.request(t, http.MethodGet, "/api/catalog", "", f.authed)
	var c Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil || res.StatusCode != 200 {
		t.Fatalf("status %d: %v", res.StatusCode, err)
	}
	if c.Kinds[0].Kind != "claude" || c.Kinds[0].SkillInstalled || !c.CheckpointerOK || c.Home != f.home || len(c.Today) != 10 {
		t.Fatalf("catalog = %+v", c)
	}
	if err := os.MkdirAll(filepath.Join(f.home, ".claude", "skills", "herdsman"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".claude", "skills", "herdsman", "SKILL.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.srv.cfg.SkillDir = filepath.Join(f.home, "none")
	res = f.request(t, http.MethodGet, "/api/catalog", "", f.authed)
	c = Catalog{}
	_ = json.NewDecoder(res.Body).Decode(&c)
	if !c.Kinds[0].SkillInstalled || c.Kinds[1].SkillInstalled || c.CheckpointerOK || c.CheckpointerWhy == "" {
		t.Fatalf("catalog = %+v", c)
	}
	if res := f.request(t, http.MethodGet, "/api/catalog", "", nil); res.StatusCode != 401 {
		t.Fatalf("unpaired: %d", res.StatusCode)
	}
}

func TestInboxState(t *testing.T) {
	f := newRouteFixture(t)
	get := func(path string) (int, InboxState) {
		res := f.request(t, http.MethodGet, "/api/routes/inbox?path="+path, "", f.authed)
		var st InboxState
		_ = json.NewDecoder(res.Body).Decode(&st)
		return res.StatusCode, st
	}
	if code, st := get("~/.herdsman-private/r"); code != 200 || st.Exists || st.Private || st.Setup {
		t.Fatalf("missing inbox: %d %+v", code, st)
	}
	root := filepath.Join(f.home, ".herdsman-private", "r")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, st := get("~/.herdsman-private/r"); !st.Exists || st.Private {
		t.Fatalf("mode 755: %+v", st)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "setup.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := `{"orch-1":{"role":"orchestrator","identity":{"pane":"w1:p1","session":{"agent":"claude","kind":"id","source":"herdr","value":"s1"}}}}`
	if err := os.WriteFile(filepath.Join(root, "peers"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, st := get("~/.herdsman-private/r")
	if code != 200 || !st.Private || !st.Setup || !slices.Equal(st.Approved, []string{}) {
		t.Fatalf("set up inbox: %d %+v", code, st)
	}
	if !slices.Equal(st.Proposed, []string{"orch-1 (orchestrator)"}) {
		t.Fatalf("proposed = %v", st.Proposed)
	}
	if code, _ := get("/etc"); code != 400 {
		t.Fatalf("outside home: %d", code)
	}
	if err := os.WriteFile(filepath.Join(f.home, "afile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, st := get("~/afile"); st.Error == "" || !st.Exists {
		t.Fatalf("a file: %+v", st)
	}
}

func TestRoutePromptWaitsForTheNewAgent(t *testing.T) {
	f := newRouteFixture(t)
	promptReadyRetry = time.Millisecond
	defer func() { promptReadyRetry = 500 * time.Millisecond }()
	var tries atomic.Int32
	f.herdr.Handle("agent.prompt", func(map[string]any) (any, string) {
		if tries.Add(1) < 3 {
			return nil, "agent_not_ready"
		}
		return map[string]any{"type": "agent_prompted"}, ""
	})
	req := f.orchestrator()
	req.Checkpointer.On = false
	if code, res := f.post(t, req); code != 200 || !res.OK || tries.Load() != 3 {
		t.Fatalf("status %d, tries %d: %+v", code, tries.Load(), res)
	}
}

func TestRoutePromptGivesUpAfterTheWait(t *testing.T) {
	f := newRouteFixture(t)
	promptReadyRetry, promptReadyWait = time.Millisecond, 20*time.Millisecond
	defer func() { promptReadyRetry, promptReadyWait = 500*time.Millisecond, 20*time.Second }()
	f.herdr.Handle("agent.prompt", func(map[string]any) (any, string) { return nil, "agent_not_ready" })
	code, res := f.post(t, f.orchestrator())
	if code != http.StatusBadGateway || res.Code != "agent_not_ready" || res.Steps[2].Status != "failed" {
		t.Fatalf("status %d: %+v", code, res)
	}
}

func TestDoJobTurnsAPanicIntoAResult(t *testing.T) {
	f := newFixture(t)
	r := f.queue.DoJob(context.Background(), func(context.Context) Result { panic("boom") })
	if r.OK || r.Code != "internal_error" || !strings.Contains(r.Error, "boom") {
		t.Fatalf("result = %+v", r)
	}
	// The queue keeps running after it.
	if r := f.queue.DoJob(context.Background(), func(context.Context) Result { return Result{OK: true} }); !r.OK {
		t.Fatalf("result = %+v", r)
	}
}
