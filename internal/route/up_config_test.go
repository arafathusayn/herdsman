package route_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/launch"
	"github.com/arafathusayn/herdsman/internal/route"
	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

func TestConfigLaunchBecomesASpec(t *testing.T) {
	spec, err := route.ConfigLaunch(&configv1.Launch{
		Command: []string{"codex", "--fast"}, Env: map[string]string{"B": "2"}, PreludeTimeout: proto.String("90s"),
		Prelude: []*configv1.LaunchStep{{
			Argv: []string{"make", "setup"}, Env: map[string]string{"Z": "1", "A": "2"},
			Probe: []string{"test", "-f", "ready"}, Retry: configv1.RetryPolicy_RETRY_POLICY_IDEMPOTENT,
		}, {Argv: []string{"true"}, Once: proto.Bool(true), Retry: configv1.RetryPolicy_RETRY_POLICY_NEVER}},
	}, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Cwd != "/repo" || spec.PreludeTimeout != 90*time.Second || spec.Command[1] != "--fast" || spec.Env["B"] != "2" {
		t.Fatalf("%+v", spec)
	}
	first := spec.Prelude[0]
	if first.Retry != launch.RetryIdempotent || first.Probe.Argv[0] != "test" || first.Command.Env[0].Name != "A" {
		t.Fatalf("%+v", first)
	}
	if !spec.Prelude[1].Once || spec.Prelude[1].Retry != launch.RetryNever || spec.Prelude[1].Probe != nil {
		t.Fatalf("%+v", spec.Prelude[1])
	}
}

func TestConfigLaunchPlacesARelativeFolderInTheRepository(t *testing.T) {
	spec, err := route.ConfigLaunch(&configv1.Launch{Command: []string{"codex"}, Cwd: proto.String("tools")}, "/repo")
	if err != nil || spec.Cwd != "/repo/tools" {
		t.Fatalf("%+v %v", spec, err)
	}
}

func TestConfigWithoutALaunchSectionIsNoSpec(t *testing.T) {
	for _, l := range []*configv1.Launch{nil, {}} {
		if spec, err := route.ConfigLaunch(l, "/repo"); err != nil || spec != nil {
			t.Fatalf("%+v %v", spec, err)
		}
	}
}

func TestConfigLaunchRefusesWhatCannotRun(t *testing.T) {
	cases := map[string]*configv1.Launch{
		"no command":  {Prelude: []*configv1.LaunchStep{{Argv: []string{"true"}}}},
		"bad timeout": {Command: []string{"codex"}, PreludeTimeout: proto.String("soon")},
		"empty step":  {Command: []string{"codex"}, Prelude: []*configv1.LaunchStep{{}}},
	}
	for name, l := range cases {
		if spec, err := route.ConfigLaunch(l, "/repo"); err == nil {
			t.Errorf("%s: %+v", name, spec)
		}
	}
}

func TestConfiguredLaunchFillsTheRoleWithoutACommand(t *testing.T) {
	r := newRig(t)
	req := r.request("claude", "")
	req.Launch = &launch.Spec{Command: []string{"codex"}, Cwd: r.repo, Prelude: []launch.Step{{
		Command: launch.Command{Argv: []string{"make", "setup"}}, Retry: launch.RetryIdempotent,
	}}}
	p := r.plan(req)
	if !strings.Contains(p.Text, "'make' 'setup'") {
		t.Fatalf("plan:\n%s", p.Text)
	}
	found := false
	for _, step := range p.Prelude {
		found = found || step.Retry == launch.RetryIdempotent
	}
	if !found {
		t.Fatalf("prelude %+v", p.Prelude)
	}
}

func TestACommandLineBeatsTheConfiguredLaunch(t *testing.T) {
	r := newRig(t)
	req := r.request("claude", "codex")
	req.Launch = &launch.Spec{Command: []string{"codex"}, Cwd: r.repo, Prelude: []launch.Step{{Command: launch.Command{Argv: []string{"make", "setup"}}}}}
	if p := r.plan(req); strings.Contains(p.Text, "'make' 'setup'") {
		t.Fatalf("plan:\n%s", p.Text)
	}
}

func TestOneLaunchSectionCannotFillBothRoles(t *testing.T) {
	r := newRig(t)
	req := r.request("", "")
	req.Launch = &launch.Spec{Command: []string{"codex"}, Cwd: r.repo}
	if _, err := route.PlanUp(req); err == nil || !strings.Contains(err.Error(), "one agent") {
		t.Fatalf("err = %v", err)
	}
}
