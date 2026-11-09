// Command worktrees benchmarks the worktree verbs against wt and holds the
// check that keeps the committed results current. See `make bench`.
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

//go:embed wt.version
var pinnedWt string

// Options configure one benchmark run.
type Options struct {
	Herdsman  string
	Wt        string
	Repo      string
	Out       string
	Work      string
	Rounds    int
	IgnoredMB int
	Small     int
	Large     int
}

func toolEnv(f *Fixtures) []string {
	// The check wrapper's core limit binds only the Go tool, so it would bias
	// the comparison with wt.
	env := slices.DeleteFunc(f.git.env(), func(entry string) bool { return strings.HasPrefix(entry, "GOMAXPROCS=") })
	for i, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			env[i] = "PATH=" + f.Bin + string(os.PathListSeparator) + value
		}
	}
	return env
}

// execStep runs a command and judges its exit code and output.
func execStep(env []string, step Step) (string, error) {
	cmd := exec.Command(step.Argv[0], step.Argv[1:]...)
	cmd.Dir = step.Dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && slices.Contains(step.Accept, exit.ExitCode()) {
		err = nil
	}
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", filepath.Base(step.Argv[0]), strings.Join(step.Argv[1:], " "), err, firstLine(out))
	}
	if step.Must != "" && !strings.Contains(string(out), step.Must) {
		return string(out), fmt.Errorf("%s: output lacks %s: %s", filepath.Base(step.Argv[0]), step.Must, firstLine(out))
	}
	return string(out), nil
}

func firstLine(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// composeReady reports a reachable container engine with compose.
func composeReady(env []string, dir string) bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	for _, args := range [][]string{{"docker", "info"}, {"docker", "compose", "version"}} {
		if _, err := execStep(env, Step{Dir: dir, Argv: args}); err != nil {
			return false
		}
	}
	return true
}

// measureWith times the steps of a sample and nothing around them.
func (b *Bench) measureWith() measureFunc {
	return func(slot string, c Case, round int) (total time.Duration, err error) {
		s := Sample{Tag: fmt.Sprintf("%s-%d", slot, round), Wt: slot == slotWt}
		if c.Before != nil {
			if err := c.Before(s); err != nil {
				return 0, fmt.Errorf("prepare: %w", err)
			}
		}
		if c.Cleanup != nil {
			// Whatever happens below, the next sample starts from a clean machine.
			defer func() { err = errors.Join(err, c.Cleanup(s)) }()
		}
		for _, step := range c.Steps(s) {
			start := time.Now()
			_, runErr := b.run(step)
			total += time.Since(start)
			if runErr != nil {
				return 0, runErr
			}
		}
		if c.After != nil {
			if err := c.After(s); err != nil {
				return 0, err
			}
		}
		return total, nil
	}
}

func toolVersion(program string, args ...string) (string, error) {
	out, err := exec.Command(program, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", program, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func wtVersion(program string) (string, error) {
	text, err := toolVersion(program, "--version")
	if err != nil {
		return "", err
	}
	version := strings.TrimPrefix(text, "wt ")
	if pinned := strings.TrimSpace(pinnedWt); version != pinned {
		return "", fmt.Errorf("wt is %s, the benchmark pins %s: run make tools-wt", version, pinned)
	}
	return version, nil
}

// machineLoad is the load average, which says how quiet the run was.
func machineLoad() string {
	out, err := toolVersion("uptime")
	if err != nil {
		return "unknown"
	}
	if _, load, ok := strings.Cut(out, "load average"); ok {
		return strings.TrimLeft(load, "s: ")
	}
	return "unknown"
}

func machineName() string {
	system, err := toolVersion("uname", "-srm")
	if err != nil {
		system = runtime.GOOS + " " + runtime.GOARCH
	}
	return fmt.Sprintf("%s, %d cores", system, runtime.NumCPU())
}

// sourceCommit names what is measured, and refuses edits the commit lacks.
func sourceCommit(repo string) (string, error) {
	dirty, err := gitOutput(repo, append([]string{"status", "--porcelain", "--"}, measuredPackages...)...)
	if err != nil {
		return "", fmt.Errorf("read the source state: %w", err)
	}
	if dirty != "" {
		return "", errors.New("the measured packages have uncommitted changes")
	}
	return gitOutput(repo, "rev-parse", "HEAD")
}

// Run builds the fixtures, measures every case and writes the results file.
func Run(o Options, stderr io.Writer) (Results, error) {
	commit, err := sourceCommit(o.Repo)
	if err != nil {
		return Results{}, err
	}
	wt, err := wtVersion(o.Wt)
	if err != nil {
		return Results{}, err
	}
	gitVersion, err := toolVersion("git", "version")
	if err != nil {
		return Results{}, err
	}
	load := machineLoad()
	f, err := BuildFixtures(o.Work, o.Small, o.Large, o.IgnoredMB)
	if err != nil {
		return Results{}, err
	}
	env := toolEnv(f)
	b := &Bench{Fixtures: f, Tools: Tools{Herdsman: o.Herdsman, Wt: o.Wt}, run: func(s Step) (string, error) { return execStep(env, s) }}
	b.ComposeReady = composeReady(env, f.Repo20)
	results := Results{
		Schema: SchemaVersion, Date: time.Now().UTC().Format(time.RFC3339), Commit: commit,
		Machine: machineName(), Git: strings.TrimPrefix(gitVersion, "git version "), Go: runtime.Version(),
		Wt: wt, Load: load, Rounds: o.Rounds, Small: o.Small, Large: o.Large, IgnoredMB: o.IgnoredMB,
	}
	for _, c := range b.Cases() {
		rounds := o.Rounds
		if c.Rounds > 0 && c.Rounds < rounds {
			rounds = c.Rounds
		}
		result := runCase(c, rounds, b.measureWith())
		fmt.Fprintf(stderr, "%s: %s %s\n", result.ID, result.Verdict, result.Reason)
		results.Cases = append(results.Cases, result)
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return results, err
	}
	return results, os.WriteFile(filepath.Join(o.Out, results.FileName()), []byte(results.Render()), 0o644)
}

func runCommand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	o := Options{}
	fs.StringVar(&o.Herdsman, "herdsman", "", "the herdsman binary")
	fs.StringVar(&o.Wt, "wt", "", "the wt binary")
	fs.StringVar(&o.Repo, "repo", ".", "the source checkout")
	fs.StringVar(&o.Out, "out", "bench/worktrees/results", "where the results file goes")
	fs.IntVar(&o.Rounds, "rounds", gateRounds, "samples per slot")
	fs.IntVar(&o.IgnoredMB, "ignored-mb", gateIgnoredMB, "size of the ignored tree")
	fs.IntVar(&o.Small, "small", gateSmall, "worktrees in the small repository")
	fs.IntVar(&o.Large, "large", gateLarge, "worktrees in the large repository")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if o.Herdsman == "" || o.Wt == "" {
		fmt.Fprintln(stderr, "run needs --herdsman and --wt")
		return 2
	}
	work, err := os.MkdirTemp("", "herdsman-bench-")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(work)
	// macOS hands out a symlinked temp folder; git reports the real path.
	if work, err = filepath.EvalSymlinks(work); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	o.Work = work
	results, err := Run(o, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stderr, "wrote", filepath.Join(o.Out, results.FileName()))
	if err := checkCases(results.FileName(), results); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func checkCommand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "the checkout to compare with")
	results := fs.String("results", "bench/worktrees/results", "the results folder")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := Check(*repo, *results); err != nil {
		fmt.Fprintln(stderr, "bench-results:", err)
		return 1
	}
	return 0
}

func dispatch(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return runCommand(args[1:], stderr)
		case "check":
			return checkCommand(args[1:], stderr)
		}
	}
	fmt.Fprintln(stderr, "usage: worktrees run --herdsman <bin> --wt <bin> | worktrees check")
	return 2
}

// exit is a variable so that a test can run main.
var exit = os.Exit

func main() { exit(dispatch(os.Args[1:], os.Stderr)) }
