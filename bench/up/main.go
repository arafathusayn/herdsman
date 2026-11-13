// Command up measures `herdsman up` on herdr-core against the fake core world
// of internal/runtime/core/coretest: from the daemon's UpRequest to the start's
// first wait for a person, which on core is the adjutant's start line. Each
// sample gets a fresh daemon, fake core and fake app; nothing reaches the
// user's own core, app, daemon or ~/.herdsman.
//
// The method is the worktree benchmark's: two slots run the same build in
// rotating order for the A/A control, every result is a 95% confidence
// interval of the mean, and the budget holds only when the interval's upper
// bound, widened by the A/A band, stays below it. No median, percentile or
// single value is reported.
//
//	go run ./bench/up -rounds 20
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
	"github.com/arafathusayn/herdsman/internal/runtime"
	"github.com/arafathusayn/herdsman/internal/runtime/core"
	"github.com/arafathusayn/herdsman/internal/runtime/core/coretest"
	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

// CaseUpCore is the id of the case this program measures.
const CaseUpCore = "up-core"

// defaultBudgetMS bounds one start on the fake core up to its first wait for
// a person. The fake answers at once, so the budget is the daemon's own work:
// planning, journaling and the launch steps of one agent.
const defaultBudgetMS = 2000

// The two slots of a round. Both run this build: their difference is the A/A
// control, the noise of the machine.
const (
	slotFirst  = "herdsman-a"
	slotSecond = "herdsman-b"
)

func main() {
	testing.Init()
	rounds := flag.Int("rounds", 20, "rounds, each one sample per slot")
	budget := flag.Float64("budget-ms", defaultBudgetMS, "the budget of one start, in milliseconds")
	flag.Parse()
	result := runCase(*rounds, *budget, measureUp)
	fmt.Println(result.String())
	if result.Verdict != verdictWithin {
		os.Exit(1)
	}
}

// measureUp times one start in a fresh fake core world.
func measureUp(slot string, round int) (time.Duration, error) {
	var (
		elapsed time.Duration
		err     error
		ran     bool
	)
	// testing.Benchmark supplies the testing.TB the fake world is built on
	// and runs its cleanups when the sample ends. It may call the function
	// again with a larger N; only the first call measures.
	testing.Benchmark(func(b *testing.B) {
		if ran {
			return
		}
		ran = true
		// A world that fails ends the call before the assignment below.
		err = errors.New("the fake core world failed")
		elapsed, err = sampleUp(b, fmt.Sprintf("%s-%d", slot, round))
	})
	return elapsed, err
}

// sampleUp builds the world untimed, then times the start.
func sampleUp(b *testing.B, tag string) (time.Duration, error) {
	w := coretest.New(b)
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	w.AutoWrap(ctx)
	w.OpenApp()
	dir, err := os.MkdirTemp("", "hsu")
	if err != nil {
		return 0, err
	}
	b.Cleanup(func() { _ = os.RemoveAll(dir) })
	private, home := filepath.Join(dir, "p"), filepath.Join(dir, "h")
	repo := filepath.Join(private, "proj")
	files := map[string]string{
		filepath.Join(private, "herdsman.config.toml"):  "[runtime]\nbackend = \"herdr-core\"\n\n[runtime.core]\nexperimental = true\n",
		filepath.Join(home, ".claude", "settings.json"): "{}\n",
		filepath.Join(dir, "herdsman-bin"):              "binary",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return 0, err
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			return 0, err
		}
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return 0, err
	}
	herdr := herdrtest.Start(b)
	herdrtest.Serve(herdr)
	d := daemon.New(daemon.Config{PrivateHome: private, Home: home, Version: "bench", Executable: filepath.Join(dir, "herdsman-bin"),
		HerdrSocket: herdr.Socket, Log: func(string) {}, LockWait: 2, DrainWait: time.Millisecond,
		Procs: func() ([]daemon.Proc, error) { return nil, nil },
		Core: daemon.CoreConfig{Socket: w.Core.Socket, App: coretest.AppRunner{W: w}, AppHome: w.AppHome, Procs: w.Procs, Peer: w.Peer,
			Environ:  []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=C"},
			Timeouts: core.Timeouts{Register: 5 * time.Second, Ready: 5 * time.Second, Visibility: time.Second, Poll: 2 * time.Millisecond}, NoReconcile: true}})
	if err := d.Start(); err != nil {
		return 0, err
	}
	serveCtx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(serveCtx) }()
	b.Cleanup(func() { stop(); <-served })
	c, err := client.Dial(client.Options{Socket: daemon.SocketPath(private)})
	if err != nil {
		return 0, err
	}
	b.Cleanup(c.Close)

	id := &corev1.RouteId{Value: "bench"}
	req := &daemonv1.UpRequest{Route: id, Repo: repo, Prompt: "measure " + tag, AdjutantCommand: "claude", OrchestratorCommand: "codex --no-daemon",
		Date: "2026-11-11", Caller: &daemonv1.UpCaller{Pid: int64(os.Getpid())}}
	start := time.Now()
	if _, err := c.Route.Up(context.Background(), req); err != nil {
		return 0, err
	}
	got, err := waitHalt(c, id, repo)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}
	// The sample counts only when the start reached the wait it is meant
	// to: on core, not refused, at the adjutant's start line.
	if got.GetBackend() != runtime.BackendCore || !strings.Contains(got.GetReason(), core.CodeNeedsAttention) {
		return 0, fmt.Errorf("the start stopped elsewhere: %s %s %s", got.GetBackend(), got.GetState(), got.GetReason())
	}
	return elapsed, nil
}

// waitHalt polls the start until it no longer runs.
func waitHalt(c *client.Client, id *corev1.RouteId, repo string) (*daemonv1.RouteStart, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := c.Route.Get(context.Background(), &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
		if err != nil {
			return nil, err
		}
		if got.GetStart().GetState() != "running" {
			return got.GetStart(), nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return nil, errors.New("up did not finish in 30 seconds")
}
