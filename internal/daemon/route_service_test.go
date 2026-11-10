package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/ops"
	"github.com/arafathusayn/herdsman/internal/route"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

func routeClient(t *testing.T, h *harness) *client.Client {
	t.Helper()
	c, err := client.Dial(client.Options{Socket: SocketPath(h.ph)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func codeOf(err error) rpc.Code {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func upRequest(repo string) *daemonv1.UpRequest {
	return &daemonv1.UpRequest{
		Route: &corev1.RouteId{Value: "harbor"}, Repo: repo, Prompt: "ship it",
		AdjutantCommand: "claude doctor && claude", OrchestratorCommand: "codex",
		Kinds: []string{"claude", "codex"}, Date: "2026-11-09", DryRun: true,
	}
}

func TestUpDryRunPlansWithoutJournaling(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	res, err := c.Route.Up(ctx, upRequest(repo))
	if err != nil {
		t.Fatal(err)
	}
	if res.GetStart().GetState() != "planned" || !strings.Contains(res.GetStart().GetPlan(), "claude doctor") {
		t.Fatalf("%+v", res.GetStart())
	}
	if res.GetStart().GetWorkspace() != "proj-harbor" {
		t.Fatalf("workspace %q", res.GetStart().GetWorkspace())
	}
	list, err := c.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 0 {
		t.Fatalf("a dry run was journaled: %v %v", list, err)
	}
}

func TestUpRefusesABadRequest(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	if _, err := c.Route.Up(ctx, &daemonv1.UpRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("no route: %v", err)
	}
	q := upRequest(filepath.Join(h.ph, "proj"))
	q.AdjutantCommand = "claude; rm -rf x"
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("refused line: %v", err)
	}
	q = upRequest(filepath.Join(h.ph, "proj"))
	q.DryRun, q.Continue = false, true
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("continue with no start: %v", err)
	}
}

func TestUpRefusesACallerThatIsNotThePeer(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	q := upRequest(filepath.Join(h.ph, "proj"))
	q.DryRun = false
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid()) + 1, Tty: "forged"}
	if _, err := c.Route.Up(context.Background(), q); codeOf(err) != rpc.PermissionDenied {
		t.Fatalf("got %v", err)
	}
}

func TestVerifiedCallerIgnoresClaimedFacts(t *testing.T) {
	inspect := func(pid int) (procFacts, error) {
		switch pid {
		case 10:
			return procFacts{ppid: 20, tty: "ttys9", command: "herdsman up"}, nil
		case 20:
			return procFacts{ppid: 1, tty: "ttys9", command: "/opt/bin/claude --model x"}, nil
		}
		return procFacts{ppid: 1}, nil
	}
	facts := inspectCaller(10, inspect)
	if facts.tty != "/dev/ttys9" || facts.harness != "claude" {
		t.Fatalf("%+v", facts)
	}
	none := func(int) (procFacts, error) { return procFacts{ppid: 1, tty: "??", command: "sshd"}, nil }
	if facts = inspectCaller(10, none); facts != (callerFacts{}) {
		t.Fatalf("%+v", facts)
	}
	node := func(int) (procFacts, error) {
		return procFacts{ppid: 1, tty: "ttys1", command: "node /usr/lib/node_modules/.bin/codex"}, nil
	}
	if facts = inspectCaller(10, node); facts.harness != "codex" {
		t.Fatalf("a script harness was missed: %+v", facts)
	}
}

// herdr-core starts a terminal's program under its holder, with no agent above
// it, whoever asked. The holder and core command lines are the ones herdr-core
// 0.1.19 shows in the process table: under the name hc when hc started the
// core, and under the name herdr-core when the Herdr app did.
func TestInspectCallerFindsACommandStartedThroughHerdrCore(t *testing.T) {
	const (
		holder    = "/opt/bin/hc hold --socket /run/herdr-core/default.holder.3cd8168b.sock"
		core      = "/opt/bin/hc --hc-launchd /tmp/hc-launchd-1/ready"
		appHolder = "/opt/bin/herdr-core hold --socket /state/herdr-core/default/default.holder.8f6529c6.sock"
		appCore   = "/opt/bin/herdr-core serve"
		tty       = "/dev/ttys26"
	)
	// chain makes a process table from command lines: the caller is process
	// 10, and each row's parent is the row after it.
	chain := func(rows []string) func(int) (procFacts, error) {
		return func(pid int) (procFacts, error) {
			i := pid - 10
			if i < 0 || i >= len(rows) {
				return procFacts{}, errors.New("no such process")
			}
			ppid := pid + 1
			if i == len(rows)-1 {
				ppid = 1
			}
			return procFacts{ppid: ppid, tty: "ttys26", command: rows[i]}, nil
		}
	}
	cases := []struct {
		name string
		rows []string
		want callerFacts
	}{
		{"up is the terminal's program", []string{"herdsman up r -p x", holder, core}, callerFacts{tty: tty, coreCommand: "herdsman"}},
		{"a command string under sh", []string{"herdsman up r", "/bin/sh -c herdsman up r", holder, core}, callerFacts{tty: tty, coreCommand: "sh"}},
		{"a script", []string{"herdsman up r", "/bin/bash /tmp/start-route.sh", holder, core}, callerFacts{tty: tty, coreCommand: "bash"}},
		{"typed in the login shell", []string{"herdsman up r", "/bin/zsh -l", holder, core}, callerFacts{tty: tty}},
		{"typed in a login shell named with a dash", []string{"herdsman up r", "-zsh", holder, core}, callerFacts{tty: tty}},
		{"run by a tool a person started in the shell", []string{"herdsman up r", "make up", "/bin/zsh -l", holder, core}, callerFacts{tty: tty}},
		{"an agent in a core terminal", []string{"herdsman up r", "/opt/bin/claude --model x", "/bin/zsh -l", holder, core}, callerFacts{tty: tty, harness: "claude"}},
		{"outside herdr-core", []string{"herdsman up r", "-zsh", "/usr/bin/login -fp someone"}, callerFacts{tty: tty}},
		{"up is the program, the core named herdr-core", []string{"herdsman up r -p x", appHolder, appCore}, callerFacts{tty: tty, coreCommand: "herdsman"}},
		{"typed in a shell, the core named herdr-core", []string{"herdsman up r", "/bin/zsh -l", appHolder, appCore}, callerFacts{tty: tty}},
		{"a holder with other arguments", []string{"herdsman up r", "/opt/bin/hc keep-terminals --socket /run/x.sock", core}, callerFacts{tty: tty, coreCommand: "herdsman"}},
		{"the program is a child of the server", []string{"herdsman up r", appCore}, callerFacts{tty: tty, coreCommand: "herdsman"}},
		{"a shell under a herdr-core command, in a terminal started with a script", []string{"herdsman up r", "/bin/zsh -l", "/opt/bin/hc attach t1", "/bin/bash /tmp/start-route.sh", holder, core}, callerFacts{tty: tty, coreCommand: "bash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inspectCaller(10, chain(tc.rows)); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLatestStartIsTheNewest(t *testing.T) {
	starts := []*ops.Operation{{ID: "02B"}, {ID: "01A"}, {ID: "03C"}}
	sortStarts(starts)
	if starts[len(starts)-1].ID != "03C" || starts[0].ID != "01A" {
		t.Fatalf("%v %v %v", starts[0].ID, starts[1].ID, starts[2].ID)
	}
	// Started in the same millisecond, an earlier start can hold the larger id.
	starts = []*ops.Operation{{ID: "09Z", Key: "up:r:n"}, {ID: "01A", Key: "up:r:n#2"}}
	sortStarts(starts)
	if starts[1].ID != "01A" {
		t.Fatalf("the numbered start is not the newest: %v", starts[1].ID)
	}
}

func TestGetNamesAnUnknownRoute(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	_, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: &corev1.RouteId{Value: "nope"}, Repo: "/x"})
	if codeOf(err) != rpc.NotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("empty: %v", err)
	}
}

func writeLaunchConfig(t *testing.T, dir, text string) string {
	t.Helper()
	path := filepath.Join(dir, "route.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpPlansTheConfiguredLaunch(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	req := upRequest(filepath.Join(h.ph, "proj"))
	req.OrchestratorCommand = ""
	req.ConfigPath = writeLaunchConfig(t, h.dir, "[launch]\ncommand = [\"codex\"]\nprelude_timeout = \"90s\"\n[[launch.prelude]]\nargv = [\"make\", \"setup\"]\nretry = \"idempotent\"\n")
	res, err := c.Route.Up(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.GetStart().GetPlan(), "'make' 'setup'") {
		t.Fatalf("plan:\n%s", res.GetStart().GetPlan())
	}
}

func TestUpRefusesABadConfigBeforeAnyEffect(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	req := upRequest(filepath.Join(h.ph, "proj"))
	req.DryRun = false
	req.OrchestratorCommand = ""
	req.ConfigPath = writeLaunchConfig(t, h.dir, "[launch]\ncommand = [\"codex\"]\nprelude_timeout = \"soon\"\n")
	if _, err := c.Route.Up(ctx, req); err == nil || !strings.Contains(err.Error(), "soon") {
		t.Fatalf("err = %v", err)
	}
	list, err := c.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 0 {
		t.Fatalf("journaled: %v %v", list, err)
	}
}

func writeRestrictedConfig(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[schema]\nversion = \"1.0.0\"\n[policy]\nprofile = \"restricted\"\n"
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpReadsTheProfileFromTheRepositoryConfig(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	repo := filepath.Join(h.ph, "proj")
	writeRestrictedConfig(t, repo)
	q := upRequest(repo)
	q.AdjutantCommand = "codex"
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.InvalidArgument || !strings.Contains(err.Error(), "--pair manual") {
		t.Fatalf("automatic pair: %v", err)
	}
	q.PairManual = true
	res, err := c.Route.Up(ctx, q)
	if err != nil || !strings.Contains(res.GetStart().GetPlan(), "profile: restricted") {
		t.Fatalf("%v %v", res, err)
	}
}

func TestUpRefusesAnUnreadableConfig(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte("[broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(context.Background(), upRequest(repo)); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("err = %v", err)
	}
}

func TestUpRefusesAProfileNothingEnforces(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo := filepath.Join(h.ph, "proj")
	writeRestrictedConfig(t, repo)
	body := "[schema]\nversion = \"1.0.0\"\n[policy]\nprofile = \"open\"\n"
	if err := os.WriteFile(filepath.Join(repo, "herdsman.config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(context.Background(), upRequest(repo)); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("err = %v", err)
	}
}

// upNoAdjutant starts a route with one agent through the service, on a fake
// herdr layout, and waits for it to finish.
func upNoAdjutant(t *testing.T, h *harness, c *client.Client) (string, *herdrtest.World) {
	t.Helper()
	world := herdrtest.ServeWorld(h.fx)
	repo := filepath.Join(h.ph, "proj")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := upRequest(repo)
	q.DryRun, q.NoAdjutant, q.AdjutantCommand = false, true, ""
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Up(ctx, q); err != nil {
		t.Fatal(err)
	}
	get := &daemonv1.RouteServiceGetRequest{Route: q.GetRoute(), Repo: repo}
	for range 500 {
		res, err := c.Route.Get(ctx, get)
		if err != nil {
			t.Fatal(err)
		}
		switch res.GetStart().GetState() {
		case "done":
			return repo, world
		case "running":
			time.Sleep(10 * time.Millisecond)
		default:
			t.Fatalf("up: %s: %s", res.GetStart().GetState(), res.GetStart().GetReason())
		}
	}
	t.Fatal("up did not finish")
	return "", nil
}

func TestEachVerbRepeatedChangesNothing(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, world := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	pause := func() *daemonv1.RouteControl {
		res, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetResult()
	}
	if got := pause(); got.GetState() != "done" || got.GetControl() != "paused" || got.GetOperation() == "" {
		t.Fatalf("%+v", got)
	}
	h.srv.ForgetRequests()
	if got := pause(); got.GetState() != "unchanged" || got.GetControl() != "paused" {
		t.Fatalf("%+v", got)
	}
	if n := len(h.srv.Requests()); n != 0 {
		t.Fatalf("a repeated pause reached herdr %d times", n)
	}
	res, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil || res.GetStart().GetControl() != "paused" {
		t.Fatalf("%v %v", res, err)
	}
	stop := func() *daemonv1.RouteControl {
		res, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: id, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetResult()
	}
	if got := stop(); got.GetState() != "done" || got.GetControl() != "stopped" {
		t.Fatalf("%+v", got)
	}
	if got := stop(); got.GetState() != "unchanged" {
		t.Fatalf("%+v", got)
	}
	if _, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("pause of a stopped route: %v", err)
	}
	if world.PaneByLabel("orchestrator") != "" {
		t.Fatal("stop left the orchestrator pane")
	}
}

// fakeRecovery stands in for the recovery pipeline; the snapshot's verification
// itself is proven in package route with real snapshots.
type fakeRecovery struct{}

func (fakeRecovery) Capture(_ context.Context, q route.SnapshotRequest) error {
	return os.MkdirAll(q.Dest, 0o700)
}

func (fakeRecovery) Verify(_ context.Context, dest string, _ []route.SnapshotTarget) error {
	_, err := os.Stat(dest)
	return err
}

// withRecovery restarts the daemon with a recovery backend and a down step.
func withRecovery(t *testing.T, h *harness) *client.Client {
	t.Helper()
	h.stop()
	h.cfg.Recovery = fakeRecovery{}
	h.cfg.RouteDown = func(context.Context, string, string) error { return nil }
	h.start()
	return routeClient(t, h)
}

// The daemon's own backend makes a delete that its own undo reverses.
func TestDeleteThroughTheServiceIsUndoneByUndo(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	git(t, repo, "init", "-q")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "base")
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	got, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(got.GetStart().GetFolder(), "note")
	if err := os.WriteFile(note, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true, KeepWorktrees: true})
	if err != nil || res.GetResult().GetState() != "done" {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(note); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the route folder stayed: %v", err)
	}
	if _, err := dialOps(t, h.ph).Undo(ctx, &daemonv1.UndoRequest{Snapshot: res.GetResult().GetOperation()}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(note); err != nil || string(raw) != "keep me" {
		t.Fatalf("%q %v", raw, err)
	}
}

func TestDeleteNeedsDownUnlessWorktreesAreKept(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	id := &corev1.RouteId{Value: "harbor"}
	_, err := c.Route.Delete(context.Background(), &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true})
	if codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), "down") {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteRefusesWhileAgentsRunAndForceStopsThem(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	c = withRecovery(t, h)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	_, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo})
	if codeOf(err) != rpc.FailedPrecondition || !strings.Contains(err.Error(), "harbor-orchestrator") {
		t.Fatalf("got %v", err)
	}
	list, err := c.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	folder := list.GetStart().GetFolder()
	dry, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true, DryRun: true})
	if err != nil || dry.GetResult().GetState() != "planned" || !strings.Contains(dry.GetResult().GetPlan(), "snapshot") {
		t.Fatalf("%v %v", dry, err)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatalf("a dry run touched the folder: %v", err)
	}
	res, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true})
	if err != nil || res.GetResult().GetState() != "done" || res.GetResult().GetControl() != "deleted" || res.GetResult().GetSnapshot() == "" {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("resume of a deleted route: %v", err)
	}
}

func waitStart(t *testing.T, c *client.Client, id *corev1.RouteId, repo string) *daemonv1.RouteStart {
	t.Helper()
	for range 500 {
		got, err := c.Route.Get(context.Background(), &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
		if err != nil {
			t.Fatal(err)
		}
		if got.GetStart().GetState() != "running" {
			return got.GetStart()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("up did not finish")
	return nil
}

func TestAFreshStartAfterDeleteIsRunningAndControllable(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	c = withRecovery(t, h)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	q := upRequest(repo)
	q.DryRun, q.NoAdjutant, q.AdjutantCommand = false, true, ""
	q.Caller = &daemonv1.UpCaller{Pid: int64(os.Getpid())}
	if _, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(ctx, q); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("up on a paused route: %v", err)
	}
	if _, err := c.Route.Delete(ctx, &daemonv1.RouteServiceDeleteRequest{Route: id, Repo: repo, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Up(ctx, q); err != nil {
		t.Fatal(err)
	}
	got := waitStart(t, c, id, repo)
	if got.GetState() != "done" || got.GetControl() != "running" {
		t.Fatalf("%+v", got)
	}
	res, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo})
	if err != nil || res.GetResult().GetControl() != "paused" {
		t.Fatalf("%v %v", res, err)
	}
	list, err := c.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil || len(list.GetStarts()) != 2 {
		t.Fatalf("%d starts, %v", len(list.GetStarts()), err)
	}
}

func TestResumeOfAStoppedRouteThroughTheService(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, world := upNoAdjutant(t, h, c)
	ctx := context.Background()
	id := &corev1.RouteId{Value: "harbor"}
	if _, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: id, Repo: repo, GraceSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo, Caller: &daemonv1.UpCaller{Pid: int64(os.Getpid()) + 1}}); codeOf(err) != rpc.PermissionDenied {
		t.Fatalf("forged caller: %v", err)
	}
	res, err := c.Route.Resume(ctx, &daemonv1.RouteServiceResumeRequest{Route: id, Repo: repo, Note: "go on", Caller: &daemonv1.UpCaller{Pid: int64(os.Getpid())}})
	if err != nil || res.GetResult().GetState() != "done" || res.GetResult().GetControl() != "running" {
		t.Fatalf("%v %v", res, err)
	}
	if world.PaneByLabel("orchestrator") == "" {
		t.Fatal("the orchestrator did not come back")
	}
}

func TestPauseRefusesATeardownItCannotRun(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo, _ := upNoAdjutant(t, h, c)
	h.stop()
	h.cfg.RouteTeardown = func(context.Context, string, string) error { return errors.New("a stop hook failed") }
	h.start()
	c = routeClient(t, h)
	id := &corev1.RouteId{Value: "harbor"}
	res, err := c.Route.Pause(context.Background(), &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo, Teardown: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetResult().GetState() == "done" || !strings.Contains(res.GetResult().GetReason(), "a stop hook failed") {
		t.Fatalf("%+v", res.GetResult())
	}
	// The halted verb holds the route: only the same verb may pick it up.
	ctx := context.Background()
	if _, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: id, Repo: repo}); codeOf(err) != rpc.FailedPrecondition {
		t.Fatalf("another verb: %v", err)
	}
	dry, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo, DryRun: true})
	if err != nil || dry.GetResult().GetOperation() != res.GetResult().GetOperation() {
		t.Fatalf("%v %v", dry, err)
	}
	again, err := c.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo})
	if err != nil || again.GetResult().GetOperation() != res.GetResult().GetOperation() || again.GetResult().GetState() == "done" {
		t.Fatalf("%v %v", again, err)
	}
}

func TestRouteTeardownOnARepositoryWithNoTasks(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	s := &WorktreeService{}
	if err := teardownRoute(s)(context.Background(), repo, filepath.Join(repo, ".herdsman", "x")); err != nil {
		t.Fatal(err)
	}
	if err := teardownRoute(s)(context.Background(), filepath.Join(repo, "missing"), "x"); err == nil {
		t.Fatal("a missing repository was accepted")
	}
}

func TestControlRefusesARouteWithNoStart(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	ctx := context.Background()
	_, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{Route: &corev1.RouteId{Value: "nope"}, Repo: "/x"})
	if codeOf(err) != rpc.NotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Route.Stop(ctx, &daemonv1.RouteServiceStopRequest{}); codeOf(err) != rpc.InvalidArgument {
		t.Fatalf("empty: %v", err)
	}
}

func TestUpReadsARelativeConfigFromTheRepository(t *testing.T) {
	h := newHarness(t)
	c := routeClient(t, h)
	repo := filepath.Join(h.dir, "proj")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLaunchConfig(t, repo, "[launch]\ncommand = [\"codex\"]\n")
	req := upRequest(repo)
	req.OrchestratorCommand = ""
	req.ConfigPath = "route.toml"
	if _, err := c.Route.Up(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}
