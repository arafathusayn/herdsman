package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const runtimeHelp = `usage: herdsman runtime doctor [--route <route>] [--backend <backend>] [-c <config>] [--json]
       herdsman runtime ls [--route <route>] [--json]
       herdsman runtime default [<backend>] [--json]
       herdsman runtime submit <execution> <line> [--yes] [--again] [--json]
       herdsman runtime show <execution> [--json]

Where agents run. A backend is herdr-core or herdr-tui. A new route takes
--backend, then the config file's [runtime] backend, then the installation
default; a route that exists keeps the backend it started on. herdr-core ships
as an experiment (runtime.core/0.1.0): a new route there also needs
experimental = true in [runtime.core], and every installation's default is
herdr-tui.

exit codes:
  0  done; for doctor, the selected backend can run
  1  the daemon refused or is not running; for doctor, a check failed
  2  bad usage
`

const runtimeDoctorHelp = `usage: herdsman runtime doctor [--route <route>] [--backend <backend>] [-c <config>] [--json]

Says which backend a run here would use and why, and whether each backend can
run on this machine. For herdr-core it also says how each harness is woken
and which of its steps need a person, the steps up names before a route
starts, and each value of [runtime.core], [runtime.app] and
[runtime.tui_view] in use with where it came from: the config file, the
route's persisted endpoint, or the default. With --route, the route's own
backend and endpoint. It changes nothing.

exit codes:
  0  the selected backend can run
  1  a check failed, or the daemon refused or is not running
  2  bad usage
`

const runtimeLsHelp = `usage: herdsman runtime ls [--route <route>] [--json]

Lists the executions the daemon knows, newest first: one per harness launch.
An agent of a route started before execution records existed is listed as
legacy. --route lists one route of this repository. --json also gives each
record's experiment label and the harness's own conversation id with where it
was read, when one was found.

exit codes:
  0  listed
  1  the daemon refused or is not running
  2  bad usage
`

const runtimeDefaultHelp = `usage: herdsman runtime default [<backend>] [--json]

With no value, prints the installation default and where it came from. With a
value, herdr-core or herdr-tui, sets it. Routes that exist keep their backend,
and a config file's [runtime] backend still wins.

exit codes:
  0  printed or set
  1  the daemon refused or is not running
  2  bad usage
`

// dialRuntime reaches the daemon through the SDK; tests replace it.
var dialRuntime = func() (daemonv1.RuntimeService, func(), error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, nil, err
	}
	return sdk.Runtime, sdk.Close, nil
}

// runtimeRepo is the repository the runtime verbs read: the git checkout of
// the working folder, or none.
var runtimeRepo = func() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	repo, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	return repo
}

var runtimeVerbs = map[string]func([]string, io.Writer, io.Writer) int{
	"doctor":  runRuntimeDoctor,
	"ls":      runRuntimeLs,
	"default": runRuntimeDefault,
	"submit":  runRuntimeSubmit,
	"show":    runRuntimeShow,
}

const runtimeShowHelp = `usage: herdsman runtime show <execution> [--json]

Focuses an execution's existing terminal in the herdr app, through the daemon.
It never starts an execution, never creates a view and types nothing. The
execution is an id from herdsman runtime ls.

exit codes:
  0  shown
  1  refused (no recorded place, identity not proven) or the daemon is not running
  2  bad usage
`

func runRuntimeShow(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("runtime show", stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "runtime show takes one execution id")
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	res, err := service.Show(context.Background(), &daemonv1.RuntimeServiceShowRequest{ExecutionId: positional[0]})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, res); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "shown on %s\n", res.GetBackend())
	return 0
}

const runtimeSubmitHelp = `usage: herdsman runtime submit <execution> <line> [--yes] [--again] [--json]

Types one line into one herdr-core execution's terminal and presses Enter, as
your own explicit action. herdr-core checks the program's state once, before
the paste; the Enter follows that check by about 200 ms with no second check,
so a dialog that opens in between is confirmed by it. Once core holds the
line nothing can recall it. It is typed only while the program reports idle,
after it reported a status since it started, with no other submit of yours
outstanding there. No automation ever runs this, and an agent is refused.

Without --yes it shows that warning and asks you to type yes. --again sends
although an earlier submit is unresolved, knowing that both may be typed.

exit codes:
  0  typed, or rejected by core before anything was written
  1  refused, unresolved, or the daemon is not running
  2  bad usage
`

// submitWarning is what a person reads before a typed submit; the daemon
// refuses a submit that was not confirmed.
const submitWarning = "This types one line into the agent's terminal and presses Enter. herdr-core checks the program's state once, before the paste; " +
	"the Enter follows that check by about 200 ms with no second check, so a dialog that opens in between is confirmed by it. " +
	"Once core holds the line, nothing can recall it."

// confirmSubmit asks the person at the terminal; tests replace it.
var confirmSubmit = func(stderr io.Writer) bool {
	fmt.Fprintf(stderr, "%s\ntype yes to type the line: ", submitWarning)
	var answer string
	if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
		return false
	}
	return answer == "yes"
}

func runRuntimeSubmit(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("runtime submit", stderr)
	yes := fs.Bool("yes", false, "you read the warning: the Enter follows the check by about 200 ms")
	again := fs.Bool("again", false, "send although an earlier submit is unresolved")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 2 {
		return usageError(stderr, "runtime submit takes an execution id and one line")
	}
	if !*yes && !confirmSubmit(stderr) {
		return usageError(stderr, "not confirmed; nothing was typed")
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	res, err := service.Submit(context.Background(), &daemonv1.RuntimeServiceSubmitRequest{ExecutionId: positional[0], Text: positional[1],
		Pid: int64(os.Getpid()), Confirmed: true, Again: *again})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, res); err != nil {
			return fail(stderr, err)
		}
	} else {
		fmt.Fprintf(stdout, "submit %s: %s\n", res.GetSubmitId(), res.GetState())
		if res.GetDetail() != "" {
			fmt.Fprintln(stdout, res.GetDetail())
		}
	}
	if res.GetState() == "unresolved" {
		fmt.Fprintln(stderr, "herdsman: unresolved: keep waiting, stop the execution, or send again with --again knowing both may be typed")
		return 1
	}
	return 0
}

func init() {
	register(command{name: "runtime", summary: "Where agents run: runtime doctor, runtime ls, runtime default, runtime submit, runtime show.", help: runtimeHelp,
		subHelp: map[string]string{"doctor": runtimeDoctorHelp, "ls": runtimeLsHelp, "default": runtimeDefaultHelp, "submit": runtimeSubmitHelp, "show": runtimeShowHelp}, run: runRuntime})
}

func runRuntime(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, runtimeHelp)
		return 2
	}
	run, ok := runtimeVerbs[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "runtime: unknown verb %q\n%s", args[0], runtimeHelp)
		return 2
	}
	return run(args[1:], stdout, stderr)
}

func printProto(stdout io.Writer, m proto.Message) error {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(out))
	return err
}

func runRuntimeDoctor(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("runtime doctor", stderr)
	routeName := fs.String("route", "", "report this route's own backend")
	backend := fs.String("backend", "", "resolve as up --backend would")
	configPath := fs.String("c", "", "the config file")
	fs.StringVar(configPath, "config", "", "the config file")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "runtime doctor takes no argument")
	}
	if *configPath != "" {
		if *configPath, err = filepath.Abs(*configPath); err != nil {
			return fail(stderr, err)
		}
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	req := &daemonv1.RuntimeServiceDoctorRequest{Repo: runtimeRepo(), ConfigPath: *configPath, Backend: *backend}
	if *routeName != "" {
		req.Route = &corev1.RouteId{Value: *routeName}
	}
	report, err := service.Doctor(context.Background(), req)
	if err != nil {
		return fail(stderr, err)
	}
	failed := false
	for _, check := range report.GetChecks() {
		failed = failed || check.GetState() == "fail"
	}
	if *asJSON {
		if err := printProto(stdout, report); err != nil {
			return fail(stderr, err)
		}
	} else {
		if sel := report.GetSelection(); sel.GetBackend() != "" {
			from := sel.GetSource()
			if sel.GetConfigPath() != "" {
				from += " " + sel.GetConfigPath()
			}
			fmt.Fprintf(stdout, "backend: %s (from %s)\n", sel.GetBackend(), from)
		}
		if inst := report.GetInstallation(); inst.GetBackend() != "" {
			fmt.Fprintf(stdout, "installation default: %s (%s)\n", inst.GetBackend(), inst.GetSource())
		}
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, b := range report.GetBackends() {
			state := "available"
			if !b.GetAvailable() {
				state = "unavailable: " + b.GetReason()
			}
			fmt.Fprintf(w, "%s\t%s\n", b.GetBackend(), state)
		}
		_ = w.Flush()
		for _, check := range report.GetChecks() {
			fmt.Fprintf(stdout, "%-4s %s: %s\n", check.GetState(), check.GetName(), check.GetDetail())
		}
	}
	if failed {
		return 1
	}
	return 0
}

func runRuntimeLs(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("runtime ls", stderr)
	routeName := fs.String("route", "", "list one route of this repository")
	asJSON := fs.Bool("json", false, "print the executions as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "runtime ls takes no argument")
	}
	req := &daemonv1.RuntimeServiceListExecutionsRequest{}
	if *routeName != "" {
		repo := runtimeRepo()
		if repo == "" {
			return usageError(stderr, "--route names a route of this repository; run it inside one")
		}
		req.Route, req.Repo = &corev1.RouteId{Value: *routeName}, repo
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	list, err := service.ListExecutions(context.Background(), req)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, list); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	if len(list.GetExecutions()) == 0 {
		fmt.Fprintln(stdout, "no executions")
		return 0
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROUTE\tAGENT\tROLE\tBACKEND\tEXECUTION\tPROCESS\tREADINESS\tPROGRAM")
	for _, e := range list.GetExecutions() {
		id := e.GetRef().GetExecutionId()
		if id == "" {
			id = e.GetSource()
		} else {
			id = fmt.Sprintf("%s (gen %d)", id, e.GetRef().GetGeneration())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.GetRoute().GetValue(), e.GetAgent(), e.GetRole(), e.GetRef().GetBackend(), id,
			orDash(e.GetObservation().GetProcess()), orDash(e.GetObservation().GetReadiness()), orDash(e.GetObservation().GetProgram()))
	}
	_ = w.Flush()
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runRuntimeDefault(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("runtime default", stderr)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) > 1 {
		return usageError(stderr, "runtime default takes at most one backend")
	}
	service, closeService, err := dialRuntime()
	if err != nil {
		return fail(stderr, err)
	}
	defer closeService()
	ctx := context.Background()
	if len(positional) == 1 {
		set, err := service.SetDefault(ctx, &daemonv1.RuntimeServiceSetDefaultRequest{Backend: positional[0]})
		if err != nil {
			return fail(stderr, err)
		}
		if *asJSON {
			if err := printProto(stdout, set); err != nil {
				return fail(stderr, err)
			}
			return 0
		}
		fmt.Fprintf(stdout, "installation default: %s (was %s)\n", set.GetBackend(), set.GetPrevious())
		// Setting a backend this build cannot run is allowed, since it is
		// only data, but a person should hear it at once.
		if report, err := service.Doctor(ctx, &daemonv1.RuntimeServiceDoctorRequest{}); err == nil {
			for _, b := range report.GetBackends() {
				if b.GetBackend() == set.GetBackend() && !b.GetAvailable() {
					fmt.Fprintln(stderr, "herdsman: note:", b.GetReason())
				}
			}
		}
		return 0
	}
	def, err := service.GetDefault(ctx, &daemonv1.RuntimeServiceGetDefaultRequest{})
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if err := printProto(stdout, def); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	when := ""
	if def.GetWrittenAt() > 0 {
		when = ", " + time.UnixMilli(def.GetWrittenAt()).UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(stdout, "%s (%s%s)\n", def.GetBackend(), describeDefaultSource(def.GetSource()), when)
	return 0
}

// describeDefaultSource says in words where the installation default came from.
func describeDefaultSource(source string) string {
	switch source {
	case "existing-store":
		return "kept by the upgrade of an existing installation"
	case "new-store":
		return "written when this installation was created"
	case "set":
		return "set with herdsman runtime default"
	}
	return source
}
