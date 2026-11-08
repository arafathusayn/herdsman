// Package budget is the acceptance gate for spend limits, run against real
// processes. The teardown port here is a local adapter over ledger plan lines;
// it applies the same rule as the real teardown, acting only on exclusive lines.
package budget

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/policy"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (c *child) running() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// start runs a shell script and waits for its "ready" line, so a signal cannot
// arrive before the script has set its traps.
func start(t *testing.T, script string) *child {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, done: make(chan struct{})}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child never became ready: %q %v", line, err)
	}
	go func() {
		_ = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-c.done
	})
	return c
}

// lineAdapter stops the exclusive lines of a plan: a polite signal, a wait of at
// most grace, then a forced one. Other lines are reported and never touched.
type lineAdapter struct {
	lines    []ledger.Line
	children map[string]*child
}

func (a *lineAdapter) Teardown(ctx context.Context, _ policy.Scope, grace time.Duration) (policy.TeardownReport, error) {
	var report policy.TeardownReport
	var wg sync.WaitGroup
	for _, line := range a.lines {
		if line.Level != ledger.LevelExclusive {
			report.LeftRunning = append(report.LeftRunning, line.ID+" ("+string(line.Level)+")")
			continue
		}
		report.Stopped = append(report.Stopped, line.ID)
		target := a.children[line.ID]
		_ = target.cmd.Process.Signal(syscall.SIGTERM)
		wg.Go(func() {
			select {
			case <-target.done:
			case <-time.After(grace):
				_ = target.cmd.Process.Kill()
				<-target.done
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
	return report, ctx.Err()
}

func TestMachineLimitStopsExclusiveWithinGraceAndLeavesShared(t *testing.T) {
	polite := start(t, `trap 'exit 0' TERM; echo ready; while :; do sleep 0.02; done`)
	stubborn := start(t, `trap '' TERM; echo ready; exec sleep 60`)
	shared := start(t, `echo ready; exec sleep 60`)

	id := func(c *child) string { return "pid-" + strconv.Itoa(c.cmd.Process.Pid) }
	adapter := &lineAdapter{
		lines: []ledger.Line{
			{Kind: "process", ID: id(polite), Level: ledger.LevelExclusive},
			{Kind: "process", ID: id(stubborn), Level: ledger.LevelExclusive},
			{Kind: "process", ID: id(shared), Level: ledger.LevelShared},
		},
		children: map[string]*child{id(polite): polite, id(stubborn): stubborn, id(shared): shared},
	}

	var events []policy.Event
	const grace = 400 * time.Millisecond
	enforcer := &policy.Enforcer{
		Policy:   policy.Policy{Budget: policy.Budget{Machine: time.Minute}},
		Grace:    grace,
		Teardown: adapter,
		Emit:     func(e policy.Event) { events = append(events, e) },
	}

	began := time.Now()
	scope := policy.Scope{Kind: policy.ScopeTask, Name: "demo"}
	actions, err := enforcer.Observe(context.Background(), scope, policy.Usage{Machine: 2 * time.Minute})
	took := time.Since(began)
	if err != nil {
		t.Fatal(err)
	}
	if polite.running() || stubborn.running() {
		t.Fatal("an exclusive process outlived the teardown")
	}
	if took > grace+2*time.Second {
		t.Fatalf("teardown took %v against a grace of %v", took, grace)
	}
	if !shared.running() {
		t.Fatal("the shared process was stopped")
	}
	if len(actions) != 1 || actions[0].Kind != policy.ActionTeardown {
		t.Fatalf("actions %+v", actions)
	}
	if len(events) != 1 || events[0].Kind != policy.EventBudgetExhausted || len(events[0].LeftRunning) != 1 {
		t.Fatalf("events %+v", events)
	}
}

func TestWallDeadlineRefusesANewOperationThroughTheGate(t *testing.T) {
	started := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	section := policy.Section{Budget: policy.BudgetSection{Wall: "30m"}}
	limits, err := policy.Parse(section)
	if err != nil {
		t.Fatal(err)
	}
	now := started.Add(29 * time.Minute)
	gate := &daemon.PolicyGate{
		Load: func() (policy.Policy, error) { return limits, nil },
		Usage: func(policy.Operation) (policy.Usages, error) {
			return policy.Usages{Route: policy.Usage{Started: started}}, nil
		},
		Now: func() time.Time { return now },
	}
	op := policy.Operation{Kind: "merge", Route: "demo"}
	if e := gate.Admit(op); e != nil {
		t.Fatalf("before the deadline: %+v", e)
	}
	now = started.Add(30 * time.Minute)
	if e := gate.Admit(op); e == nil || e.Code != "human_boundary" {
		t.Fatalf("at the deadline: %+v", e)
	}
	limits.Budget.Wall = 2 * time.Hour
	if e := gate.Admit(op); e != nil {
		t.Fatalf("after a person raised the budget: %+v", e)
	}
}
