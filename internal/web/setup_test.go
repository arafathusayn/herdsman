package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/choices"
)

// promptText is the text of the orchestrator's agent.prompt call.
func promptText(t *testing.T, f *routeFixture) string {
	t.Helper()
	for _, r := range mutations(f.herdr) {
		if r.Method == "agent.prompt" {
			var p struct {
				Text string `json:"text"`
			}
			b, _ := json.Marshal(r.Params)
			_ = json.Unmarshal(b, &p)
			return p.Text
		}
	}
	t.Fatal("no prompt")
	return ""
}

// A32: the chosen setup reaches the orchestrator as text rendered from
// RouteRequest.setup, with effective values: the checkpointer reads off
// when the route starts none.
func TestSetupReachesTheOrchestrator(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Request = "ship it"
	req.Checkpointer.On = false
	req.Setup = &RouteSetup{
		Recommended: true, Checkpointer: true, GitHubAccount: "user-b",
		Roles:   []choices.Pick{{Role: "orchestrator", Runner: "herdr", Harness: "claude", Model: "fable", Effort: "medium"}},
		Answers: []SetupAnswer{{Item: "Merge", Answer: "You merge; branches stay until the route closes."}},
	}
	if code, res := f.post(t, req); code != 200 || !res.OK {
		t.Fatalf("%d %+v", code, res)
	}
	want := "/herdsman run " + quoteArg("ship it\n\n"+
		"Setup (recommended):\n"+
		"- adjutant: off\n"+
		"- checkpointer: off\n"+
		"- GitHub account: user-b\n"+
		"- orchestrator: runner herdr, harness claude, model fable, effort medium\n"+
		"- Merge: You merge; branches stay until the route closes.")
	if got := promptText(t, f); got != want {
		t.Fatalf("prompt\n%s\nwant\n%s", got, want)
	}
}

func TestCustomSetupAnswers(t *testing.T) {
	s := RouteSetup{Answers: []SetupAnswer{{Item: "Integration", Answer: "one combined branch"}, {Item: "Merge", Answer: ""}}}
	got := s.text(RouteRequest{Role: "adjutant", Checkpointer: RouteCheckpointer{On: true}}, true)
	want := "Setup (custom):\n- adjutant: on\n- checkpointer: on\n- Integration: one combined branch\n- Merge: ask me"
	if got != want {
		t.Fatalf("%q", got)
	}
}

func TestSetupIsChecked(t *testing.T) {
	long := strings.Repeat("x", maxSetupAnswer+1)
	for name, s := range map[string]RouteSetup{
		"bad role":       {Roles: []choices.Pick{{Role: "boss", Runner: "herdr", Harness: "claude"}}},
		"bad runner":     {Roles: []choices.Pick{{Role: "reviewer", Runner: "cloud", Harness: "claude"}}},
		"long answer":    {Answers: []SetupAnswer{{Item: "Merge", Answer: long}}},
		"newline":        {Answers: []SetupAnswer{{Item: "Merge", Answer: "a\nb"}}},
		"empty item":     {Answers: []SetupAnswer{{Item: "", Answer: "a"}}},
		"bad account":    {GitHubAccount: "a b"},
		"too many":       {Answers: make([]SetupAnswer, maxSetupAnswers+1)},
		"too many roles": {Roles: make([]choices.Pick, len(choices.Roles)+1)},
		"role route":     {Roles: []choices.Pick{{Role: "reviewer", Runner: "herdr", Harness: "claude", Route: "r-1"}}},
		"long item":      {Answers: []SetupAnswer{{Item: strings.Repeat("i", maxSetupItem+1), Answer: "a"}}},
		"invalid text":   {Answers: []SetupAnswer{{Item: "Merge", Answer: "\xff"}}},
	} {
		if err := s.check(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The HTTP boundary refuses a bad setup before any herdr call.
func TestBadSetupRefusedWithoutMutation(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Setup = &RouteSetup{Answers: []SetupAnswer{{Item: "Merge", Answer: "a\nb"}}}
	code, res := f.post(t, req)
	if code != 400 || res.OK || res.Field != "setup" || !strings.Contains(res.Error, "one line") {
		t.Fatalf("%d %+v", code, res)
	}
	if m := mutations(f.herdr); len(m) != 0 {
		t.Fatalf("mutations %v", m)
	}
}

// Without a request, the setup alone is the prompt's argument.
func TestSetupWithoutRequest(t *testing.T) {
	f := newRouteFixture(t)
	req := f.orchestrator()
	req.Request, req.Checkpointer.On = "", false
	req.Setup = &RouteSetup{Roles: []choices.Pick{}, Answers: []SetupAnswer{}}
	if code, res := f.post(t, req); code != 200 || !res.OK {
		t.Fatalf("%d %+v", code, res)
	}
	if got := promptText(t, f); got != "/herdsman run "+quoteArg("Setup (custom):\n- adjutant: off\n- checkpointer: off") {
		t.Fatalf("%q", got)
	}
}
