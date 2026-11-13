// Command acceptance runs the real-platform gates as one suite and fails unless
// each gate either passed or was skipped by a capability that requirements.md
// declares absent on this platform.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Gate is one scripted failure case: one test in one package.
type Gate struct {
	Name    string
	Package string
	Test    string
	Needs   []string
}

// Gates is the whole suite. A gate whose test does not exist yet stays listed
// and fails the run: an unwritten gate must never look like a passed one.
var Gates = []Gate{
	{Name: "teardown-overlapping-owners", Package: "./acceptance/teardown", Test: "TestOneTasksTeardownLeavesEverythingElse", Needs: []string{"ps", "lsof", "signals"}},
	{Name: "teardown-verb", Package: "./acceptance/teardown", Test: "TestTheVerbStopsWhatItRecorded", Needs: []string{"ps", "lsof", "signals"}},
	{Name: "teardown-container", Package: "./acceptance/teardown", Test: "TestAContainerIsStoppedAndKept", Needs: []string{"docker", "ps", "lsof", "signals"}},
	{Name: "budget-machine-limit", Package: "./acceptance/budget", Test: "TestMachineLimitStopsExclusiveWithinGraceAndLeavesShared", Needs: []string{"signals"}},
	{Name: "budget-wall-deadline", Package: "./acceptance/budget", Test: "TestWallDeadlineRefusesANewOperationThroughTheGate"},
	{Name: "up-dry-run", Package: "./acceptance/up", Test: "TestDryRunPlansAndStartsNothing"},
	{Name: "up-usage-error", Package: "./acceptance/up", Test: "TestMissingFlagsAreAUsageError"},
	{Name: "up-manual-pair", Package: "./acceptance/up", Test: "TestManualPairOnAScriptedTerminal", Needs: []string{"pty"}},

	{Name: "up-prelude-crash", Package: "./acceptance/up", Test: "TestPreludeCrashReplaysNothing", Needs: []string{"pty", "signals"}},
	{Name: "up-verdict-then-pair", Package: "./acceptance/up", Test: "TestVerdictSurvivesThePairQuestion", Needs: []string{"pty", "signals"}},
	{Name: "clean-undo-ignored-data", Package: "./acceptance/recovery", Test: "TestIgnoredDataComesBackThroughUndo"},
	{Name: "clean-failed-snapshot", Package: "./acceptance/recovery", Test: "TestFailedSnapshotDeletesNothing"},
	{Name: "restore-without-source", Package: "./acceptance/recovery", Test: "TestRestoreNeedsNoSourceRepository"},
	{Name: "merge-moved-target", Package: "./acceptance/landing", Test: "TestMovedTargetStopsExactLanding"},
	{Name: "merge-stale-review", Package: "./acceptance/landing", Test: "TestWhitespaceEditInvalidatesReview"},
	{Name: "merge-lost-response", Package: "./acceptance/landing", Test: "TestLostResponseLandsOnce"},
	{Name: "restricted-maintenance", Package: "./acceptance/restricted", Test: "TestMaintenanceAfterBranchesAreGone"},
	{Name: "drill-export-restore", Package: "./acceptance/drills", Test: "TestRestoreFromExportOnACleanMachine"},
	{Name: "core-launch-registers", Package: "./acceptance/core", Test: "TestRealCoreLaunchRegistersAndStops", Needs: []string{"herdr-core", "ps", "signals"}},
}

// Suite is one run on one platform.
type Suite struct {
	Requirements Requirements
	GOOS         string
	Probes       map[string]Probe
	Gates        []Gate
	Exec         func(context.Context, Gate) Result
}

// Run settles every gate, in order, and starts those whose needs are met.
func (s *Suite) Run(ctx context.Context) []Result {
	results := make([]Result, 0, len(s.Gates))
	for _, gate := range s.Gates {
		result := Decide(s.Requirements, s.GOOS, s.Probes, gate)
		if result.Status == StatusRun {
			result = s.Exec(ctx, gate)
		}
		results = append(results, result)
	}
	return results
}

// Select returns the named gates in table order; an empty list means all.
func Select(gates []Gate, names string) ([]Gate, error) {
	if names == "" {
		return gates, nil
	}
	wanted := strings.Split(names, ",")
	for _, name := range wanted {
		if !slices.ContainsFunc(gates, func(g Gate) bool { return g.Name == name }) {
			return nil, fmt.Errorf("unknown gate %q", name)
		}
	}
	var selected []Gate
	for _, gate := range gates {
		if slices.Contains(wanted, gate.Name) {
			selected = append(selected, gate)
		}
	}
	return selected, nil
}

type testEvent struct {
	Action string
	Test   string
	Output string
}

const reasonTailLines = 30

// judge reads `go test -json` output. Only a pass event for the gate's own test
// counts, because a -run pattern that matches nothing exits 0.
func judge(gate Gate, stream string, runErr error) Result {
	var tail, skipped []string
	passed := false
	for _, line := range strings.Split(strings.TrimRight(stream, "\n"), "\n") {
		var event testEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			tail = append(tail, line)
			continue
		}
		if event.Output != "" {
			tail = append(tail, strings.TrimRight(event.Output, "\n"))
		}
		if event.Test != "" && event.Action == "skip" {
			skipped = append(skipped, event.Test)
		}
		if event.Test == gate.Test && event.Action == "pass" {
			passed = true
		}
	}
	fail := func(reason string) Result {
		return Result{Gate: gate.Name, Status: StatusFail, Reason: reason}
	}
	if len(skipped) > 0 {
		return fail("skipped inside the gate, which only requirements.md may do: " + strings.Join(skipped, ", "))
	}
	if len(tail) > reasonTailLines {
		cut, kept := tail[:len(tail)-reasonTailLines], tail[len(tail)-reasonTailLines:]
		// A failed test prints its cause first and may dump a log after it.
		if i := slices.IndexFunc(cut, func(l string) bool { return strings.Contains(l, "_test.go:") }); i >= 0 {
			kept = append([]string{cut[i]}, kept...)
		}
		tail = kept
	}
	if runErr != nil {
		return fail(fmt.Sprintf("go test: %v\n%s", runErr, strings.Join(tail, "\n")))
	}
	if !passed {
		return fail("no result for " + gate.Test + " in " + gate.Package)
	}
	return Result{Gate: gate.Name, Status: StatusPass}
}

// hostTools must never run from a gate: their answers depend on the machine's
// logins and installs.
var hostTools = []string{"gh", "claude", "codex"}

// plantPoison writes stand-ins for the host tools that record every call, and
// returns the file they record to.
func plantPoison(dir string) (string, error) {
	marker := filepath.Join(dir, "host-tool-calls")
	script := "#!/bin/sh\necho \"${0##*/} $*\" >> '" + strings.ReplaceAll(marker, "'", `'\''`) + "'\nexit 127\n"
	for _, tool := range hostTools {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			return "", err
		}
	}
	return marker, nil
}

func poisonUse(marker string) string {
	data, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// gateKeep lists the only host variables a gate inherits. Tokens, agent
// settings and live herdr sessions are left behind by not being listed.
var gateKeep = []string{"TMPDIR", "LANG", "LC_ALL", "TERM", "USER", "GOFLAGS", "GOROOT", "GOMAXPROCS", "CGO_ENABLED", "DOCKER_HOST"}

func gateEnv(host []string, poison, home string, goEnv map[string]string) []string {
	var env []string
	path := ""
	for _, kv := range host {
		key, value, _ := strings.Cut(kv, "=")
		if key == "PATH" {
			path = value
		} else if slices.Contains(gateKeep, key) {
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+poison+":"+path, "HOME="+home)
	keys := make([]string, 0, len(goEnv))
	for key := range goEnv {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		env = append(env, key+"="+goEnv[key])
	}
	return env
}

// goEnvironment fixes the toolchain's paths while HOME is still the real one.
func goEnvironment() (map[string]string, error) {
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN"}
	out, err := exec.Command("go", append([]string{"env"}, keys...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	values := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(values) != len(keys) {
		return nil, fmt.Errorf("go env: got %d values for %d keys", len(values), len(keys))
	}
	env := map[string]string{"GOENV": "off"}
	for i, key := range keys {
		env[key] = values[i]
	}
	return env, nil
}

// execGate runs one gate's test in an environment with no host identity.
func execGate(ctx context.Context, gate Gate, goEnv map[string]string) Result {
	fail := func(err error) Result {
		return Result{Gate: gate.Name, Status: StatusFail, Reason: err.Error()}
	}
	root, err := os.MkdirTemp("", "gate")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(root)
	poison, home := filepath.Join(root, "bin"), filepath.Join(root, "home")
	for _, dir := range []string{poison, home} {
		if err := makeDir(dir, 0o755); err != nil {
			return fail(err)
		}
	}
	marker, err := plantPoison(poison)
	if err != nil {
		return fail(err)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-p", "1", "-tags", "acceptance", "-count=1", "-timeout", "15m", "-json", "-run", "^"+gate.Test+"$", gate.Package)
	cmd.Env = gateEnv(os.Environ(), poison, home, goEnv)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()
	result := judge(gate, output.String(), runErr)
	if used := poisonUse(marker); used != "" {
		return Result{Gate: gate.Name, Status: StatusFail, Reason: "the gate reached a host tool:\n" + used}
	}
	return result
}

// These are replaced by tests.
var (
	makeDir = os.Mkdir
	exit    = os.Exit
)

func main() {
	exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	names := flags.String("gate", "", "comma-separated gate names to run (default: every gate)")
	list := flags.Bool("list", false, "print the gate names and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *list {
		for _, gate := range Gates {
			fmt.Fprintln(stdout, gate.Name)
		}
		return 0
	}
	gates, err := Select(Gates, *names)
	if err != nil {
		fmt.Fprintln(stderr, "acceptance:", err)
		return 2
	}
	reqs, err := ParseRequirements(requirementsFile)
	if err != nil {
		fmt.Fprintln(stderr, "acceptance:", err)
		return 2
	}
	goEnv, err := goEnvironment()
	if err != nil {
		fmt.Fprintln(stderr, "acceptance:", err)
		return 1
	}
	suite := &Suite{
		Requirements: reqs, GOOS: runtime.GOOS, Probes: probes(), Gates: gates,
		Exec: func(ctx context.Context, gate Gate) Result { return execGate(ctx, gate, goEnv) },
	}
	results := suite.Run(context.Background())
	WriteReport(stdout, runtime.GOOS, results)
	if !Passed(results) {
		return 1
	}
	return 0
}
