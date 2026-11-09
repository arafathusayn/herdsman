package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arafathusayn/herdsman/internal/doctor"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/secrets"
	"github.com/arafathusayn/herdsman/sdk/client"
	"github.com/arafathusayn/herdsman/sdk/config"
	corev1 "github.com/arafathusayn/herdsman/sdk/core/v1"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
)

func init() {
	register(command{name: "up", summary: "Start a route: workspace, private inbox, both coordinators, the pair and the first brief.", help: upHelp, run: runUp})
}

const upUsage = `usage: herdsman up <route> -p <prompt> -a <command> -o <command> [-c <config>]
                   [--adjutant-kind <kind>] [--orchestrator-kind <kind>] [--no-adjutant]
                   [--pair auto|manual] [--web] [--no-attach] [--dry-run]
       herdsman up [<route>] --continue [--step <n>=done|rerun]...
`

const upHelp = upUsage + `
-a and -o are the commands that launch each coordinator's harness, typed as you
would type them: "claude doctor && claude --model opus".
-c names a config file. Its [launch] section starts the coordinator that has
no -a or -o; a command given on the line wins.
--dry-run prints the whole plan and starts nothing.
--web also starts the web app on the route.
--pair manual asks on the terminal before the pair is approved. Under the
restricted profile it is the default and auto is refused.
A second up on the same route picks up where the first stopped.
`

// upDiskMinimum is the free space a route's folders and logs need to start.
const upDiskMinimum = 1 << 30

type upFlags struct {
	pairSet                                       bool
	route, prompt, adjutant, orchestrator, config string
	adjutantKind, orchestratorKind, pair          string
	noAdjutant, web, noAttach, dryRun, resume     bool
	noTUI                                         bool
	steps                                         []*daemonv1.StepVerdict
}

type stepFlag struct{ into *[]*daemonv1.StepVerdict }

func (s stepFlag) String() string { return "" }

func (s stepFlag) Set(v string) error {
	n, verdict, ok := strings.Cut(v, "=")
	step, err := strconv.Atoi(n)
	if !ok || err != nil || (verdict != "done" && verdict != "rerun") {
		return errors.New("want <n>=done or <n>=rerun")
	}
	*s.into = append(*s.into, &daemonv1.StepVerdict{Step: int32(step), Verdict: verdict})
	return nil
}

func parseUp(args []string, stderr io.Writer) (upFlags, bool) {
	var f upFlags
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		f.route, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, upUsage) }
	for _, name := range []string{"p", "prompt"} {
		fs.StringVar(&f.prompt, name, "", "the request that becomes the first brief")
	}
	for _, name := range []string{"a", "adjutant"} {
		fs.StringVar(&f.adjutant, name, "", "the command that launches the adjutant")
	}
	for _, name := range []string{"o", "orchestrator"} {
		fs.StringVar(&f.orchestrator, name, "", "the command that launches the orchestrator")
	}
	for _, name := range []string{"c", "config"} {
		fs.StringVar(&f.config, name, "", "the config file")
	}
	fs.StringVar(&f.adjutantKind, "adjutant-kind", "", "the adjutant's harness kind")
	fs.StringVar(&f.orchestratorKind, "orchestrator-kind", "", "the orchestrator's harness kind")
	fs.StringVar(&f.pair, "pair", "auto", "auto or manual")
	fs.BoolVar(&f.noAdjutant, "no-adjutant", false, "talk to the orchestrator directly")
	fs.BoolVar(&f.web, "web", false, "also start the web app")
	fs.BoolVar(&f.noAttach, "no-attach", false, "print the attach line instead of attaching")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print what would run and stop")
	fs.BoolVar(&f.resume, "continue", false, "pick up a halted start")
	fs.BoolVar(&f.noTUI, "no-tui", false, "never open the form")
	fs.Bool("plain", false, "ask with line prompts")
	fs.Var(stepFlag{&f.steps}, "step", "a verdict on a halted prelude step")
	if err := fs.Parse(args); err != nil {
		return f, false
	}
	fs.Visit(func(set *flag.Flag) { f.pairSet = f.pairSet || set.Name == "pair" })
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n%s", fs.Arg(0), upUsage)
		return f, false
	}
	if f.pair != "auto" && f.pair != "manual" {
		fmt.Fprintf(stderr, "--pair must be auto or manual\n%s", upUsage)
		return f, false
	}
	if f.config != "" {
		// The daemon runs in its own folder; a relative path is the caller's.
		absolute, err := filepath.Abs(f.config)
		if err != nil {
			fmt.Fprintf(stderr, "herdsman up: %v\n", err)
			return f, false
		}
		f.config = absolute
	}
	if f.resume {
		return f, true
	}
	var missing []string
	for _, field := range []struct{ name, value string }{{"<route>", f.route}, {"-p", f.prompt}} {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	// With a config, its [launch] section can start one coordinator; the daemon
	// refuses the request if nothing starts the other.
	if f.orchestrator == "" && f.config == "" {
		missing = append(missing, "-o")
	}
	if f.adjutant == "" && !f.noAdjutant && f.config == "" {
		missing = append(missing, "-a")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "herdsman up: missing %s\n%s", strings.Join(missing, ", "), upUsage)
		return f, false
	}
	return f, true
}

func runUp(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if code, opened := openUpForm(&args, stdin, stdout, stderr); opened {
		return code
	}
	f, ok := parseUp(args, stderr)
	if !ok {
		return 2
	}
	repo, err := upRepo()
	if err != nil {
		fmt.Fprintln(stderr, "herdsman up:", err)
		return 1
	}
	ctx := context.Background()
	socket, err := upHerdr(ctx, f.dryRun)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman up:", err)
		return 1
	}
	if !f.dryRun && !f.resume {
		if err := upDoctor(ctx, repo, socket, stdout); err != nil {
			fmt.Fprintln(stderr, "herdsman up:", err)
			return 1
		}
	}
	sdk, err := upDaemon(ctx, f.dryRun)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman up:", err)
		return 1
	}
	defer sdk.Close()
	if f.route == "" {
		if f.route, err = upHaltedRoute(ctx, sdk, repo); err != nil {
			fmt.Fprintln(stderr, "herdsman up:", err)
			return 1
		}
	}
	req := &daemonv1.UpRequest{
		Route: &corev1.RouteId{Value: f.route}, Repo: repo, Prompt: f.prompt, AdjutantCommand: f.adjutant, OrchestratorCommand: f.orchestrator,
		AdjutantKind: f.adjutantKind, OrchestratorKind: f.orchestratorKind, NoAdjutant: f.noAdjutant, ConfigPath: f.config,
		HerdrSocket: socket, PairManual: upPairMode(f, repo) == "manual", SecretRefs: os.Getenv(secrets.RefsVariable), DryRun: f.dryRun, Continue: f.resume, Verdicts: f.steps,
		Caller: upCaller(), Kinds: upKinds(ctx), Date: now().Format("2006-01-02"),
	}
	res, err := sdk.Route.Up(ctx, req)
	if err != nil {
		fmt.Fprintln(stderr, "herdsman up:", err)
		return 1
	}
	if f.dryRun {
		fmt.Fprint(stdout, res.GetStart().GetPlan())
		return 0
	}
	start, code := upFollow(ctx, sdk, req, stdout, stderr)
	if code != 0 {
		return code
	}
	if f.web {
		if err := upWeb(start, stdout); err != nil {
			fmt.Fprintln(stderr, "herdsman up:", err)
			return 1
		}
	}
	return upAttach(ctx, f.noAttach, stdout, stderr)
}

func upRepo() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("run herdsman up inside a git repository")
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}

// upDoctor checks the machine, with herdr already reachable so that a stopped
// server is not reported as a failure: a failed check stops the start.
func upDoctor(ctx context.Context, repo, socket string, stdout io.Writer) error {
	probes := doctor.Default()
	probes.Herdr = herdr.New(socket)
	getenv := probes.Getenv
	probes.Getenv = func(k string) string {
		if k == "HERDSMAN_INBOX" || k == "HERDSMAN_PREFLIGHT" {
			return "" // the caller's own channel is not this route's
		}
		return getenv(k)
	}
	checks, err := doctor.Run(ctx, probes)
	if err != nil {
		return err
	}
	if doctor.Failed(checks) {
		for _, c := range checks {
			fmt.Fprintf(stdout, "%v\n", c)
		}
		return errors.New("doctor found a failed check; fix it and run up again")
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(repo, &st); err != nil {
		return err
	}
	if free := uint64(st.Bavail) * uint64(st.Bsize); free < upDiskMinimum {
		return fmt.Errorf("only %d MB free on the repository's disk", free>>20)
	}
	return nil
}

// upHerdr finds the herdr server, starting it when it is down.
func upHerdr(ctx context.Context, dryRun bool) (string, error) {
	socket := os.Getenv("HERDR_SOCKET_PATH")
	if socket == "" {
		var err error
		if socket, err = herdr.SocketPath(os.Getenv("HERDR_SESSION")); err != nil {
			return "", err
		}
	}
	c := herdr.New(socket)
	if _, err := c.Check(ctx); err == nil || dryRun {
		return socket, nil
	}
	cmd := exec.Command("herdr", "server")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start the herdr server: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	for range 50 {
		if _, err := c.Check(ctx); err == nil {
			return socket, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("the herdr server did not come up")
}

// upDaemon reaches the daemon, starting it when it is down.
func upDaemon(ctx context.Context, dryRun bool) (*client.Client, error) {
	sdk, err := client.Dial(client.Options{})
	if err != nil {
		return nil, err
	}
	probe := func() error {
		_, err := sdk.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
		return err
	}
	if probe() == nil {
		return sdk, nil
	}
	if dryRun {
		return nil, errors.New("the daemon is not running; start it with: herdsman daemon start")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if out, err := exec.Command(exe, "daemon", "start").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start the daemon: %v: %s", err, out)
	}
	for range 50 {
		if probe() == nil {
			return sdk, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, errors.New("the daemon did not come up")
}

func upHaltedRoute(ctx context.Context, sdk *client.Client, repo string) (string, error) {
	list, err := sdk.Route.List(ctx, &daemonv1.RouteServiceListRequest{})
	if err != nil {
		return "", err
	}
	var names []string
	prefix := repo + string(filepath.Separator)
	for _, s := range list.GetStarts() {
		halted := s.GetState() == "stopped" || s.GetState() == "conflict"
		if halted && strings.HasPrefix(s.GetFolder(), prefix) && !slices.Contains(names, s.GetRoute().GetValue()) {
			names = append(names, s.GetRoute().GetValue())
		}
	}
	if len(names) != 1 {
		return "", fmt.Errorf("name the route: %d halted starts here", len(names))
	}
	return names[0], nil
}

// upCaller names this process and its pane. The daemon reads everything else
// about the caller from the system.
func upCaller() *daemonv1.UpCaller {
	return &daemonv1.UpCaller{Pane: os.Getenv("HERDR_PANE_ID"), Pid: int64(os.Getpid())}
}

var upKindsRe = regexp.MustCompile(`possible values: ([^\]]+)\]`)

// upKinds asks herdr which harness kinds it can start.
func upKinds(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "herdr", "agent", "start", "--help").CombinedOutput()
	if err != nil {
		return nil
	}
	m := upKindsRe.FindSubmatch(out)
	if m == nil {
		return nil
	}
	return strings.Split(string(m[1]), ", ")
}

// upFollow prints the start's progress until it ends or halts, and asks on the
// terminal when a manual pair waits for a person.
func upFollow(ctx context.Context, sdk *client.Client, req *daemonv1.UpRequest, stdout, stderr io.Writer) (*daemonv1.RouteStart, int) {
	get := &daemonv1.RouteServiceGetRequest{Route: req.GetRoute(), Repo: req.GetRepo()}
	shown := map[int32]string{}
	asked := ""
	for {
		res, err := sdk.Route.Get(ctx, get)
		if err != nil {
			fmt.Fprintln(stderr, "herdsman up:", err)
			return nil, 1
		}
		start := res.GetStart()
		for _, st := range start.GetSteps() {
			if st.GetState() == "done" && shown[st.GetOrdinal()] == "" {
				shown[st.GetOrdinal()] = "done"
				fmt.Fprintf(stdout, "done: %s\n", st.GetName())
			}
		}
		switch start.GetState() {
		case "done":
			fmt.Fprintf(stdout, "route %s is up\nfolder: %s\n", req.GetRoute().GetValue(), start.GetFolder())
			return start, 0
		case "stopped", "conflict", "failed", "stale":
			sum := start.GetPair().GetChecksum()
			if req.GetPairManual() && sum != "" && !start.GetPair().GetApproved() && asked != sum && strings.Contains(start.GetReason(), "confirm") {
				asked = sum
				if ok, err := upConfirmTTY(start); err == nil && ok {
					upConfirmedPair(req, sum)
					if _, err := sdk.Route.Up(ctx, req); err != nil {
						fmt.Fprintln(stderr, "herdsman up:", err)
						return start, 1
					}
					time.Sleep(50 * time.Millisecond)
					continue
				}
			}
			fmt.Fprintf(stderr, "herdsman up: the start is %s: %s\n", start.GetState(), start.GetReason())
			return start, 1
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// upConfirmedPair turns a request into the call that carries a person's
// confirmation. Its verdicts were spent by the call that got this far.
func upConfirmedPair(req *daemonv1.UpRequest, sum string) {
	req.Caller.ConfirmedChecksum = sum
	req.Continue = true
	req.Verdicts = nil
}

// upConfirmTTY shows the pair on the controlling terminal and asks.
func upConfirmTTY(start *daemonv1.RouteStart) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer tty.Close()
	peers, err := os.ReadFile(filepath.Join(start.GetInbox(), "peers"))
	if err != nil {
		return false, err
	}
	fmt.Fprintf(tty, "%s\nchecksum %s\napprove this pair? [y/N] ", peers, start.GetPair().GetChecksum())
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}

// upWeb starts the web app on the route, or points at the one already serving.
func upWeb(start *daemonv1.RouteStart, stdout io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return upWebProcess(exe, start.GetFolder(), start.GetInbox(), 5*time.Second, stdout)
}

var upWebAddress = regexp.MustCompile(`http://[^\s]+`)

// upWebServing returns the newest address in the log if something answers on
// it, so that a repeated up does not start a process that loses the port.
func upWebServing(log []byte) string {
	found := upWebAddress.FindAll(log, -1)
	if len(found) == 0 {
		return ""
	}
	last := string(found[len(found)-1])
	parsed, err := url.Parse(last)
	if err != nil {
		return ""
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 500*time.Millisecond)
	if err != nil {
		return ""
	}
	_ = conn.Close()
	return last
}

// upWebProcess reads only what this run adds to the append-only log: an
// address left by an earlier run says nothing about this process.
func upWebProcess(exe, folder, inbox string, wait time.Duration, stdout io.Writer) error {
	logPath := filepath.Join(folder, "web.log")
	before, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if address := upWebServing(before); address != "" {
		fmt.Fprintf(stdout, "the web app is already serving: %s\n", address)
		return nil
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, "web")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(os.Environ(), "HERDSMAN_INBOX="+inbox)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	added := func() string {
		b, _ := os.ReadFile(logPath)
		return string(b[min(len(before), len(b)):])
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); {
		select {
		case err := <-exited:
			// The address may have been written just before the exit.
			if text := added(); err == nil && strings.Contains(text, "http://") {
				fmt.Fprint(stdout, text)
				return nil
			} else {
				return fmt.Errorf("the web app exited: %v: %s", err, strings.TrimSpace(text))
			}
		case <-time.After(100 * time.Millisecond):
		}
		if text := added(); strings.Contains(text, "http://") {
			fmt.Fprint(stdout, text)
			return nil
		}
	}
	fmt.Fprintf(stdout, "the web app is starting; its address will be in %s\n", logPath)
	return nil
}

// upAttach hands the terminal to herdr, or prints the line that does.
func upAttach(ctx context.Context, noAttach bool, stdout, stderr io.Writer) int {
	session := os.Getenv("HERDR_SESSION")
	line := []string{"session", "attach"}
	if session != "" {
		line = append(line, session)
	}
	if noAttach || os.Getenv("HERDR_PANE_ID") != "" {
		fmt.Fprintf(stdout, "attach with: herdr %s\n", strings.Join(line, " "))
		return 0
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
		fmt.Fprintf(stdout, "attach with: herdr %s\n", strings.Join(line, " "))
		return 0
	} else {
		_ = tty.Close()
	}
	cmd := exec.CommandContext(ctx, "herdr", line...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(stderr, "herdsman up:", err)
		return 1
	}
	return 0
}

// upPairMode is the pair mode to request. Without an explicit --pair, the
// restricted profile means manual; an explicit auto is sent as typed so that
// the service refuses it. An unreadable file is the service's to report.
func upPairMode(f upFlags, repo string) string {
	if f.pairSet {
		return f.pair
	}
	result, err := config.Load(config.Options{RepoRoot: repo})
	if err == nil && result.Config.GetPolicy().GetProfile() == "restricted" {
		return "manual"
	}
	return f.pair
}
