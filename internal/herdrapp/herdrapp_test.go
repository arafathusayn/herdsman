package herdrapp_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdrapp"
	"github.com/arafathusayn/herdsman/internal/herdrapp/apptest"
)

var now = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

// fixture is one app home with a fake app and the selected core.
type fixture struct {
	home string
	fake *apptest.Fake
	app  *herdrapp.App
	core *apptest.Core
	sel  herdrapp.Core
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	fake := apptest.New(home)
	core := apptest.NewCore("core-a")
	return fixture{
		home: home, fake: fake, app: herdrapp.New(fake, home), core: core,
		sel: herdrapp.Core{ID: "core-a", Endpoint: "session=hs-test"},
	}
}

func mustIDs(t *testing.T) herdrapp.PlacementIDs {
	t.Helper()
	ids, err := herdrapp.NewPlacementIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func launchLabels(ids herdrapp.PlacementIDs) herdrapp.LaunchLabels {
	return herdrapp.LaunchLabels{
		Owner: "install-1", Route: "route-1", Agent: "agent-1", Execution: "exec-1", Role: "implementer",
		Placement: ids,
	}
}

// launch writes a managed agent's labels on the core and reads them back.
func (f fixture) launch(t *testing.T, address string) herdrapp.Owned {
	t.Helper()
	want := launchLabels(mustIDs(t))
	labels, err := want.Map()
	if err != nil {
		t.Fatal(err)
	}
	f.core.Run(address, "agent", labels)
	read, err := f.core.TerminalLabels(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := herdrapp.OwnedFromCore(address, read, want)
	if err != nil {
		t.Fatal(err)
	}
	return owned
}

func (f fixture) verify(t *testing.T) herdrapp.Binding {
	t.Helper()
	challenge := mustIDs(t)
	f.core.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab})
	binding, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t1", now)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestVerifyBindsTheSelectedCore(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	b := f.verify(t)
	if b.CoreID != "core-a" || b.Endpoint != f.sel.Endpoint || b.AppHome != f.home || b.AppVersion != "0.1.15" || !b.VerifiedAt.Equal(now) || b.AppHomeID == "" {
		t.Fatalf("binding = %+v", b)
	}
	if err := b.Valid(f.sel, f.home); err != nil {
		t.Fatalf("fresh binding refused: %v", err)
	}
}

// An app attached to another core shows a terminal with the same address,
// the same name and the same space and tab names. It must not verify.
func TestVerifyRefusesAppOnAnotherCore(t *testing.T) {
	f := newFixture(t)
	other := apptest.NewCore("core-b")
	decoy := mustIDs(t)
	other.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: decoy.Space, herdrapp.LabelTab: decoy.Tab})
	f.fake.Open(other)

	challenge := mustIDs(t)
	f.core.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab})
	_, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t1", now)
	if !errors.Is(err, herdrapp.ErrChallengeNotShown) {
		t.Fatalf("err = %v, want ErrChallengeNotShown", err)
	}
}

// The selected core reached through the app as a remote machine is not the
// local binding the challenge asks for.
func TestVerifyRefusesChallengeOnRemoteMachine(t *testing.T) {
	f := newFixture(t)
	local := apptest.NewCore("core-b")
	local.Run("t1", "shell", nil)
	f.fake.Open(local)
	f.fake.AddMachine("vps", f.core)

	challenge := mustIDs(t)
	f.core.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab})
	_, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t1", now)
	if !errors.Is(err, herdrapp.ErrEndpointMismatch) {
		t.Fatalf("err = %v, want ErrEndpointMismatch", err)
	}
}

// Labels written on the core the app shows, but not on the selected core,
// do not verify the selected core.
func TestVerifyRequiresLabelsOnSelectedCore(t *testing.T) {
	f := newFixture(t)
	other := apptest.NewCore("core-b")
	f.fake.Open(other)
	challenge := mustIDs(t)
	other.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab})
	f.core.Run("t1", "challenge", nil)
	_, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t1", now)
	if !errors.Is(err, herdrapp.ErrChallengeNotOnCore) {
		t.Fatalf("err = %v, want ErrChallengeNotOnCore", err)
	}
}

// The challenge ids turn up on another core under another address: the
// window shows the challenge tab holding a terminal that is not the one the
// selected core carries the challenge on.
func TestVerifyRefusesOtherTerminalInChallengeTab(t *testing.T) {
	f := newFixture(t)
	other := apptest.NewCore("core-b")
	f.fake.Open(other)
	challenge := mustIDs(t)
	labels := map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab}
	other.Run("t1", "challenge", labels)
	f.core.Run("t9", "challenge", labels)
	_, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t9", now)
	if !errors.Is(err, herdrapp.ErrEndpointMismatch) {
		t.Fatalf("err = %v, want ErrEndpointMismatch", err)
	}
}

func TestVerifyWithWindowClosedIsAppClosed(t *testing.T) {
	f := newFixture(t)
	challenge := mustIDs(t)
	f.core.Run("t1", "challenge", map[string]string{herdrapp.LabelSpace: challenge.Space, herdrapp.LabelTab: challenge.Tab})
	_, err := herdrapp.Verify(context.Background(), f.app, f.sel, f.core, challenge, "t1", now)
	if !errors.Is(err, herdrapp.ErrAppClosed) {
		t.Fatalf("err = %v, want ErrAppClosed", err)
	}
}

func TestBindingInvalidatedByChangedEndpointHomeOrCopy(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	b := f.verify(t)

	cases := map[string]func() error{
		"other core":     func() error { return b.Valid(herdrapp.Core{ID: "core-b", Endpoint: f.sel.Endpoint}, f.home) },
		"other endpoint": func() error { return b.Valid(herdrapp.Core{ID: f.sel.ID, Endpoint: "session=other"}, f.home) },
		"other home":     func() error { return b.Valid(f.sel, t.TempDir()) },
		"none recorded":  func() error { return herdrapp.Binding{}.Valid(f.sel, f.home) },
	}
	for name, check := range cases {
		if err := check(); !errors.Is(err, herdrapp.ErrBindingStale) {
			t.Errorf("%s: err = %v, want ErrBindingStale", name, err)
		}
	}

	// A copy of the home put in its place is another directory.
	if err := os.Rename(f.home, f.home+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := b.Valid(f.sel, f.home); !errors.Is(err, herdrapp.ErrBindingStale) {
		t.Fatalf("copied home: err = %v, want ErrBindingStale", err)
	}
}

func TestPanePresentAndAgentListedAreSeparate(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	owned := f.launch(t, "t2")

	obs, err := f.app.Observe(context.Background(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.PanePresent || obs.AgentListed || obs.Rearranged {
		t.Fatalf("starting harness: %+v, want a pane and no agent", obs)
	}
	f.core.SetStatus("t2", "idle")
	obs, err = f.app.Observe(context.Background(), owned)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.PanePresent || !obs.AgentListed {
		t.Fatalf("ready harness: %+v, want pane and agent", obs)
	}
}

func TestNameAndShowReadBack(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	first := f.launch(t, "t2")
	second := f.launch(t, "t3")
	ctx := context.Background()

	if err := f.app.NameSpace(ctx, second, "route one"); err != nil {
		t.Fatal(err)
	}
	if err := f.app.NameTab(ctx, second, "reviewer"); err != nil {
		t.Fatal(err)
	}
	layout, err := f.app.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Spaces[1].Name != "route one" || layout.Spaces[1].Tabs[0].Name != "reviewer" {
		t.Fatalf("layout = %+v", layout.Spaces)
	}
	if _, err := f.app.Show(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Show(ctx, first); err != nil {
		t.Fatal(err)
	}
}

// A reply with pending:true is an acknowledgement, never the effect.
func TestPendingReplyIsNotPlacement(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	f.launch(t, "t2")
	second := f.launch(t, "t3")
	f.fake.PendingOnly(true)
	ctx := context.Background()

	if err := f.app.NameSpace(ctx, second, "route one"); !errors.Is(err, herdrapp.ErrNotApplied) {
		t.Fatalf("NameSpace err = %v, want ErrNotApplied", err)
	}
	if err := f.app.NameTab(ctx, second, "reviewer"); !errors.Is(err, herdrapp.ErrNotApplied) {
		t.Fatalf("NameTab err = %v, want ErrNotApplied", err)
	}
	if _, err := f.app.Show(ctx, second); !errors.Is(err, herdrapp.ErrNotApplied) {
		t.Fatalf("Show err = %v, want ErrNotApplied", err)
	}
}

func TestNamingRefusesContainersNotOwned(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	want := launchLabels(mustIDs(t))
	f.core.Run("t2", "agent", map[string]string{herdrapp.LabelSpace: want.Placement.Space, herdrapp.LabelTab: want.Placement.Tab})
	read, _ := f.core.TerminalLabels(context.Background(), "t2")
	if _, err := herdrapp.OwnedFromCore("t2", read, want); !errors.Is(err, herdrapp.ErrNotOwned) {
		t.Fatalf("labels without ownership: err = %v, want ErrNotOwned", err)
	}

	// Owned on paper, but the window shows that space on another machine.
	remote := newFixture(t)
	local := apptest.NewCore("core-b")
	remote.fake.Open(local)
	remote.fake.AddMachine("vps", remote.core)
	owned := remote.launch(t, "t2")
	if err := remote.app.NameSpace(context.Background(), owned, "x"); !errors.Is(err, herdrapp.ErrNotOwned) {
		t.Fatalf("remote space: err = %v, want ErrNotOwned", err)
	}
	for _, call := range remote.fake.Calls() {
		if call.Args[0] == "do" {
			t.Fatalf("a command was sent for a container not owned: %v", call.Args)
		}
	}
}

func TestPresentationLabelsNeverWritten(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	f.verify(t)
	owned := f.launch(t, "t2")
	f.core.SetStatus("t2", "idle")
	ctx := context.Background()
	if err := f.app.NameSpace(ctx, owned, "route one"); err != nil {
		t.Fatal(err)
	}
	if err := f.app.NameTab(ctx, owned, "implementer"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Show(ctx, owned); err != nil {
		t.Fatal(err)
	}

	for _, written := range f.core.Writes() {
		for _, key := range []string{"herdr.seen", "herdr.pinned", "where"} {
			if _, ok := written[key]; ok {
				t.Fatalf("Herdsman wrote %s: %v", key, written)
			}
		}
	}
	allowed := []string{"space.rename", "tab.rename", "agent.focus", "pane.focus"}
	for _, call := range f.fake.Calls() {
		if call.Args[0] == "do" && !slices.Contains(allowed, call.Args[1]) {
			t.Fatalf("sent %s", call.Args[1])
		}
		if call.Home != f.home {
			t.Fatalf("call with home %q", call.Home)
		}
	}
	// The app's own labels stay as the app wrote them.
	read, _ := f.core.TerminalLabels(ctx, "t2")
	if read["where"] == "" || read["herdr.seen"] == "" {
		t.Fatalf("app presentation label lost: %v", read)
	}

	for _, key := range []string{"herdr.seen", "herdr.pinned", "where"} {
		labels := map[string]string{herdrapp.LabelSpace: "hs-a", key: "x"}
		if err := herdrapp.CheckLabels(labels); !errors.Is(err, herdrapp.ErrPresentationLabel) {
			t.Errorf("CheckLabels(%s) = %v", key, err)
		}
	}
	labels, err := launchLabels(mustIDs(t)).Map()
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Sorted(maps.Keys(labels))
	want := []string{"herdr.space", "herdr.tab", "herdsman.agent", "herdsman.execution", "herdsman.owner", "herdsman.role", "herdsman.route"}
	if !slices.Equal(keys, want) {
		t.Fatalf("launch label keys = %v", keys)
	}
}

func TestMissingOrUnsupportedAppNamesTUIBackend(t *testing.T) {
	cases := map[string]struct {
		setup func(*apptest.Fake)
		want  error
	}{
		"missing":     {func(f *apptest.Fake) { f.Uninstall() }, herdrapp.ErrUnavailable},
		"too old":     {func(f *apptest.Fake) { f.SetVersion("0.1.9") }, herdrapp.ErrUnsupported},
		"bad version": {func(f *apptest.Fake) { f.SetVersion("nightly") }, herdrapp.ErrUnsupported},
		"no agent.focus": {func(f *apptest.Fake) {
			f.SetCommands(slices.DeleteFunc(slices.Clone(apptest.DefaultCommands), func(s string) bool { return s == "agent.focus" }))
		}, herdrapp.ErrUnsupported},
	}
	for name, c := range cases {
		home := t.TempDir()
		fake := apptest.New(home)
		c.setup(fake)
		_, err := herdrapp.New(fake, home).Check(context.Background())
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), `backend = "herdr-tui"`) {
			t.Errorf("%s: err = %v, want %v naming the TUI backend", name, err, c.want)
		}
	}

	home := t.TempDir()
	_, err := herdrapp.New(herdrapp.ExecRunner{Binary: "herdr-app-not-installed-here"}, home).Check(context.Background())
	if !errors.Is(err, herdrapp.ErrUnavailable) || !strings.Contains(err.Error(), `backend = "herdr-tui"`) {
		t.Fatalf("real runner, missing binary: err = %v", err)
	}
}

func TestParkMakesMissing(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	binding := f.verify(t)
	owned := f.launch(t, "t2")

	state := reconcile(t, f, binding, owned, herdrapp.Pending, false)
	if state.State != herdrapp.Verified {
		t.Fatalf("before park: %+v", state)
	}
	f.fake.Park("t2")
	state = reconcile(t, f, binding, owned, state.State, false)
	if state.State != herdrapp.Missing || state.State.DispatchAllowed() {
		t.Fatalf("after park: %+v, want missing with dispatch held", state)
	}
	// The window closing does not turn missing back into pending.
	f.fake.Quit()
	state = reconcile(t, f, binding, owned, state.State, false)
	if state.State != herdrapp.Missing || !state.AppClosed {
		t.Fatalf("parked, window closed: %+v", state)
	}
}

func TestAppQuitIsAppClosedNotMissing(t *testing.T) {
	f := newFixture(t)
	f.fake.Open(f.core)
	binding := f.verify(t)
	owned := f.launch(t, "t2")

	state := reconcile(t, f, binding, owned, herdrapp.Pending, false)
	if state.State != herdrapp.Verified {
		t.Fatalf("open: %+v", state)
	}
	f.fake.Quit()
	for range 2 {
		state = reconcile(t, f, binding, owned, state.State, true)
		if state.State != herdrapp.Pending || !state.AppClosed || !state.State.DispatchAllowed() {
			t.Fatalf("quit: %+v, want pending with app_closed and dispatch allowed", state)
		}
	}
	f.fake.Open(f.core)
	state = reconcile(t, f, binding, owned, state.State, false)
	if state.State != herdrapp.Verified || state.AppClosed {
		t.Fatalf("reopened: %+v", state)
	}

	// Reopened on another core while closed: pending executions become
	// missing once the timeout after reopening passes.
	f.fake.Quit()
	state = reconcile(t, f, binding, owned, state.State, false)
	f.fake.Open(apptest.NewCore("core-b"))
	state = reconcile(t, f, binding, owned, state.State, false)
	if state.State != herdrapp.Pending {
		t.Fatalf("reopened elsewhere, within timeout: %+v", state)
	}
	state = reconcile(t, f, binding, owned, state.State, true)
	if state.State != herdrapp.Missing || state.AppClosed {
		t.Fatalf("reopened elsewhere, past timeout: %+v", state)
	}
}

// reconcile reads core and window once and applies the rules.
func reconcile(t *testing.T, f fixture, b herdrapp.Binding, owned herdrapp.Owned, prev herdrapp.Visibility, timedOut bool) herdrapp.Report {
	t.Helper()
	ctx := context.Background()
	read, err := f.core.TerminalLabels(ctx, owned.Terminal)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := f.app.Observe(ctx, owned)
	if err != nil {
		t.Fatal(err)
	}
	return herdrapp.Next(prev, herdrapp.Facts{
		BindingValid:   b.Valid(f.sel, f.home) == nil,
		LabelsReadBack: true,
		Observation:    obs,
		Parked:         herdrapp.Parked(read, owned.Placement),
		TimedOut:       timedOut,
	})
}

func TestNextTransitions(t *testing.T) {
	shown := herdrapp.Observation{PanePresent: true}
	closed := herdrapp.Observation{AppClosed: true}
	absent := herdrapp.Observation{}
	cases := []struct {
		name string
		prev herdrapp.Visibility
		f    herdrapp.Facts
		want herdrapp.Report
	}{
		{"no binding", herdrapp.Verified, herdrapp.Facts{LabelsReadBack: true, Observation: shown}, herdrapp.Report{State: herdrapp.Unverified}},
		{"labels written, not read back", herdrapp.Unverified, herdrapp.Facts{BindingValid: true, Observation: shown}, herdrapp.Report{State: herdrapp.Unverified}},
		{"read back, window closed", herdrapp.Unverified, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: closed}, herdrapp.Report{State: herdrapp.Pending, AppClosed: true}},
		{"read back, shown", herdrapp.Pending, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: shown}, herdrapp.Report{State: herdrapp.Verified}},
		{"not shown yet", herdrapp.Pending, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: absent}, herdrapp.Report{State: herdrapp.Pending}},
		{"not shown past timeout", herdrapp.Pending, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: absent, TimedOut: true}, herdrapp.Report{State: herdrapp.Missing}},
		{"shown, then gone", herdrapp.Verified, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: absent}, herdrapp.Report{State: herdrapp.Missing}},
		{"verified, window closed", herdrapp.Verified, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: closed}, herdrapp.Report{State: herdrapp.Pending, AppClosed: true}},
		{"parked", herdrapp.Verified, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: absent, Parked: true}, herdrapp.Report{State: herdrapp.Missing}},
		{"missing stays until shown", herdrapp.Missing, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: absent}, herdrapp.Report{State: herdrapp.Missing}},
		{"missing restored", herdrapp.Missing, herdrapp.Facts{BindingValid: true, LabelsReadBack: true, Observation: shown}, herdrapp.Report{State: herdrapp.Verified}},
	}
	for _, c := range cases {
		if got := herdrapp.Next(c.prev, c.f); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	for _, v := range []herdrapp.Visibility{herdrapp.Unverified, herdrapp.Missing} {
		if v.DispatchAllowed() {
			t.Errorf("%s allows dispatch", v)
		}
	}
}

func TestNewIDsAreFreshAndOpaque(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		ids := mustIDs(t)
		for _, id := range []string{ids.Space, ids.Tab} {
			if seen[id] || !strings.HasPrefix(id, "hs-") || len(id) != len("hs-")+32 {
				t.Fatalf("id %q repeated or malformed", id)
			}
			seen[id] = true
		}
	}
}
