package route_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/policy"
	"github.com/arafathusayn/herdsman/internal/route"
)

const restrictedSecret = "sk-route-Secret-value"

type routeKeychain map[string]string

func (k routeKeychain) Load(service, account string) (string, error) {
	v, ok := k[service+"/"+account]
	if !ok {
		return "", os.ErrNotExist
	}
	return v, nil
}

func restrictedRequest(r *rig) route.UpRequest {
	req := r.request("", "codex")
	req.NoAdjutant = true
	req.Policy = policy.Policy{Profile: policy.ProfileRestricted}
	return req
}

// realRepo turns the rig's folder into a repository a clone can copy.
func realRepo(t *testing.T, r *rig) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = r.repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func (r *rig) agentArgs(t *testing.T, name string) []string {
	t.Helper()
	for _, q := range r.srv.Requests() {
		if q.Method != "agent.start" || q.Params["name"] != name {
			continue
		}
		var args []string
		for _, a := range q.Params["args"].([]any) {
			args = append(args, a.(string))
		}
		return args
	}
	t.Fatalf("no agent.start for %s", name)
	return nil
}

func TestRestrictedRefusesAnAutomaticPair(t *testing.T) {
	r := newRig(t)
	req := r.request("codex", "codex")
	req.Policy = policy.Policy{Profile: policy.ProfileRestricted}
	if _, err := route.PlanUp(req); err == nil || !strings.Contains(err.Error(), "--pair manual") {
		t.Fatalf("err = %v", err)
	}
	req.PairManual = true
	if _, err := route.PlanUp(req); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedLocalKeepsTheAutomaticPair(t *testing.T) {
	r := newRig(t)
	if _, err := route.PlanUp(r.request("codex", "claude")); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedRefusesAHarnessItCannotConfine(t *testing.T) {
	r := newRig(t)
	req := restrictedRequest(r)
	req.OrchestratorCommand = "claude"
	if _, err := route.PlanUp(req); err == nil || !strings.Contains(err.Error(), "confine") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestrictedRefusesFlagsThatLiftTheSandbox(t *testing.T) {
	r := newRig(t)
	for _, line := range []string{
		"codex --dangerously-bypass-approvals-and-sandbox",
		"codex --sandbox danger-full-access",
		"codex -c sandbox_mode=danger-full-access",
		"codex --add-dir /etc",
	} {
		req := restrictedRequest(r)
		req.OrchestratorCommand = line
		if _, err := route.PlanUp(req); err == nil || !strings.Contains(err.Error(), "restricted") {
			t.Errorf("%s: err = %v", line, err)
		}
	}
}

func TestRestrictedStartClonesAndConfinesTheAgent(t *testing.T) {
	r := newRig(t)
	realRepo(t, r)
	p := r.plan(restrictedRequest(r))
	if !strings.Contains(p.Text, "profile: restricted") {
		t.Errorf("plan:\n%s", p.Text)
	}
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	clone := filepath.Join(filepath.Dir(r.repo), "proj.worktrees", "harbor-orchestrator")
	if _, err := os.Stat(filepath.Join(clone, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("alternates: %v", err)
	}
	args := strings.Join(r.agentArgs(t, p.OrchestratorName), " ")
	for _, want := range []string{"--cd " + clone, `sandbox_mode="workspace-write"`, filepath.Join(clone, ".git"), p.Folder} {
		if !strings.Contains(args, want) {
			t.Errorf("want %q in %s", want, args)
		}
	}
}

func TestRestrictedCloneIsNotRepeated(t *testing.T) {
	r := newRig(t)
	realRepo(t, r)
	p := r.plan(restrictedRequest(r))
	for i, s := range p.Spec.Steps {
		if s.Key == "clone:orchestrator" {
			r.crashOn = fmt.Sprintf("step:%d:applied", i)
		}
	}
	if r.crashOn == "" {
		t.Fatal("no clone step")
	}
	e, done := r.engine()
	crashDrive(t, e, p.Spec)
	done()
	e, done = r.engine()
	defer done()
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, err := e.Submit(context.Background(), p.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if n := r.world.Starts(p.OrchestratorName); n != 1 {
		t.Fatalf("agent started %d times", n)
	}
}

func TestRestrictedProtectedPathStopsTheStart(t *testing.T) {
	r := newRig(t)
	realRepo(t, r)
	req := restrictedRequest(r)
	req.Protected = []string{filepath.Join(filepath.Dir(r.repo), "proj.worktrees", "harbor-orchestrator", "x")}
	e, done := r.engine()
	defer done()
	op := drive(t, e, r.plan(req).Spec)
	if op.State == ops.OpDone || r.world.Starts(r.plan(req).OrchestratorName) != 0 {
		t.Fatalf("state %s, started", op.State)
	}
}

func TestSecretReachesThePaneButNeverTheJournal(t *testing.T) {
	r := newRig(t)
	r.keychain = routeKeychain{"svc/acct": restrictedSecret}
	req := r.request("", "codex")
	req.NoAdjutant = true
	req.SecretRefs = "TOKEN=svc/acct"
	p := r.plan(req)
	if strings.Contains(p.Text, restrictedSecret) {
		t.Fatal("the printed start holds the value")
	}
	e, done := r.engine()
	defer done()
	op := drive(t, e, p.Spec)
	if op.State != ops.OpDone {
		t.Fatalf("state %s: %s", op.State, op.Reason)
	}
	if got := r.world.Env(r.world.PaneByLabel("orchestrator"))["TOKEN"]; got != restrictedSecret {
		t.Fatalf("env = %q", got)
	}
	for _, s := range op.Steps {
		if strings.Contains(s.Spec, restrictedSecret) {
			t.Fatalf("journal step %s holds the value", s.Key)
		}
	}
}

func TestUnreadableSecretStopsTheStart(t *testing.T) {
	r := newRig(t)
	r.keychain = routeKeychain{}
	req := r.request("", "codex")
	req.NoAdjutant = true
	req.SecretRefs = "TOKEN=svc/acct"
	e, done := r.engine()
	defer done()
	p := r.plan(req)
	if op := drive(t, e, p.Spec); op.State == ops.OpDone || r.world.Starts(p.OrchestratorName) != 0 {
		t.Fatalf("state %s", op.State)
	}
}

func TestBadSecretReferencesAreRefused(t *testing.T) {
	r := newRig(t)
	req := r.request("", "codex")
	req.NoAdjutant = true
	req.SecretRefs = "broken"
	if _, err := route.PlanUp(req); err == nil {
		t.Fatal("accepted")
	}
}
