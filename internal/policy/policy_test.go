package policy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func mustParse(t *testing.T, s Section) Policy {
	t.Helper()
	p, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestParseReadsEveryField(t *testing.T) {
	got := mustParse(t, Section{
		Budget:         BudgetSection{Wall: "90m", Machine: "20m", Tokens: 4000, Cost: "$2.50"},
		Protected:      []string{"trunk", "stable/*"},
		RequiredChecks: []string{"lint"},
		Deny:           []string{"curl"},
		Roots:          []string{"/srv/a"},
		Landing:        "delegated",
		Review:         "required",
		MaxAgents:      3,
		Profile:        "restricted",
	})
	want := Policy{
		Budget:         Budget{Wall: 90 * time.Minute, Machine: 20 * time.Minute, Tokens: 4000, CostMicros: 2_500_000},
		Protected:      []string{"trunk", "stable/*"},
		RequiredChecks: []string{"lint"},
		Deny:           []string{"curl"},
		Roots:          []string{"/srv/a"},
		Landing:        LandingDelegated,
		Review:         ReviewRequired,
		MaxAgents:      3,
		Profile:        ProfileRestricted,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseRefusesAnythingItCannotEnforce(t *testing.T) {
	for name, s := range map[string]Section{
		"wall not a duration":    {Budget: BudgetSection{Wall: "soon"}},
		"wall zero":              {Budget: BudgetSection{Wall: "0s"}},
		"machine negative":       {Budget: BudgetSection{Machine: "-1m"}},
		"machine not a duration": {Budget: BudgetSection{Machine: "lots"}},
		"tokens negative":        {Budget: BudgetSection{Tokens: -1}},
		"cost words":             {Budget: BudgetSection{Cost: "cheap"}},
		"cost zero":              {Budget: BudgetSection{Cost: "0"}},
		"cost negative":          {Budget: BudgetSection{Cost: "-1"}},
		"cost seven decimals":    {Budget: BudgetSection{Cost: "1.0000001"}},
		"cost huge":              {Budget: BudgetSection{Cost: "99999999999999.0"}},
		"landing":                {Landing: "yolo"},
		"review":                 {Review: "maybe"},
		"profile":                {Profile: "open"},
		"max agents":             {MaxAgents: -2},
		"bad pattern":            {Protected: []string{"a["}},
		"empty pattern":          {Protected: []string{""}},
		"empty deny":             {Deny: []string{"  "}},
		"empty check":            {RequiredChecks: []string{""}},
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%s: Parse accepted %+v", name, s)
		}
	}
}

func TestCostAcceptsPlainAndDollarForms(t *testing.T) {
	for in, want := range map[string]int64{"3": 3_000_000, "$0.25": 250_000, "0.000001": 1} {
		got := mustParse(t, Section{Budget: BudgetSection{Cost: in}}).Budget.CostMicros
		if got != want {
			t.Errorf("%s = %d, want %d", in, got, want)
		}
	}
}

func TestEffectiveDefaults(t *testing.T) {
	var p Policy
	if p.EffectiveLanding() != LandingExact {
		t.Errorf("landing default = %s", p.EffectiveLanding())
	}
	if p.EffectiveProfile() != ProfileTrustedLocal {
		t.Errorf("profile default = %s", p.EffectiveProfile())
	}
	p = Policy{Landing: LandingDelegated, Profile: ProfileRestricted}
	if p.EffectiveLanding() != LandingDelegated || p.EffectiveProfile() != ProfileRestricted {
		t.Errorf("explicit values lost: %+v", p)
	}
}

func TestComposeTakesTheMinimumOfEachLimit(t *testing.T) {
	user := Policy{Budget: Budget{Wall: time.Hour, Machine: 5 * time.Minute, Tokens: 900, CostMicros: 10}, MaxAgents: 4}
	project := Policy{Budget: Budget{Wall: 2 * time.Hour, Machine: time.Minute, Tokens: 1000, CostMicros: 5}, MaxAgents: 8}
	got := Compose(user, project)
	want := Budget{Wall: time.Hour, Machine: time.Minute, Tokens: 900, CostMicros: 5}
	if got.Budget != want || got.MaxAgents != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestComposeTreatsAnUnsetLimitAsNoStatement(t *testing.T) {
	got := Compose(Policy{Budget: Budget{Wall: time.Hour}}, Policy{Budget: Budget{Tokens: 7}, MaxAgents: 2})
	if got.Budget != (Budget{Wall: time.Hour, Tokens: 7}) || got.MaxAgents != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestComposeUnionsListsSortedAndWithoutRepeats(t *testing.T) {
	got := Compose(
		Policy{Protected: []string{"z", "a"}, Deny: []string{"rm"}, RequiredChecks: []string{"unit"}},
		Policy{Protected: []string{"a", "m"}, Deny: []string{"dd"}, RequiredChecks: []string{"lint", "unit"}},
	)
	if !reflect.DeepEqual(got.Protected, []string{"a", "m", "z"}) {
		t.Errorf("protected = %v", got.Protected)
	}
	if !reflect.DeepEqual(got.Deny, []string{"dd", "rm"}) {
		t.Errorf("deny = %v", got.Deny)
	}
	if !reflect.DeepEqual(got.RequiredChecks, []string{"lint", "unit"}) {
		t.Errorf("checks = %v", got.RequiredChecks)
	}
}

func TestComposeNeverLetsALaterFileWiden(t *testing.T) {
	user := Policy{Landing: LandingExact, Review: ReviewRequired, Profile: ProfileRestricted, Roots: []string{"/a", "/b"}}
	project := Policy{Landing: LandingDelegated, Review: ReviewOptional, Profile: ProfileTrustedLocal, Roots: []string{"/b", "/c"}}
	got := Compose(user, project)
	if got.Landing != LandingExact || got.Review != ReviewRequired || got.Profile != ProfileRestricted {
		t.Fatalf("widened: %+v", got)
	}
	if !reflect.DeepEqual(got.Roots, []string{"/b"}) {
		t.Fatalf("roots = %v", got.Roots)
	}
	// The stricter value wins from either side.
	rev := Compose(Policy{Landing: LandingDelegated, Review: ReviewOptional, Profile: ProfileTrustedLocal}, Policy{Landing: LandingExact, Review: ReviewRequired, Profile: ProfileRestricted})
	if rev.Landing != LandingExact || rev.Review != ReviewRequired || rev.Profile != ProfileRestricted {
		t.Fatalf("stricter project value lost: %+v", rev)
	}
}

func TestComposeKeepsDisjointRootsEmptyOfGrants(t *testing.T) {
	got := Compose(Policy{Roots: []string{"/a"}}, Policy{Roots: []string{"/b"}})
	if !got.NoRoots {
		t.Fatalf("two disjoint grants must leave nothing granted: %+v", got)
	}
	// A third file cannot bring a root back.
	got = Compose(Policy{Roots: []string{"/a"}}, Policy{Roots: []string{"/b"}}, Policy{Roots: []string{"/a"}})
	if !got.NoRoots || len(got.Roots) != 0 {
		t.Fatalf("root came back: %+v", got)
	}
}

func TestComposeLetsAFileSetWhatOthersLeftUnset(t *testing.T) {
	got := Compose(Policy{}, Policy{Landing: LandingDelegated, Review: ReviewOptional, Roots: []string{"/x"}, Profile: ProfileTrustedLocal})
	if got.Landing != LandingDelegated || got.Review != ReviewOptional || !reflect.DeepEqual(got.Roots, []string{"/x"}) || got.Profile != ProfileTrustedLocal {
		t.Fatalf("got %+v", got)
	}
	if empty := Compose(); !reflect.DeepEqual(empty, Policy{}) {
		t.Fatalf("no files: %+v", empty)
	}
}

func TestBudgetCheckReportsEachExceededDimensionWithItsOvershoot(t *testing.T) {
	b := Budget{Wall: time.Hour, Machine: time.Minute, Tokens: 100, CostMicros: 1_000_000}
	u := Usage{Started: epoch, Machine: 90 * time.Second, Tokens: 130, CostMicros: 1_500_000}
	got := b.Check(u, epoch.Add(61*time.Minute))
	want := []Exceeded{
		{Dimension: DimensionWall, Limit: "1h0m0s", Used: "1h1m0s", Overshoot: "1m0s"},
		{Dimension: DimensionMachine, Limit: "1m0s", Used: "1m30s", Overshoot: "30s"},
		{Dimension: DimensionTokens, Limit: "100", Used: "130", Overshoot: "30"},
		{Dimension: DimensionCost, Limit: "$1.00", Used: "$1.50", Overshoot: "$0.50"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestBudgetLimitsAreReachedAtExactlyTheLimit(t *testing.T) {
	b := Budget{Wall: time.Hour, Tokens: 10}
	if got := b.Check(Usage{Started: epoch, Tokens: 9}, epoch.Add(time.Hour-time.Nanosecond)); len(got) != 0 {
		t.Fatalf("below limit: %+v", got)
	}
	got := b.Check(Usage{Started: epoch, Tokens: 10}, epoch.Add(time.Hour))
	if len(got) != 2 {
		t.Fatalf("at limit: %+v", got)
	}
}

func TestBudgetCheckSkipsUnsetLimitsAndAnUnknownStart(t *testing.T) {
	if got := (Budget{}).Check(Usage{Started: epoch, Machine: time.Hour, Tokens: 1, CostMicros: 1}, epoch.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("no limits: %+v", got)
	}
	if got := (Budget{Wall: time.Second}).Check(Usage{}, epoch); len(got) != 0 {
		t.Fatalf("no start time: %+v", got)
	}
}

func TestAdmitRefusesEveryNewOperationAtTheWallDeadline(t *testing.T) {
	p := Policy{Budget: Budget{Wall: time.Hour}}
	for _, kind := range []string{"new", "merge", "up", "hook", "automation", "prelude"} {
		op := Operation{Kind: kind, Task: "t1", Route: "r1"}
		err := Admit(p, op, Usages{Task: Usage{Started: epoch}}, epoch.Add(time.Hour))
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: got %v", kind, err)
		}
		if refusal.Scope != (Scope{Kind: ScopeTask, Name: "t1"}) || refusal.Reason != ReasonBudget {
			t.Fatalf("%s: %+v", kind, refusal)
		}
		if !strings.Contains(err.Error(), "raise") {
			t.Fatalf("%s: message does not say how to continue: %v", kind, err)
		}
	}
}

func TestAdmitChecksTheRouteAsWellAsTheTask(t *testing.T) {
	p := Policy{Budget: Budget{Wall: time.Hour}}
	op := Operation{Kind: "merge", Task: "t1", Route: "r1"}
	err := Admit(p, op, Usages{Task: Usage{Started: epoch.Add(50 * time.Minute)}, Route: Usage{Started: epoch}}, epoch.Add(61*time.Minute))
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Scope != (Scope{Kind: ScopeRoute, Name: "r1"}) {
		t.Fatalf("got %v", err)
	}
}

func TestAdmitIgnoresAnEmptyScopeName(t *testing.T) {
	p := Policy{Budget: Budget{Wall: time.Hour}}
	err := Admit(p, Operation{Kind: "new"}, Usages{Task: Usage{Started: epoch}, Route: Usage{Started: epoch}}, epoch.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("an operation outside any task or route: %v", err)
	}
}

func TestAdmitReopensWhenAPersonRaisesTheBudget(t *testing.T) {
	op := Operation{Kind: "new", Task: "t1"}
	usage := Usages{Task: Usage{Started: epoch}}
	now := epoch.Add(2 * time.Hour)
	if Admit(Policy{Budget: Budget{Wall: time.Hour}}, op, usage, now) == nil {
		t.Fatal("expected a refusal")
	}
	if err := Admit(Policy{Budget: Budget{Wall: 3 * time.Hour}}, op, usage, now); err != nil {
		t.Fatalf("after raising: %v", err)
	}
}

func TestAdmitDoesNotRefuseOnOtherDimensions(t *testing.T) {
	p := Policy{Budget: Budget{Machine: time.Second, Tokens: 1, CostMicros: 1}}
	u := Usage{Started: epoch, Machine: time.Hour, Tokens: 9, CostMicros: 9}
	if err := Admit(p, Operation{Kind: "new", Task: "t"}, Usages{Task: u, Route: u}, epoch); err != nil {
		t.Fatalf("those promises are enforced elsewhere: %v", err)
	}
}

func TestAdmitCapsConcurrentAgents(t *testing.T) {
	p := Policy{MaxAgents: 2}
	if err := Admit(p, Operation{Kind: "up", Route: "r", Agents: 2}, Usages{}, epoch); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	err := Admit(p, Operation{Kind: "up", Route: "r", Agents: 3}, Usages{}, epoch)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonAgents {
		t.Fatalf("over the cap: %v", err)
	}
	if !strings.Contains(err.Error(), "max_agents") {
		t.Fatalf("message: %v", err)
	}
	if err := Admit(Policy{}, Operation{Kind: "up", Agents: 99}, Usages{}, epoch); err != nil {
		t.Fatalf("no cap: %v", err)
	}
}

func TestCheckCommandRefusesDeniedCommands(t *testing.T) {
	p := Policy{Deny: []string{"curl", "git push", "/usr/bin/nc"}}
	for _, cmd := range []string{
		"curl https://example.test",
		"curl",
		"  git   push origin x",
		"make && git push",
		"true; curl x",
		"a || curl x",
		"echo ok | curl x",
		"A=1 B=2 curl x",
		"env A=1 curl x",
		"env curl x",
		"echo one\ncurl x",
		"/usr/bin/curl x",
		"nc -l 1",
		"true & curl x",
	} {
		err := p.CheckCommand(cmd)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Reason != ReasonDenied {
			t.Errorf("%q: got %v", cmd, err)
		}
	}
}

func TestCheckCommandAllowsWhatIsNotDenied(t *testing.T) {
	p := Policy{Deny: []string{"curl", "git push"}}
	for _, cmd := range []string{
		"",
		"curlew x",
		"git pushy",
		"git status",
		"echo 'a; curl b'",
		`echo "a && curl b"`,
		"echo curl",
		"A=1",
	} {
		if err := p.CheckCommand(cmd); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
}

func TestProtectedMatchesNamesAndPatterns(t *testing.T) {
	p := Policy{Protected: []string{"trunk", "stable/*"}}
	for _, b := range []string{"trunk", "refs/heads/trunk", "stable/1.2"} {
		if !p.IsProtected(b) {
			t.Errorf("%s should be protected", b)
		}
	}
	for _, b := range []string{"trunk2", "feature", "stable/1/2", ""} {
		if p.IsProtected(b) {
			t.Errorf("%s should not be protected", b)
		}
	}
}

func TestCheckRefMoveRefusesAProtectedBranch(t *testing.T) {
	p := Policy{Protected: []string{"trunk"}}
	err := p.CheckRefMove("delete", "trunk")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonProtected {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "delete") || !strings.Contains(err.Error(), "trunk") {
		t.Fatalf("message: %v", err)
	}
	if err := p.CheckRefMove("delete", "topic"); err != nil {
		t.Fatalf("unprotected: %v", err)
	}
}

func TestCheckLandingRequiresEveryCheckToPass(t *testing.T) {
	p := Policy{RequiredChecks: []string{"lint", "unit", "e2e"}}
	err := p.CheckLanding(Landing{Contract: LandingExact, Checks: map[string]bool{"lint": true, "unit": false}, Reviewed: true})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonChecks {
		t.Fatalf("got %v", err)
	}
	if !reflect.DeepEqual(refusal.Missing, []string{"e2e", "unit"}) {
		t.Fatalf("missing = %v", refusal.Missing)
	}
	ok := Landing{Contract: LandingExact, Checks: map[string]bool{"lint": true, "unit": true, "e2e": true}, Reviewed: true}
	if err := p.CheckLanding(ok); err != nil {
		t.Fatalf("all passed: %v", err)
	}
}

func TestCheckLandingAcceptsDelegatedOnlyWhenPolicySaysSo(t *testing.T) {
	var refusal *Refusal
	err := (Policy{}).CheckLanding(Landing{Contract: LandingDelegated, Reviewed: true})
	if !errors.As(err, &refusal) || refusal.Reason != ReasonLanding {
		t.Fatalf("got %v", err)
	}
	if err := (Policy{Landing: LandingDelegated}).CheckLanding(Landing{Contract: LandingDelegated, Reviewed: true}); err != nil {
		t.Fatalf("policy allows it: %v", err)
	}
	if err := (Policy{Landing: LandingDelegated}).CheckLanding(Landing{Contract: LandingExact, Reviewed: true}); err != nil {
		t.Fatalf("a stricter contract than the policy's is fine: %v", err)
	}
}

func TestCheckLandingForbidsWaiversWhenReviewIsRequired(t *testing.T) {
	p := Policy{Review: ReviewRequired}
	var refusal *Refusal
	err := p.CheckLanding(Landing{Contract: LandingExact, Waiver: "hotfix"})
	if !errors.As(err, &refusal) || refusal.Reason != ReasonReview {
		t.Fatalf("waiver: %v", err)
	}
	// A landing that is both reviewed and waived still carries a waiver.
	if err := p.CheckLanding(Landing{Contract: LandingExact, Waiver: "x", Reviewed: true}); !errors.As(err, &refusal) {
		t.Fatalf("waiver beside a review: %v", err)
	}
	if err := p.CheckLanding(Landing{Contract: LandingExact}); !errors.As(err, &refusal) {
		t.Fatalf("no review at all: %v", err)
	}
	if err := p.CheckLanding(Landing{Contract: LandingExact, Reviewed: true}); err != nil {
		t.Fatalf("reviewed: %v", err)
	}
	if err := (Policy{}).CheckLanding(Landing{Contract: LandingExact, Waiver: "ok"}); err != nil {
		t.Fatalf("waivers are allowed by default: %v", err)
	}
}

type fakeInterrupter struct {
	calls []Scope
	err   error
}

func (f *fakeInterrupter) Interrupt(_ context.Context, s Scope) error {
	f.calls = append(f.calls, s)
	return f.err
}

type fakeTeardown struct {
	calls  []Scope
	grace  []time.Duration
	report TeardownReport
	err    error
}

func (f *fakeTeardown) Teardown(_ context.Context, s Scope, grace time.Duration) (TeardownReport, error) {
	f.calls = append(f.calls, s)
	f.grace = append(f.grace, grace)
	return f.report, f.err
}

func newEnforcer(p Policy) (*Enforcer, *fakeInterrupter, *fakeTeardown, *[]Event) {
	in, td, events := &fakeInterrupter{}, &fakeTeardown{}, &[]Event{}
	return &Enforcer{
		Policy: p, Grace: 7 * time.Second, Now: func() time.Time { return epoch.Add(time.Hour) },
		Interrupt: in, Teardown: td, Emit: func(e Event) { *events = append(*events, e) },
	}, in, td, events
}

var taskScope = Scope{Kind: ScopeTask, Name: "t1"}

func TestEnforcerInterruptsOnTokensCostAndWallAndReportsOvershoot(t *testing.T) {
	for name, tc := range map[string]struct {
		b Budget
		u Usage
		d Dimension
	}{
		"tokens": {Budget{Tokens: 5}, Usage{Tokens: 8}, DimensionTokens},
		"cost":   {Budget{CostMicros: 5}, Usage{CostMicros: 8}, DimensionCost},
		"wall":   {Budget{Wall: time.Minute}, Usage{Started: epoch}, DimensionWall},
	} {
		e, in, td, events := newEnforcer(Policy{Budget: tc.b})
		actions, err := e.Observe(context.Background(), taskScope, tc.u)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(in.calls) != 1 || len(td.calls) != 0 {
			t.Fatalf("%s: interrupts %v teardowns %v", name, in.calls, td.calls)
		}
		if len(actions) != 1 || actions[0].Kind != ActionInterrupt || actions[0].Exceeded.Dimension != tc.d || actions[0].Exceeded.Overshoot == "" {
			t.Fatalf("%s: actions %+v", name, actions)
		}
		if len(*events) != 1 || (*events)[0].Kind != EventBudgetExhausted || (*events)[0].Scope != taskScope {
			t.Fatalf("%s: events %+v", name, *events)
		}
	}
}

func TestEnforcerRunsTeardownOnMachineAndReportsWhatItLeft(t *testing.T) {
	e, in, td, events := newEnforcer(Policy{Budget: Budget{Machine: time.Minute}})
	td.report = TeardownReport{Stopped: []string{"pid 10"}, LeftRunning: []string{"pid 20 (shared)"}}
	actions, err := e.Observe(context.Background(), taskScope, Usage{Machine: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(td.calls) != 1 || td.grace[0] != 7*time.Second || len(in.calls) != 0 {
		t.Fatalf("teardowns %v grace %v interrupts %v", td.calls, td.grace, in.calls)
	}
	if actions[0].Kind != ActionTeardown || !reflect.DeepEqual(actions[0].Report, td.report) {
		t.Fatalf("actions %+v", actions)
	}
	if !reflect.DeepEqual((*events)[0].LeftRunning, []string{"pid 20 (shared)"}) {
		t.Fatalf("events %+v", *events)
	}
}

func TestEnforcerActsOncePerExhaustion(t *testing.T) {
	e, in, _, events := newEnforcer(Policy{Budget: Budget{Tokens: 5}})
	for range 3 {
		if _, err := e.Observe(context.Background(), taskScope, Usage{Tokens: 8}); err != nil {
			t.Fatal(err)
		}
	}
	if len(in.calls) != 1 || len(*events) != 1 {
		t.Fatalf("interrupts %d events %d", len(in.calls), len(*events))
	}
}

func TestEnforcerTracksEachScopeApart(t *testing.T) {
	e, in, _, _ := newEnforcer(Policy{Budget: Budget{Tokens: 5}})
	for _, s := range []Scope{taskScope, {Kind: ScopeRoute, Name: "t1"}} {
		if _, err := e.Observe(context.Background(), s, Usage{Tokens: 8}); err != nil {
			t.Fatal(err)
		}
	}
	if len(in.calls) != 2 {
		t.Fatalf("interrupts %v", in.calls)
	}
}

func TestEnforcerRetriesAFailedActionWithoutARepeatedEvent(t *testing.T) {
	e, in, _, events := newEnforcer(Policy{Budget: Budget{Tokens: 5}})
	in.err = errors.New("pane gone")
	if _, err := e.Observe(context.Background(), taskScope, Usage{Tokens: 8}); err == nil || !strings.Contains(err.Error(), "pane gone") {
		t.Fatalf("got %v", err)
	}
	in.err = nil
	if _, err := e.Observe(context.Background(), taskScope, Usage{Tokens: 8}); err != nil {
		t.Fatal(err)
	}
	if len(in.calls) != 2 || len(*events) != 1 {
		t.Fatalf("interrupts %d events %d", len(in.calls), len(*events))
	}
}

func TestEnforcerRetriesAFailedTeardown(t *testing.T) {
	e, _, td, _ := newEnforcer(Policy{Budget: Budget{Machine: time.Second}})
	td.err = errors.New("plan changed")
	if _, err := e.Observe(context.Background(), taskScope, Usage{Machine: time.Minute}); err == nil {
		t.Fatal("expected the teardown error")
	}
	td.err = nil
	if _, err := e.Observe(context.Background(), taskScope, Usage{Machine: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if len(td.calls) != 2 {
		t.Fatalf("teardowns %d", len(td.calls))
	}
}

func TestEnforcerRearmsAfterTheBudgetIsRaised(t *testing.T) {
	e, in, _, events := newEnforcer(Policy{Budget: Budget{Tokens: 5}})
	ctx := context.Background()
	if _, err := e.Observe(ctx, taskScope, Usage{Tokens: 8}); err != nil {
		t.Fatal(err)
	}
	e.SetPolicy(Policy{Budget: Budget{Tokens: 100}})
	if actions, err := e.Observe(ctx, taskScope, Usage{Tokens: 8}); err != nil || len(actions) != 0 {
		t.Fatalf("raised: %+v %v", actions, err)
	}
	if _, err := e.Observe(ctx, taskScope, Usage{Tokens: 100}); err != nil {
		t.Fatal(err)
	}
	if len(in.calls) != 2 || len(*events) != 2 {
		t.Fatalf("interrupts %d events %d", len(in.calls), len(*events))
	}
}

func TestEnforcerUsesDefaultsWhenLeftUnset(t *testing.T) {
	e, _, td, _ := newEnforcer(Policy{Budget: Budget{Machine: time.Second}})
	e.Grace, e.Now, e.Emit = 0, nil, nil
	if _, err := e.Observe(context.Background(), taskScope, Usage{Machine: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if td.grace[0] != DefaultGrace {
		t.Fatalf("grace = %v", td.grace[0])
	}
}

func TestEnforcerWithoutAPortFailsLoudly(t *testing.T) {
	e := &Enforcer{Policy: Policy{Budget: Budget{Tokens: 1, Machine: time.Second}}}
	if _, err := e.Observe(context.Background(), taskScope, Usage{Tokens: 2}); err == nil || !strings.Contains(err.Error(), "interrupt") {
		t.Fatalf("no interrupter: %v", err)
	}
	if _, err := e.Observe(context.Background(), taskScope, Usage{Machine: time.Minute}); err == nil || !strings.Contains(err.Error(), "teardown") {
		t.Fatalf("no teardown: %v", err)
	}
}

func TestScopeAndRefusalRead(t *testing.T) {
	if got := (Scope{Kind: ScopeRoute, Name: "r"}).String(); got != "route r" {
		t.Fatalf("scope = %q", got)
	}
	r := &Refusal{Reason: ReasonBudget, Message: "m"}
	if r.Error() != "m" {
		t.Fatalf("refusal = %q", r.Error())
	}
}

func TestCostTextKeepsSmallAmounts(t *testing.T) {
	if got := costText(1); got != "$0.000001" {
		t.Fatalf("got %s", got)
	}
	if got := costText(1_250_000); got != "$1.25" {
		t.Fatalf("got %s", got)
	}
}

func TestCheckCommandReadsBackslashEscapes(t *testing.T) {
	p := Policy{Deny: []string{"curl"}}
	if err := p.CheckCommand(`cu\rl x`); err == nil {
		t.Error("an escaped letter hid the program name")
	}
	if err := p.CheckCommand(`echo a\;curl`); err != nil {
		t.Errorf("an escaped separator split the command: %v", err)
	}
}

func TestComposeKeepsAnEarlierFileWhenALaterSaysNothing(t *testing.T) {
	first := Policy{Landing: LandingDelegated, Review: ReviewOptional, Roots: []string{"/a"}}
	got := Compose(first, Policy{})
	if got.Landing != LandingDelegated || got.Review != ReviewOptional || !reflect.DeepEqual(got.Roots, []string{"/a"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestComposeKeepsAnAgreedRelaxedValue(t *testing.T) {
	if got := Compose(Policy{Landing: LandingDelegated}, Policy{Landing: LandingDelegated}); got.Landing != LandingDelegated {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckCommandSeesThroughShellSyntax(t *testing.T) {
	p := Policy{Deny: []string{"curl"}}
	for _, cmd := range []string{
		">out curl x",
		"2>out curl x",
		">&2 curl x",
		"> out curl x",
		"&>out curl x",
		"<in curl x",
		"/usr/bin/env curl x",
		"env -u X curl x",
		"env --unset X curl x",
		"env --unset=X curl x",
		"env -uX curl x",
		"env -C /tmp curl x",
		"env -i A=1 curl x",
		"env -- curl x",
		"if true; then curl x; fi",
		"while true; do curl x; done",
		"for a in b; do curl x; done",
		"! curl x",
		"{ curl x; }",
		"time -p curl x",
		"exec curl x",
		"command curl x",
		"nohup curl x",
		"cu\\\nrl x",
		`c"ur"l x`,
		"true |& curl x",
	} {
		err := p.CheckCommand(cmd)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Reason != ReasonDenied {
			t.Errorf("%q: got %v", cmd, err)
		}
	}
}

func TestCheckCommandRefusesWhatItCannotDetermine(t *testing.T) {
	p := Policy{Deny: []string{"curl", "git push"}}
	for _, cmd := range []string{
		"$CMD x",
		"c${x}url x",
		"/usr/bin/cur? x",
		"{curl,x}",
		"echo $(curl x)",
		`echo "$(curl x)"`,
		"echo `curl x`",
		"(curl x)",
		"f() { curl x; }",
		"cat <(curl x)",
		"case a in a) curl x;; esac",
		"eval curl x",
		"sh -c 'curl x'",
		"bash -ec 'curl x'",
		"sudo curl x",
		"xargs curl",
		"timeout 5 curl x",
		"find . -exec curl x ;",
		"exec -a name curl x",
		"env -S 'curl x'",
		"git $sub x",
	} {
		err := p.CheckCommand(cmd)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Reason != ReasonUnclear {
			t.Errorf("%q: got %v", cmd, err)
		}
	}
}

func TestCheckCommandMatchesMultiWordEntriesAcrossOptions(t *testing.T) {
	p := Policy{Deny: []string{"git push"}}
	if err := p.CheckCommand("git -c a=b push origin"); err == nil {
		t.Error("an option between the words hid the entry")
	}
}

func TestCheckCommandDeniesTheWrapperItself(t *testing.T) {
	p := Policy{Deny: []string{"nohup", "env"}}
	for _, cmd := range []string{"nohup true", "env true", "/usr/bin/env A=1 true"} {
		if err := p.CheckCommand(cmd); err == nil {
			t.Errorf("%q was not denied", cmd)
		}
	}
}

func TestCheckCommandAllowsOrdinaryShellWithADenyList(t *testing.T) {
	p := Policy{Deny: []string{"curl"}}
	for _, cmd := range []string{
		"echo $HOME",
		`echo "$HOME" > out 2>&1`,
		"echo '$(curl x)'",
		`echo \$\(curl`,
		"ls *.go",
		"for a in b; do echo a; done",
		"if true; then echo x; fi",
		"bash ./setup.sh",
		"sh script.sh --flag",
		"find . -name x",
		"env",
		"env -i",
		"env --unset",
		"cat < in",
		`"tail`,
	} {
		if err := p.CheckCommand(cmd); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
}

func TestCheckCommandIsNoCostWithoutADenyList(t *testing.T) {
	if err := (Policy{}).CheckCommand("eval $(anything)"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCommandReadsQuotingEdges(t *testing.T) {
	p := Policy{Deny: []string{"curl"}}
	for _, cmd := range []string{`echo "a\$(curl b\q"`, "echo \"\\`curl\"", "'tail"} {
		if err := p.CheckCommand(cmd); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	err := p.CheckCommand("bash --command 'curl x'")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonUnclear {
		t.Errorf("--command: got %v", err)
	}
}
