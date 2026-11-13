package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/ledger"
	"github.com/arafathusayn/herdsman/internal/wtree"
)

// Case ids a results file must hold.
const (
	CaseLedgerScan      = "ledger-scan"
	CaseTeardown        = "teardown"
	CaseTeardownCompose = "teardown-compose"

	// teardownTask is the task both budget cases work on.
	teardownTask = "teardown-target"

	ledgerBudgetMS = 200
	teardownGrace  = 1
	// The stop may take the grace period and two more seconds.
	teardownBudgetMS = (teardownGrace + 2) * 1000
)

var requiredVersus = []string{
	"ls-20-cold", "ls-20-warm", "ls-50-cold", "ls-50-warm",
	"ls-full-20", "ls-full-50", "new-shared-cache", "go-branch", "go-pr",
}

var requiredBudgets = []string{CaseLedgerScan, CaseTeardown}

// The workload a results file must have measured to count for the gate.
const (
	gateRounds    = 20
	gateSmall     = 20
	gateLarge     = 50
	gateIgnoredMB = 2048
)

var requiredBudgetMS = map[string]float64{CaseLedgerScan: ledgerBudgetMS, CaseTeardown: teardownBudgetMS}

// Step is one timed command. Accept lists exit codes besides zero that still
// count as success, and Must is text its output has to contain.
type Step struct {
	Dir    string
	Argv   []string
	Accept []int
	Must   string
}

// Tools names the programs under test.
type Tools struct {
	Herdsman string
	Wt       string
}

// Sample is what one slot does in one round.
type Sample struct {
	Tag string
	Wt  bool
}

// Case is one benchmark. Before and After are not timed; After also checks
// that the timed commands did their work.
type Case struct {
	ID       string
	Kind     string
	BudgetMS float64
	Rounds   int
	Steps    func(Sample) []Step
	Before   func(Sample) error
	After    func(Sample) error
	Cleanup  func(Sample) error
	// Daemon says the case's verbs reach a daemon, so one runs for the case.
	Daemon bool
}

// Bench holds what the cases share.
type Bench struct {
	Fixtures     *Fixtures
	Tools        Tools
	run          func(Step) (string, error)
	ComposeReady bool
	daemon       func() (func() error, error)
}

func (b *Bench) resetCaches(repo string, s Sample) error {
	if s.Wt {
		_, err := b.run(Step{Dir: repo, Argv: []string{b.Tools.Wt, "config", "state", "cache", "clear"}})
		return err
	}
	common, err := b.Fixtures.git.run(repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(common, wtree.StateFolder))
}

func (b *Bench) listSteps(repo string, full bool) func(Sample) []Step {
	return func(s Sample) []Step {
		verb := []string{b.Tools.Herdsman, "ls"}
		if s.Wt {
			verb = []string{b.Tools.Wt, "list"}
		}
		if full {
			verb = append(verb, "--full")
		}
		return []Step{{Dir: repo, Argv: verb}}
	}
}

func (b *Bench) listCase(id, repo string, full, cold bool) Case {
	c := Case{ID: id, Kind: KindVersus, Steps: b.listSteps(repo, full)}
	c.Before = func(s Sample) error {
		if cold {
			return b.resetCaches(repo, s)
		}
		// A warm sample follows a run of the same command.
		_, err := b.run(c.Steps(s)[0])
		return err
	}
	return c
}

// worktreeOf finds the linked worktree whose branch ends in the name.
func (b *Bench) worktreeOf(repo, name string) (path, branch string, err error) {
	out, err := b.Fixtures.git.run(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", "", err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var p, ref string
		for _, line := range strings.Split(block, "\n") {
			if value, ok := strings.CutPrefix(line, "worktree "); ok {
				p = value
			}
			if value, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				ref = value
			}
		}
		if ref == name || strings.HasSuffix(ref, "/"+name) {
			return p, ref, nil
		}
	}
	return "", "", fmt.Errorf("no worktree for %s", name)
}

func (b *Bench) newCase() Case {
	repo := b.Fixtures.Repo20
	name := func(s Sample) string { return "new-" + s.Tag }
	return Case{
		ID: "new-shared-cache", Kind: KindVersus, Rounds: 5,
		Steps: func(s Sample) []Step {
			if s.Wt {
				return []Step{
					{Dir: repo, Argv: []string{b.Tools.Wt, "switch", "--create", name(s), "--no-cd", "--no-hooks", "-y"}},
					{Dir: repo, Argv: []string{b.Tools.Wt, "step", "copy-ignored", "--to", name(s)}},
				}
			}
			return []Step{{Dir: repo, Argv: []string{b.Tools.Herdsman, "new", name(s), "--base", "main"}}}
		},
		After: func(s Sample) error {
			path, _, err := b.worktreeOf(repo, name(s))
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(path, "node_modules", cacheMarker)); err != nil {
				return errors.New("the new worktree did not receive the ignored tree")
			}
			return nil
		},
		Cleanup: func(s Sample) error {
			path, branch, err := b.worktreeOf(repo, name(s))
			if err != nil {
				return nil // The timed command failed before it created one.
			}
			return b.Fixtures.git.script(repo, []string{"worktree", "remove", "--force", path}, []string{"branch", "-q", "-D", branch})
		},
	}
}

func (b *Bench) goCase(id, selector string) Case {
	repo := b.Fixtures.Repo20
	return Case{ID: id, Kind: KindVersus, Steps: func(s Sample) []Step {
		if s.Wt {
			return []Step{{Dir: repo, Argv: []string{b.Tools.Wt, "switch", selector, "--no-cd", "-y"}}}
		}
		return []Step{{Dir: repo, Argv: []string{b.Tools.Herdsman, "go", selector, "--no-cd"}}}
	}}
}

func (b *Bench) ledgerCase() Case {
	repo := b.Fixtures.Repo20
	return Case{
		ID: CaseLedgerScan, Kind: KindBudget, BudgetMS: ledgerBudgetMS,
		Daemon: true,
		Before: func(Sample) error { return b.ensureTask(repo) },
		Steps: func(Sample) []Step {
			// With nothing to stop the verb scans the machine, prints the plan and
			// exits 0. The plan's key is required, so that output from anything
			// else cannot pass as a fast scan.
			return []Step{{Dir: repo, Argv: []string{b.Tools.Herdsman, "teardown", teardownTask, "--json"}, Must: `"lines"`}}
		},
	}
}

// ensureTask makes the budget cases' task with the verb, since teardown knows
// only tasks that new recorded. It is made here, after the list cases have
// wiped the state and so that they count exactly the fixture's worktrees.
func (b *Bench) ensureTask(repo string) error {
	if _, _, err := b.worktreeOf(repo, teardownTask); err == nil {
		return nil
	}
	_, err := b.run(Step{Dir: repo, Argv: []string{b.Tools.Herdsman, "new", teardownTask, "--base", "main", "--no-setup"}})
	return err
}

func (b *Bench) teardownCase() Case { return b.teardownWith(CaseTeardown, nil) }

// composeFile runs one container that stops at once when asked.
const composeFile = "services:\n  web:\n    image: alpine:3\n    init: true\n    command: sleep 600\n"

// composeCase also starts a compose project, so that the measured teardown
// stops a dev server and containers together.
func (b *Bench) composeCase() Case {
	const project = "herdsman-bench"
	repo := b.Fixtures.Repo20
	var folder string
	compose := func(args ...string) Step {
		return Step{Dir: folder, Argv: append([]string{"docker", "compose", "-p", project}, args...)}
	}
	c := b.teardownWith(CaseTeardownCompose, func(_ Sample, path, execution string) error {
		folder = path
		if err := os.WriteFile(filepath.Join(path, "compose.yaml"), []byte(composeFile), 0o644); err != nil {
			return err
		}
		if _, err := b.run(compose("up", "-d", "--wait")); err != nil {
			return err
		}
		_, err := b.run(Step{Dir: repo, Argv: []string{b.Tools.Herdsman, "resource", "add",
			"--task", teardownTask, "--execution", execution, "--kind", ledger.KindCompose, "--id", project}})
		return err
	})
	after, cleanup := c.After, c.Cleanup
	c.After = func(s Sample) error {
		if err := after(s); err != nil {
			return err
		}
		out, err := b.run(compose("ps", "-q", "--status", "running"))
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "" {
			return errors.New("the compose project was still running after teardown")
		}
		return nil
	}
	c.Cleanup = func(s Sample) error {
		_, err := b.run(compose("down", "-v", "--timeout", "0"))
		return errors.Join(err, cleanup(s))
	}
	return c
}

// teardownWith measures teardown of a recorded server, and whatever extra
// starts beside it under the same execution.
func (b *Bench) teardownWith(id string, extra func(s Sample, path, execution string) error) Case {
	repo := b.Fixtures.Repo20
	type server struct {
		pid  int
		done chan struct{}
	}
	servers := map[string]server{}
	return Case{
		ID: id, Kind: KindBudget, BudgetMS: teardownBudgetMS,
		Daemon: true,
		Before: func(s Sample) error {
			if err := b.ensureTask(repo); err != nil {
				return err
			}
			path, _, err := b.worktreeOf(repo, teardownTask)
			if err != nil {
				return err
			}
			// Minted before the server starts: a process older than its execution
			// counts as shared, and teardown would leave it running.
			execution := ledger.NewExecution(time.Now())
			cmd := exec.Command("sleep", "600")
			cmd.Dir = path
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				return err
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			pid := cmd.Process.Pid
			servers[s.Tag] = server{pid: pid, done: done}
			// Only a recorded process is the task's own to stop.
			_, err = b.run(Step{Dir: repo, Argv: []string{b.Tools.Herdsman, "resource", "add",
				"--task", teardownTask, "--execution", execution, "--kind", ledger.KindProcess, "--id", strconv.Itoa(pid)}})
			if err == nil && extra != nil {
				err = extra(s, path, execution)
			}
			if err != nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				<-done
			}
			return err
		},
		Steps: func(Sample) []Step {
			return []Step{{Dir: repo, Argv: []string{b.Tools.Herdsman, "teardown", teardownTask, "-y", "--grace", strconv.Itoa(teardownGrace)}}}
		},
		After: func(s Sample) error {
			select {
			case <-servers[s.Tag].done:
				return nil
			default:
				return errors.New("the dev server was still running after teardown")
			}
		},
		Cleanup: func(s Sample) error {
			running := servers[s.Tag]
			select {
			case <-running.done:
			default:
				_ = syscall.Kill(running.pid, syscall.SIGKILL)
				<-running.done
			}
			return nil
		},
	}
}

// Cases lists the benchmarks of the commands that ship today.
func (b *Bench) Cases() []Case {
	f := b.Fixtures
	cases := []Case{
		b.listCase("ls-20-cold", f.Repo20, false, true),
		b.listCase("ls-20-warm", f.Repo20, false, false),
		b.listCase("ls-50-cold", f.Repo50, false, true),
		b.listCase("ls-50-warm", f.Repo50, false, false),
		b.listCase("ls-full-20", f.Repo20, true, false),
		b.listCase("ls-full-50", f.Repo50, true, false),
		b.newCase(),
		b.goCase("go-branch", branchAhead),
		b.goCase("go-pr", "pr:"+strconv.Itoa(requestNumber)),
		b.ledgerCase(),
		b.teardownCase(),
	}
	if b.ComposeReady {
		cases = append(cases, b.composeCase())
	}
	return cases
}
