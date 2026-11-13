package hc_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dial(t *testing.T, core *hctest.Core) *hc.Client {
	t.Helper()
	c, err := hc.Dial(testCtx(t), core.Socket, hc.Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func run(t *testing.T, c *hc.Client, labels map[string]string) hc.Terminal {
	t.Helper()
	term, err := c.Run(testCtx(t), hc.RunRequest{Argv: []string{"sh"}, Labels: labels})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return term
}

func lastParams(t *testing.T, core *hctest.Core, method string) map[string]any {
	t.Helper()
	reqs := core.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			return reqs[i].Params
		}
	}
	t.Fatalf("no %s request", method)
	return nil
}

// scenario exercises one listed method against a connected client.
type scenario struct {
	method string
	run    func(t *testing.T, core *hctest.Core)
}

var scenarios = []scenario{
	{hc.MethodHello, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		h := c.Hello()
		if h.Generation != hc.Generation || h.Core.ID != core.ID || h.Version != hctest.Version || h.Framed {
			t.Fatalf("hello = %+v", h)
		}
		if p := lastParams(t, core, hc.MethodHello); p["generation"] != float64(hc.Generation) {
			t.Fatalf("hello params = %v, want generation %d", p, hc.Generation)
		}
	}},
	{hc.MethodServerStatus, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		run(t, c, nil)
		st, err := c.ServerStatus(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		if st.Core.ID != core.ID || len(st.Holders) != 1 || st.Holders[0].PID == 0 || st.Live != 1 {
			t.Fatalf("status = %+v", st)
		}
	}},
	{hc.MethodRun, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		term, err := c.Run(testCtx(t), hc.RunRequest{
			Argv: []string{"claude", "--resume", "x"}, Cwd: "/repo", Name: "impl-1", Keep: true,
			Labels: map[string]string{"herdsman.owner": "o1"}, Size: &hc.Size{Cols: 100, Rows: 40},
		})
		if err != nil {
			t.Fatal(err)
		}
		if term.Address == "" || !term.Live() || term.Name != "impl-1" || term.Labels["herdsman.owner"] != "o1" || term.PID == nil || term.Cwd != "/repo" {
			t.Fatalf("run = %+v", term)
		}
		p := lastParams(t, core, hc.MethodRun)
		if p["no_parent"] != true || p["no_notify"] != true {
			t.Fatalf("run must set no_parent and no_notify: %v", p)
		}
		for _, k := range []string{"on", "wait", "line", "parent"} {
			if _, ok := p[k]; ok {
				t.Fatalf("run sent %q: %v", k, p)
			}
		}
		if _, err := c.Run(testCtx(t), hc.RunRequest{Argv: []string{"x"}, Name: "impl-1"}); !hc.IsKind(err, hc.KindNameTaken) {
			t.Fatalf("second run with the name: %v", err)
		}
		if _, err := c.Run(testCtx(t), hc.RunRequest{}); err == nil {
			t.Fatal("run with no argv succeeded")
		}
	}},
	{hc.MethodList, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, map[string]string{"herdsman.route": "r1"})
		run(t, c, map[string]string{"herdsman.route": "r2"})
		if _, err := c.Kill(testCtx(t), run(t, c, map[string]string{"herdsman.route": "r1"}).Address); err != nil {
			t.Fatal(err)
		}
		got, err := c.List(testCtx(t), map[string]string{"herdsman.route": "r1"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Terminals) != 1 || got.Terminals[0].Address != a.Address || got.HiddenFinished != 1 {
			t.Fatalf("list = %+v", got)
		}
		all, err := c.List(testCtx(t), nil, true)
		if err != nil || len(all.Terminals) != 3 {
			t.Fatalf("list all = %+v, %v", all, err)
		}
	}},
	{hc.MethodGet, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, map[string]string{"k": "v"})
		got, err := c.Get(testCtx(t), a.Address)
		if err != nil || got.Address != a.Address || got.Labels["k"] != "v" {
			t.Fatalf("get = %+v, %v", got, err)
		}
		_, err = c.Get(testCtx(t), "t999")
		var e *hc.Error
		if !errors.As(err, &e) || e.Kind != hc.KindNotFound || e.Method != hc.MethodGet || e.Hint == "" || e.Code != -32000 {
			t.Fatalf("get t999: %#v", err)
		}
	}},
	{hc.MethodRead, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, nil)
		got, err := c.Read(testCtx(t), a.Address, 50)
		if err != nil || got.Text != "screen of "+a.Address || !got.Complete || got.How != "screen" {
			t.Fatalf("read = %+v, %v", got, err)
		}
		if p := lastParams(t, core, hc.MethodRead); p["lines"] != float64(50) {
			t.Fatalf("read params = %v", p)
		}
	}},
	{hc.MethodSend, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, nil)
		if _, err := c.Send(testCtx(t), a.Address, "hi", "idle"); !hc.IsKind(err, hc.KindStatusMismatch) {
			t.Fatalf("send to a silent program: %v", err)
		}
		core.SetStatus(a.Address, "idle")
		got, err := c.Send(testCtx(t), a.Address, "hi", "idle")
		if err != nil || got.Turn != 1 || got.Status != "idle" || got.Started == nil || !*got.Started {
			t.Fatalf("send = %+v, %v", got, err)
		}
		if p := lastParams(t, core, hc.MethodSend); p["if"] != "idle" || p["force"] != false || p["text"] != "hi" {
			t.Fatalf("send params = %v", p)
		}
		core.SetStatus(a.Address, "blocked")
		if _, err := c.Send(testCtx(t), a.Address, "hi", "idle"); !hc.IsKind(err, hc.KindBlocked) {
			t.Fatalf("send to a blocked program: %v", err)
		}
		before := core.Calls(hc.MethodSend)
		if _, err := c.Send(testCtx(t), a.Address, "hi", ""); err == nil {
			t.Fatal("send without if succeeded")
		}
		if core.Calls(hc.MethodSend) != before {
			t.Fatal("a send without if reached core")
		}
	}},
	{hc.MethodInterrupt, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, nil)
		got, err := c.Interrupt(testCtx(t), a.Address, 2)
		if err != nil || got.Key != "ctrl+c" || got.Outcome != "unknown" {
			t.Fatalf("interrupt = %+v, %v", got, err)
		}
		if p := lastParams(t, core, hc.MethodInterrupt); p["timeout"] != float64(2) {
			t.Fatalf("interrupt params = %v", p)
		}
		core.SetStatus(a.Address, "working")
		got, err = c.Interrupt(testCtx(t), a.Address, 0)
		if err != nil || got.Key != "esc" || got.Outcome != "stopped" {
			t.Fatalf("interrupt = %+v, %v", got, err)
		}
		if _, ok := lastParams(t, core, hc.MethodInterrupt)["timeout"]; ok {
			t.Fatal("a zero timeout was sent")
		}
	}},
	{hc.MethodKill, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, nil)
		got, err := c.Kill(testCtx(t), a.Address)
		if err != nil || got.Live() || got.Reason != "killed" || got.FinishedMS == nil {
			t.Fatalf("kill = %+v, %v", got, err)
		}
	}},
	{hc.MethodLabel, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, map[string]string{"a": "1", "b": "2"})
		got, err := c.Label(testCtx(t), a.Address, map[string]string{"a": "", "c": "3"})
		if err != nil || got.Labels["a"] != "" || got.Labels["b"] != "2" || got.Labels["c"] != "3" {
			t.Fatalf("label = %+v, %v", got, err)
		}
		if _, err := c.Label(testCtx(t), a.Address, nil); err == nil {
			t.Fatal("label with no labels succeeded")
		}
	}},
	{hc.MethodKeep, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, nil)
		got, err := c.Keep(testCtx(t), a.Address, true)
		if err != nil || !got.Keep {
			t.Fatalf("keep = %+v, %v", got, err)
		}
		if p := lastParams(t, core, hc.MethodKeep); p["keep"] != true {
			t.Fatalf("keep params = %v", p)
		}
		if _, err := c.Kill(testCtx(t), a.Address); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Keep(testCtx(t), a.Address, true); !hc.IsKind(err, hc.KindInvalidParams) {
			t.Fatalf("keep of a finished terminal: %v", err)
		}
	}},
	{hc.MethodExec, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		got, err := c.Exec(testCtx(t), hc.ExecRequest{Argv: []string{"echo", "x"}, Input: []byte("in"), TimeoutSeconds: 5})
		if err != nil || string(got.Stdout) != "x\n" || got.Code == nil || *got.Code != 0 || got.TimedOut {
			t.Fatalf("exec = %+v, %v", got, err)
		}
		if p := lastParams(t, core, hc.MethodExec); p["input"] != "aW4=" || p["timeout"] != float64(5) {
			t.Fatalf("exec params = %v", p)
		}
		core.SetExec("git", hctest.ExecAnswer{Code: 128, Stderr: "fatal: not a repository\n"})
		got, err = c.Exec(testCtx(t), hc.ExecRequest{Argv: []string{"git", "status"}})
		if err != nil || *got.Code != 128 || string(got.Stderr) != "fatal: not a repository\n" {
			t.Fatalf("exec git = %+v, %v", got, err)
		}
		if _, err := c.Exec(testCtx(t), hc.ExecRequest{Argv: []string{"nope"}}); !hc.IsKind(err, hc.KindSpawnFailed) {
			t.Fatalf("exec nope: %v", err)
		}
	}},
	{hc.MethodSubscribe, func(t *testing.T, core *hctest.Core) {
		c := dial(t, core)
		a := run(t, c, map[string]string{"herdsman.route": "r1"})
		s, err := hc.Subscribe(testCtx(t), core.Socket, hc.Filter{Labels: map[string]string{"herdsman.route": "r1"}}, hc.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		snap := s.Snapshot()
		if snap.Kind != hc.UpdateSnapshot || len(snap.Terminals) != 1 || snap.Terminals[0].Address != a.Address || snap.Seq != core.Seq() {
			t.Fatalf("snapshot = %+v", snap)
		}
		b := run(t, c, map[string]string{"herdsman.route": "r1"})
		u, err := s.Next(testCtx(t))
		if err != nil || u.Kind != hc.UpdateEvent || u.Event.Type != hc.EventCreated || u.Event.Terminal != b.Address || len(u.Terminals) != 2 {
			t.Fatalf("next = %+v, %v", u, err)
		}
		if p := lastParams(t, core, hc.MethodSubscribe); p["labels"].(map[string]any)["herdsman.route"] != "r1" {
			t.Fatalf("subscribe params = %v", p)
		}
	}},
}

func TestEveryListedMethodHasAScenario(t *testing.T) {
	var covered []string
	for _, s := range scenarios {
		covered = append(covered, s.method)
	}
	for _, m := range hc.Methods {
		if !slices.Contains(covered, m) {
			t.Errorf("listed method %s has no scenario", m)
		}
	}
	if len(covered) != len(hc.Methods) {
		t.Errorf("%d scenarios for %d listed methods", len(covered), len(hc.Methods))
	}
}

func TestScenariosAtPinnedGeneration(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.method, func(t *testing.T) {
			s.run(t, hctest.Start(t))
		})
	}
}

// A core at the previous generation refuses the pinned hello, so every
// method fails by generation before it is sent.
func TestScenariosAtPreviousGeneration(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.method, func(t *testing.T) {
			core := hctest.Start(t, hctest.WithGeneration(hctest.Previous))
			var err error
			if s.method == hc.MethodSubscribe {
				_, err = hc.Subscribe(testCtx(t), core.Socket, hc.Filter{}, hc.Options{})
			} else {
				_, err = hc.Dial(testCtx(t), core.Socket, hc.Options{})
			}
			var g *hc.GenerationError
			if !errors.As(err, &g) || g.Want != hc.Generation {
				t.Fatalf("dial at generation %d: %v", hctest.Previous, err)
			}
			for _, r := range core.Requests() {
				if r.Method != hc.MethodHello {
					t.Fatalf("%s reached a core at the previous generation", r.Method)
				}
			}
		})
	}
}

func TestUnknownFieldsAreToleratedAndRequiredOnesAreNot(t *testing.T) {
	var term hc.Terminal
	full := `{"address":"t1","labels":{},"argv":["sh"],"cwd":"/","state":"live","created_ms":1,"screen":"exact","cols":1,"rows":1,"brand_new":{"x":[1]},"name":null,"status":null}`
	if err := term.UnmarshalJSON([]byte(full)); err != nil || term.Address != "t1" {
		t.Fatalf("full record: %+v, %v", term, err)
	}
	for _, field := range []string{"address", "labels", "argv", "cwd", "state", "created_ms", "screen", "cols", "rows"} {
		var m map[string]any
		_ = json.Unmarshal([]byte(full), &m)
		delete(m, field)
		b, _ := json.Marshal(m)
		var got hc.Terminal
		var mf *hc.MissingFieldError
		if err := got.UnmarshalJSON(b); !errors.As(err, &mf) || mf.Field != field {
			t.Errorf("without %s: %v", field, err)
		}
	}
	var h hc.HelloResult
	var mf *hc.MissingFieldError
	if err := h.UnmarshalJSON([]byte(`{"generation":1,"version":"x","core":{"id":"a","name":"b"},"framed":false,"features":[]}`)); !errors.As(err, &mf) || mf.Field != "methods" {
		t.Fatalf("hello without methods: %v", err)
	}
	if err := h.UnmarshalJSON([]byte(`{"generation":1,"version":"x","core":{"id":"a"},"framed":false,"methods":[],"features":[]}`)); !errors.As(err, &mf) || mf.Type != "core" {
		t.Fatalf("hello with a core without a name: %v", err)
	}
}
