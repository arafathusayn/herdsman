package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arafathusayn/herdsman/sdk/client"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func init() {
	register(command{name: "pause", summary: "Interrupt a route's agents and hold its channel; every pane stays open.", help: pauseHelp, run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
		return runControl(pauseVerb, args, stdout, stderr)
	}})
}

const pauseUsage = "usage: herdsman pause <route> [--teardown] [-y] [--dry-run]\n"

const pauseHelp = pauseUsage + `
Sends each agent the interrupt key, stops the checkpointer's prompts and holds
the message channel. The interrupt is best effort: an agent acts on it at its
next turn boundary. <route> is a route name, a route folder, or @ for the route
of the current folder. --teardown also stops the route's task processes.
`

// controlFlags are the flags the lifecycle verbs share or add.
type controlFlags struct {
	yes, dryRun, force, keepFolder, keepWorktrees, teardown bool
	note                                                    string
	grace                                                   int
}

// controlVerb is one lifecycle command.
type controlVerb struct {
	name, usage string
	flags       func(*flag.FlagSet, *controlFlags)
	call        func(context.Context, *client.Client, *corev1.RouteId, string, bool, controlFlags) (*daemonv1.RouteControl, error)
}

func teardownFlag(fs *flag.FlagSet, f *controlFlags) {
	fs.BoolVar(&f.teardown, "teardown", false, "also stop the route's task processes")
}

var pauseVerb = controlVerb{name: "pause", usage: pauseUsage, flags: teardownFlag, call: func(ctx context.Context, sdk *client.Client, id *corev1.RouteId, repo string, dry bool, f controlFlags) (*daemonv1.RouteControl, error) {
	res, err := sdk.Route.Pause(ctx, &daemonv1.RouteServicePauseRequest{Route: id, Repo: repo, DryRun: dry, Teardown: f.teardown})
	return res.GetResult(), err
}}

// parseControl reads "<route> [flags]" or "[flags] <route>".
func parseControl(v controlVerb, args []string, stderr io.Writer) (string, controlFlags, bool) {
	var f controlFlags
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(v.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, v.usage) }
	fs.BoolVar(&f.yes, "y", false, "do not ask")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print what would run and stop")
	if v.flags != nil {
		v.flags(fs, &f)
	}
	if err := fs.Parse(args); err != nil {
		return "", f, false
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n%s", fs.Arg(0), v.usage)
		return "", f, false
	}
	if name == "" {
		fmt.Fprint(stderr, v.usage)
		return "", f, false
	}
	return name, f, true
}

// routeRepoOf reads the repository out of a route folder's path:
// <repo>/.herdsman/<route>-<date>.
func routeRepoOf(folder string) string { return filepath.Dir(filepath.Dir(folder)) }

// matchRoute finds the start a route argument names: a route name in this
// repository, a route folder, or @ with the folder holding cwd.
func matchRoute(starts []*daemonv1.RouteStart, arg, repo, cwd string) (*daemonv1.RouteStart, error) {
	var hit *daemonv1.RouteStart
	abs := arg
	if arg != "@" && !filepath.IsAbs(arg) && strings.ContainsRune(arg, filepath.Separator) {
		abs = filepath.Join(cwd, arg)
	}
	for _, s := range starts {
		folder := s.GetFolder()
		switch {
		case arg == "@" && (cwd == folder || strings.HasPrefix(cwd, folder+string(filepath.Separator))):
			hit = s
		case arg != "@" && filepath.IsAbs(abs) && abs == folder:
			hit = s
		case arg != "@" && !filepath.IsAbs(abs) && s.GetRoute().GetValue() == arg && routeRepoOf(folder) == repo:
			hit = s
		}
	}
	if hit == nil {
		if arg == "@" {
			return nil, errors.New("this folder is not inside a route folder")
		}
		return nil, fmt.Errorf("no route %q is journaled for this repository", arg)
	}
	return hit, nil
}

func resolveRoute(ctx context.Context, sdk *client.Client, arg string) (*corev1.RouteId, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	if cwd, err = filepath.EvalSymlinks(cwd); err != nil {
		return nil, "", err
	}
	repo, err := upRepo()
	if err != nil && arg != "@" && !filepath.IsAbs(arg) {
		return nil, "", err
	}
	list, err := sdk.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil {
		return nil, "", err
	}
	start, err := matchRoute(list.GetStarts(), arg, repo, cwd)
	if err != nil {
		return nil, "", err
	}
	return start.GetRoute(), routeRepoOf(start.GetFolder()), nil
}

// confirmVerb asks on the controlling terminal; without one the verb needs -y.
func confirmVerb(verb string, open func() (io.ReadWriteCloser, error)) error {
	tty, err := open()
	if err != nil {
		return fmt.Errorf("no terminal to confirm on; pass -y to %s without asking", verb)
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s this route? [y/N] ", verb)
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(line), "y") {
		return errors.New("not confirmed")
	}
	return nil
}

func openTTY() (io.ReadWriteCloser, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

// runControl prints a verb's plan, confirms it, and runs it.
func runControl(v controlVerb, args []string, stdout, stderr io.Writer) int {
	arg, f, ok := parseControl(v, args, stderr)
	if !ok {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "herdsman %s: %v\n", v.name, err)
		return 1
	}
	ctx := context.Background()
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return fail(err)
	}
	defer sdk.Close()
	id, repo, err := resolveRoute(ctx, sdk, arg)
	if err != nil {
		return fail(err)
	}
	plan, err := v.call(ctx, sdk, id, repo, true, f)
	if err != nil {
		return fail(err)
	}
	if plan.GetState() == "unchanged" {
		fmt.Fprintf(stdout, "route %s is already %s\n", id.GetValue(), plan.GetControl())
		return 0
	}
	fmt.Fprint(stdout, plan.GetPlan())
	if f.dryRun {
		return 0
	}
	if !f.yes {
		if err := confirmVerb(v.name, openTTY); err != nil {
			return fail(err)
		}
	}
	res, err := v.call(ctx, sdk, id, repo, false, f)
	if err != nil {
		return fail(err)
	}
	switch res.GetState() {
	case "done":
		fmt.Fprintf(stdout, "route %s is %s\n", id.GetValue(), res.GetControl())
		if res.GetSnapshot() != "" {
			fmt.Fprintf(stdout, "snapshot: %s\nto restore it: herdsman undo %s\n", res.GetSnapshot(), res.GetOperation())
		}
		return 0
	case "unchanged":
		fmt.Fprintf(stdout, "route %s is already %s\n", id.GetValue(), res.GetControl())
		return 0
	}
	return fail(fmt.Errorf("the %s is %s: %s", v.name, res.GetState(), res.GetReason()))
}

// routeStatusText prints a route's lifecycle and pair. A flag at the line's end
// is the same "<-- " suffix the agent snapshot uses.
func routeStatusText(w io.Writer, s *daemonv1.RouteStart) {
	fmt.Fprintf(w, "route %s: %s (start %s)\nfolder: %s\n", s.GetRoute().GetValue(), s.GetControl(), s.GetState(), s.GetFolder())
	if s.GetReason() != "" {
		fmt.Fprintf(w, "reason: %s\n", s.GetReason())
	}
	pair := s.GetPair()
	switch {
	case pair == nil || pair.GetChecksum() == "":
		fmt.Fprintln(w, "pair: none")
	case pair.GetApproved():
		fmt.Fprintf(w, "pair: approved %s\n", pair.GetChecksum())
	default:
		fmt.Fprintf(w, "pair: pending %s  <-- PAIR PENDING\n", pair.GetChecksum())
	}
}

func routeStatusJSON(w io.Writer, s *daemonv1.RouteStart) error {
	pending := s.GetPair().GetChecksum() != "" && !s.GetPair().GetApproved()
	return json.NewEncoder(w).Encode(map[string]any{
		"route": s.GetRoute().GetValue(), "control": s.GetControl(), "state": s.GetState(), "reason": s.GetReason(), "folder": s.GetFolder(),
		"pair": map[string]any{"checksum": s.GetPair().GetChecksum(), "approved": s.GetPair().GetApproved(), "pending": pending},
	})
}

func runRouteStatus(arg string, asJSON bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "herdsman status:", err)
		return 1
	}
	ctx := context.Background()
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return fail(err)
	}
	defer sdk.Close()
	id, repo, err := resolveRoute(ctx, sdk, arg)
	if err != nil {
		return fail(err)
	}
	res, err := sdk.Route.Get(ctx, &daemonv1.RouteServiceGetRequest{Route: id, Repo: repo})
	if err != nil {
		return fail(err)
	}
	if asJSON {
		if err := routeStatusJSON(stdout, res.GetStart()); err != nil {
			return fail(err)
		}
		return 0
	}
	routeStatusText(stdout, res.GetStart())
	return 0
}
