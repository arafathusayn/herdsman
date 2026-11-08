package automations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/sdk/config"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
)

func str(s string) *string { return &s }
func yes() *bool           { v := true; return &v }

func shellRule(event, run string) Rule { return Rule{Event: event, Kind: KindShell, Body: run} }

type rig struct {
	engine *Engine
	dir    string
	stderr *bytes.Buffer
}

func newRig(t *testing.T, rules ...Rule) *rig {
	t.Helper()
	dir := t.TempDir()
	stderr := &bytes.Buffer{}
	return &rig{dir: dir, stderr: stderr, engine: &Engine{
		Config: Config{Enabled: true, Rules: rules}, Approvals: OpenApprovals(filepath.Join(dir, "approvals.toml")),
		StateDir: filepath.Join(dir, "state"), Dir: dir, Stderr: stderr, Timeout: 20 * time.Second,
		Builtins: map[string]Builtin{},
	}}
}

func (r *rig) approve(t *testing.T, rule Rule) {
	t.Helper()
	if err := r.engine.Approvals.Approve("proj", rule); err != nil {
		t.Fatal(err)
	}
}

func event(kind, key string) Event {
	return Event{Key: key, Kind: kind, Project: "proj", Values: map[string]string{"task": "t1", "route": "r1"}}
}

func one(t *testing.T, outcomes []Outcome) Outcome {
	t.Helper()
	if len(outcomes) != 1 {
		t.Fatalf("outcomes: %+v", outcomes)
	}
	return outcomes[0]
}

func TestFromProtoReadsEachKindOfAction(t *testing.T) {
	cfg, err := FromProto(&configv1.Automations{Experimental: yes(), On: map[string]*configv1.AutomationAction{
		"task.done":    {Run: str("echo hi"), Timeout: str("90s")},
		"review.p0":    {Builtin: str("route pause")},
		"merge.landed": {Plugin: str("slack post")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || len(cfg.Rules) != 3 {
		t.Fatalf("%+v", cfg)
	}
	if cfg.Rules[0].Event != "merge.landed" || cfg.Rules[0].Kind != KindPlugin || cfg.Rules[1].Kind != KindBuiltin {
		t.Fatalf("rules are not in event order: %+v", cfg.Rules)
	}
	if cfg.Rules[2].Kind != KindShell || cfg.Rules[2].Timeout != 90*time.Second {
		t.Fatalf("%+v", cfg.Rules[2])
	}
}

func TestTheSectionLoadsFromAConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.toml")
	text := "[automations]\nexperimental = true\n[automations.on.\"review.p0\"]\nbuiltin = \"route pause\"\n[automations.on.\"task.done\"]\nrun = \"echo {{ task }}\"\ntimeout = \"90s\"\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := FromProto(loaded.Config.GetAutomations())
	if err != nil {
		t.Fatal(err)
	}
	want := []Rule{
		{Event: "review.p0", Kind: KindBuiltin, Body: BuiltinRoutePause},
		{Event: "task.done", Kind: KindShell, Body: "echo {{ task }}", Timeout: 90 * time.Second},
	}
	if !cfg.Enabled || !slices.Equal(cfg.Rules, want) {
		t.Fatalf("%+v", cfg)
	}
}

func TestFromProtoWithoutTheSectionIsOff(t *testing.T) {
	cfg, err := FromProto(nil)
	if err != nil || cfg.Enabled || len(cfg.Rules) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestFromProtoRefusesBadActions(t *testing.T) {
	for name, action := range map[string]*configv1.AutomationAction{
		"none":            {},
		"two":             {Run: str("a"), Builtin: str("route pause")},
		"empty run":       {Run: str(" ")},
		"nil":             nil,
		"unknown builtin": {Builtin: str("route explode")},
		"plugin shape":    {Plugin: str("only-a-name")},
		"bad timeout":     {Run: str("a"), Timeout: str("soon")},
		"zero timeout":    {Run: str("a"), Timeout: str("0s")},
		"filter":          {Run: str("echo {{ task | sanitize }}")},
	} {
		if _, err := FromProto(&configv1.Automations{On: map[string]*configv1.AutomationAction{"task.done": action}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := FromProto(&configv1.Automations{On: map[string]*configv1.AutomationAction{"": {Run: str("a")}}}); err == nil {
		t.Error("an empty event kind was accepted")
	}
}

func TestChecksumChangesWithEveryPartOfTheRule(t *testing.T) {
	base := shellRule("task.done", "echo a")
	seen := map[string]bool{Checksum(base): true}
	for _, other := range []Rule{
		shellRule("task.done", "echo b"),
		shellRule("merge.landed", "echo a"),
		{Event: "task.done", Kind: KindPlugin, Body: "echo a"},
		// A separator inside a field must not make two rules collide.
		shellRule("task.done\x00shell", "echo a"),
	} {
		if seen[Checksum(other)] {
			t.Errorf("collision for %+v", other)
		}
		seen[Checksum(other)] = true
	}
	withTimeout := base
	withTimeout.Timeout = time.Minute
	if Checksum(withTimeout) != Checksum(base) {
		t.Error("a timeout is not part of what the action does")
	}
	if !strings.HasPrefix(Checksum(base), "sha256:") {
		t.Error(Checksum(base))
	}
}

func TestUnapprovedTemplateNeverRuns(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusUnapproved || got.Checksum != Checksum(rule) {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the template ran without its approval")
	}
}

func TestAnEditedTemplateNeedsANewApproval(t *testing.T) {
	approved := shellRule("task.done", "true")
	edited := shellRule("task.done", "touch ran")
	r := newRig(t, edited)
	r.approve(t, approved)
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusUnapproved {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("an edited template ran on the old approval")
	}
}

func TestApprovalIsPerProject(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	ev := event("task.done", "k1")
	ev.Project = "other"
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusUnapproved {
		t.Fatalf("%+v", got)
	}
	ev.Project = ""
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusUnapproved {
		t.Fatalf("no project: %+v", got)
	}
}

func TestABrokenApprovalStoreFailsClosed(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	if err := os.WriteFile(filepath.Join(r.dir, "approvals.toml"), []byte("not = [toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusError || got.Error == "" {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran without a readable approval")
	}
}

func TestNoApprovalStoreFailsClosed(t *testing.T) {
	r := newRig(t, shellRule("task.done", "touch ran"))
	r.engine.Approvals = nil
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
}

func TestApprovedTemplateRunsWithLogs(t *testing.T) {
	rule := shellRule("task.done", `echo "$HERDSMAN_EVENT" {{ task }} > out; echo logged`)
	r := newRig(t, rule)
	r.approve(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusRan || got.Exit != 0 {
		t.Fatalf("%+v", got)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, "out"))
	if err != nil || string(data) != "task.done t1\n" {
		t.Fatalf("%q %v", data, err)
	}
	log, err := os.ReadFile(got.Log)
	if err != nil || string(log) != "logged\n" {
		t.Fatalf("%q %v", log, err)
	}
	record, err := os.ReadFile(filepath.Join(r.dir, "state", "automations", "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status":"ran"`, `"experimental":"automations/` + Version, Checksum(rule)} {
		if !strings.Contains(string(record), want) {
			t.Errorf("log lacks %s: %s", want, record)
		}
	}
}

func TestEventValuesCannotBecomeCommands(t *testing.T) {
	for name, tmpl := range map[string]string{
		"bare":          `printf %s {{ task }} > out`,
		"double quoted": `printf %s "{{ task }}" > out`,
		"single quoted": `printf %s '{{ task }}' > out`,
	} {
		t.Run(name, func(t *testing.T) {
			rule := shellRule("task.done", tmpl)
			r := newRig(t, rule)
			r.approve(t, rule)
			ev := event("task.done", "k1")
			ev.Values["task"] = `x; touch pwned1 $(touch pwned2) "$(touch pwned3)" '; touch pwned4; '`
			got := one(t, r.engine.Fire(context.Background(), ev))
			if got.Status != StatusRan {
				t.Fatalf("%+v", got)
			}
			matches, err := filepath.Glob(filepath.Join(r.dir, "pwned*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("an event value ran as a command: %v %v", matches, err)
			}
			if strings.Contains(got.Rendered, "touch") {
				t.Fatalf("the value reached the command text: %s", got.Rendered)
			}
		})
	}
}

func TestEventValueReachesTheCommandIntact(t *testing.T) {
	rule := shellRule("task.done", `printf %s {{ task }} > out`)
	r := newRig(t, rule)
	r.approve(t, rule)
	ev := event("task.done", "k1")
	ev.Values["task"] = "two  words"
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(r.dir, "out")); string(data) != "two  words" {
		t.Fatalf("%q", data)
	}
}

func TestAMissingVariableIsReportedAndNothingRuns(t *testing.T) {
	rule := shellRule("task.done", `touch ran {{ branch }}`)
	r := newRig(t, rule)
	r.approve(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusError || !strings.Contains(got.Error, "branch") {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran")
	}
}

func TestDryRunReportsAndChangesNothing(t *testing.T) {
	approved := shellRule("task.done", `touch ran {{ task }}`)
	r := newRig(t, approved)
	r.approve(t, approved)
	r.engine.DryRun = true
	for range 2 {
		got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
		if got.Status != StatusWouldRun || !strings.Contains(got.Rendered, "touch ran") {
			t.Fatalf("%+v", got)
		}
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("a dry run ran the template")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "state")); err == nil {
		t.Fatal("a dry run wrote state")
	}
	r.engine.DryRun = false
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusRan {
		t.Fatalf("a dry run used up the delivery: %+v", got)
	}
}

func TestDryRunStillSaysUnapproved(t *testing.T) {
	r := newRig(t, shellRule("task.done", "true"))
	r.engine.DryRun = true
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusUnapproved {
		t.Fatalf("%+v", got)
	}
}

func TestARepeatedDeliveryRunsOnce(t *testing.T) {
	rule := shellRule("task.done", `echo x >> out`)
	r := newRig(t, rule)
	r.approve(t, rule)
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	restarted := &Engine{Config: r.engine.Config, Approvals: r.engine.Approvals, StateDir: r.engine.StateDir, Dir: r.dir, Stderr: r.stderr}
	if got := one(t, restarted.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusDuplicate {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(r.dir, "out")); string(data) != "x\n" {
		t.Fatalf("%q", data)
	}
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k2"))); got.Status != StatusRan {
		t.Fatalf("a new event: %+v", got)
	}
}

func TestPolicyDenyStopsAnApprovedTemplate(t *testing.T) {
	rule := shellRule("task.done", `git push origin main`)
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Policy = policy.Policy{Deny: []string{"git push"}}
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusDenied || !strings.Contains(got.Error, "deny") {
		t.Fatalf("%+v", got)
	}
}

func TestPolicyRefusesAValueInCommandPosition(t *testing.T) {
	rule := shellRule("task.done", `{{ task }} --now`)
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Policy = policy.Policy{Deny: []string{"rm"}}
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusDenied {
		t.Fatalf("%+v", got)
	}
}

func TestDisabledSectionDoesNothingAndPrintsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Config.Enabled = false
	if got := r.engine.Fire(context.Background(), event("task.done", "k1")); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if r.stderr.Len() != 0 {
		t.Fatal(r.stderr.String())
	}
}

func TestExperimentalMarkerPrintsOncePerRun(t *testing.T) {
	rule := shellRule("task.done", "true")
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Fire(context.Background(), event("task.done", "k1"))
	r.engine.Fire(context.Background(), event("task.done", "k2"))
	r.engine.Fire(context.Background(), event("other.kind", "k3"))
	if n := strings.Count(r.stderr.String(), "experimental"); n != 1 {
		t.Fatalf("printed %d times: %s", n, r.stderr)
	}
	if !strings.Contains(r.stderr.String(), "automations/"+Version) {
		t.Fatal(r.stderr.String())
	}
}

func TestAnEventWithNoRuleDoesNothing(t *testing.T) {
	r := newRig(t, shellRule("task.done", "true"))
	if got := r.engine.Fire(context.Background(), event("merge.landed", "k1")); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAFailingTemplateIsRecorded(t *testing.T) {
	rule := shellRule("task.done", "echo oops; exit 7")
	r := newRig(t, rule)
	r.approve(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusFailed || got.Exit != 7 {
		t.Fatalf("%+v", got)
	}
}

func TestATemplateIsStoppedAtItsTimeout(t *testing.T) {
	rule := shellRule("task.done", "sleep 30")
	rule.Timeout = 100 * time.Millisecond
	r := newRig(t, rule)
	r.approve(t, rule)
	start := time.Now()
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	if got.Status != StatusFailed || !got.TimedOut || time.Since(start) > 10*time.Second {
		t.Fatalf("%+v after %s", got, time.Since(start))
	}
}

func TestABuiltinNeedsApprovalAndThenRuns(t *testing.T) {
	rule := Rule{Event: "review.p0", Kind: KindBuiltin, Body: BuiltinRoutePause}
	r := newRig(t, rule)
	var routes []string
	r.engine.Builtins[BuiltinRoutePause] = func(_ context.Context, ev Event) error {
		routes = append(routes, ev.Values["route"])
		return nil
	}
	if got := one(t, r.engine.Fire(context.Background(), event("review.p0", "k1"))); got.Status != StatusUnapproved || routes != nil {
		t.Fatalf("%+v %v", got, routes)
	}
	r.approve(t, rule)
	if got := one(t, r.engine.Fire(context.Background(), event("review.p0", "k2"))); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	if len(routes) != 1 || routes[0] != "r1" {
		t.Fatal(routes)
	}
}

func TestABuiltinWithNoImplementationIsAnError(t *testing.T) {
	rule := Rule{Event: "task.done", Kind: KindBuiltin, Body: BuiltinTaskStart}
	r := newRig(t, rule)
	r.approve(t, rule)
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusUnavailable {
		t.Fatalf("%+v", got)
	}
}

func TestAFailingBuiltinIsRecorded(t *testing.T) {
	rule := Rule{Event: "review.p0", Kind: KindBuiltin, Body: BuiltinRoutePause}
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Builtins[BuiltinRoutePause] = func(context.Context, Event) error { return errors.New("no herdr") }
	if got := one(t, r.engine.Fire(context.Background(), event("review.p0", "k1"))); got.Status != StatusFailed || got.Error != "no herdr" {
		t.Fatalf("%+v", got)
	}
}

func TestAPluginActionWaitsForThePluginHost(t *testing.T) {
	rule := Rule{Event: "merge.landed", Kind: KindPlugin, Body: "slack post"}
	r := newRig(t, rule)
	r.approve(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("merge.landed", "k1")))
	if got.Status != StatusUnavailable || !strings.Contains(got.Error, "plugin") {
		t.Fatalf("%+v", got)
	}
}

func TestAPluginActionUsesItsPort(t *testing.T) {
	rule := Rule{Event: "merge.landed", Kind: KindPlugin, Body: "slack post"}
	r := newRig(t, rule)
	var called string
	r.engine.Plugins = pluginFunc(func(_ context.Context, plugin, action string, _ Event) error {
		called = plugin + "/" + action
		return nil
	})
	if got := one(t, r.engine.Fire(context.Background(), event("merge.landed", "k1"))); got.Status != StatusUnapproved || called != "" {
		t.Fatalf("%+v %s", got, called)
	}
	r.approve(t, rule)
	if got := one(t, r.engine.Fire(context.Background(), event("merge.landed", "k2"))); got.Status != StatusRan || called != "slack/post" {
		t.Fatalf("%+v %s", got, called)
	}
}

type pluginFunc func(ctx context.Context, plugin, action string, ev Event) error

func (f pluginFunc) RunAction(ctx context.Context, plugin, action string, ev Event) error {
	return f(ctx, plugin, action, ev)
}

func TestLogFilesAreOwnerOnly(t *testing.T) {
	rule := shellRule("task.done", "echo secret")
	r := newRig(t, rule)
	r.approve(t, rule)
	got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1")))
	for _, path := range []string{got.Log, filepath.Join(r.dir, "state", "automations", "log.jsonl")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", path, info.Mode(), err)
		}
	}
}

func TestAnUnwritableStateDirRunsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	blocker := filepath.Join(r.dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.engine.StateDir = blocker
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran without a claim")
	}
}

func TestAnEventWithoutAKeyRunsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", ""))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran with nothing to deduplicate by")
	}
}

func TestDefaultApprovalsNeedsAHome(t *testing.T) {
	if _, err := DefaultApprovals(nil); err == nil {
		t.Fatal("no error without HOME")
	}
	store, err := DefaultApprovals([]string{"HOME=" + t.TempDir()})
	if err != nil || store == nil {
		t.Fatal(err)
	}
}

func TestAnEventWithNoValuesStillRuns(t *testing.T) {
	rule := shellRule("task.done", `printf %s {{ hook_type }} > out`)
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Timeout = 0
	ev := Event{Key: "k1", Kind: "task.done", Project: "proj"}
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(r.dir, "out")); string(data) != "task.done" {
		t.Fatalf("%q", data)
	}
}

func TestAValueWithABadNameIsDropped(t *testing.T) {
	rule := shellRule("task.done", `env > out`)
	r := newRig(t, rule)
	r.approve(t, rule)
	ev := event("task.done", "k1")
	ev.Values["BAD=name"] = "x"
	if got := one(t, r.engine.Fire(context.Background(), ev)); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(r.dir, "out"))
	if strings.Contains(string(data), "BAD=NAME") || !strings.Contains(string(data), "HERDSMAN_EVENT_TASK=t1") {
		t.Fatalf("%s", data)
	}
}

func TestApprovingNeedsAProject(t *testing.T) {
	if err := OpenApprovals(filepath.Join(t.TempDir(), "a.toml")).Approve("", shellRule("a", "b")); err == nil {
		t.Fatal("approved for no project")
	}
}

func TestAShellThatCannotStartIsFailed(t *testing.T) {
	rule := shellRule("task.done", "true")
	r := newRig(t, rule)
	r.approve(t, rule)
	r.engine.Dir = filepath.Join(r.dir, "absent")
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusFailed || got.Error == "" {
		t.Fatalf("%+v", got)
	}
}

func readOnlyDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
}

func TestAClaimThatCannotBeWrittenRunsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	readOnlyDir(t, filepath.Join(r.dir, "state", "automations", "fired"))
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran without a claim")
	}
}

func TestARunLogThatCannotBeOpenedRunsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	readOnlyDir(t, filepath.Join(r.dir, "state", "automations", "runs"))
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "ran")); err == nil {
		t.Fatal("ran without a log")
	}
}

func TestARunsFolderThatCannotBeMadeRunsNothing(t *testing.T) {
	rule := shellRule("task.done", "touch ran")
	r := newRig(t, rule)
	r.approve(t, rule)
	folder := filepath.Join(r.dir, "state", "automations")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "runs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusError {
		t.Fatalf("%+v", got)
	}
}

func TestALogThatCannotBeWrittenIsSaid(t *testing.T) {
	rule := shellRule("task.done", "true")
	r := newRig(t, rule)
	r.approve(t, rule)
	if err := os.MkdirAll(filepath.Join(r.dir, "state", "automations", "log.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := one(t, r.engine.Fire(context.Background(), event("task.done", "k1"))); got.Status != StatusRan {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(r.stderr.String(), "automations log") {
		t.Fatal(r.stderr.String())
	}
}
