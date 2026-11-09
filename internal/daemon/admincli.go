package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/arafathusayn/herdsman/internal/inbox"
	"github.com/arafathusayn/herdsman/internal/statedir"
)

// AdminEnv is one run of `herdsman daemon ...`.
type AdminEnv struct {
	Getenv     func(string) string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Version    string
	Executable string
	JSON       bool
	// Attended reports whether stdin and stdout are a terminal (the
	// attended-terminal check of setup and pair approve; 7.3, 7.5).
	Attended bool
	// Supervisor overrides the system's service manager (tests).
	Supervisor *Supervisor
	// Signals ends `daemon run` (nil: SIGINT and SIGTERM).
	Signals <-chan os.Signal
	// Inspect is `daemon run --inspect`.
	Inspect bool
}

// DaemonHelp is `herdsman daemon --help`.
const DaemonHelp = `usage: herdsman daemon [--json] [--private-home <folder>] <command> [args]
  run [--inspect]                       the daemon itself (the supervisor starts it); --inspect migrates and serves reads, with no effect at all
  ops list | show <id> | resolve <id> <step> done|rerun   the journaled operations
  install | uninstall                   write and start, or stop and remove, the LaunchAgent or systemd user unit
  start | stop | restart                through the supervisor; stop stays stopped until start
  status                                health of every route and Herdr link
  route create --repo <repo> --route <route> [--herdr-socket <path>]
  route adopt --inbox <path> --route <route> --confirm <peers.approved sha256>
  route remove <route> | route reset-events <route> | route list
  setup <route> --rule <policy file>... [--binary <path>] [--boundary <file>] [--watch-targets <file>] [--resume-targets <file>]
  pair approve <route> <sha256> [--interaction <ask reference> [--asked-by <adjutant>]] [--credential <name>=identity|file|keychain]...
  pair retire <route> <name> <sha256>
  credential revoke <route> <name>
The private home is --private-home, else HERDSMAN_STATE_DIR, else HERDSMAN_PRIVATE_HOME, else ~/.herdsman-private.

exit codes:
  0   done
  1   status: the daemon is down or degraded; start, stop, restart, uninstall: no hello or the lock still held after 10 s
  2   usage, a refusal, another daemon of this account running (daemon_running), no attended terminal
  3   human_boundary
  75  retryable: busy, route_unowned, herdr_unreachable, daemon not answering
`

func (a *AdminEnv) env(k string) string {
	if a.Getenv == nil {
		return os.Getenv(k)
	}
	return a.Getenv(k)
}

func (a *AdminEnv) out(v any, text string) {
	if a.JSON {
		b, _ := encode(v)
		fmt.Fprintln(a.Stdout, string(b))
		return
	}
	fmt.Fprintln(a.Stdout, text)
}

func (a *AdminEnv) fail(err error) int {
	if e, ok := AsAPIError(err); ok {
		if a.JSON {
			b, _ := encode(errorBody{Error: e})
			fmt.Fprintln(a.Stdout, string(b))
		}
		fmt.Fprintf(a.Stderr, "herdsman daemon: %s\n", e.Message)
		return ExitCode(e.Code)
	}
	var running *ErrRunning
	if errors.As(err, &running) {
		if a.JSON {
			b, _ := encode(errorBody{Error: &APIError{Code: "daemon_running", Message: err.Error()}})
			fmt.Fprintln(a.Stdout, string(b))
		}
		fmt.Fprintf(a.Stderr, "herdsman daemon: %v\n", err)
		return 2
	}
	code, exit := "failed", 1
	if IsDown(err) {
		code, exit = "daemon_down", 75
	}
	if a.JSON {
		b, _ := encode(errorBody{Error: &APIError{Code: code, Message: err.Error()}})
		fmt.Fprintln(a.Stdout, string(b))
	}
	fmt.Fprintf(a.Stderr, "herdsman daemon: %v\n", err)
	return exit
}

func (a *AdminEnv) usage(msg string) int {
	fmt.Fprintf(a.Stderr, "herdsman daemon: %s\n%s", msg, DaemonHelp)
	return 2
}

// RunDaemon is `herdsman daemon <command>`.
func RunDaemon(a *AdminEnv, args []string) int {
	option := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			a.JSON = true
		case "-h", "--help":
			fmt.Fprint(a.Stdout, DaemonHelp)
			return 0
		case "help":
			// help is the command only in the command's place; after it,
			// it is a value (a route may be named help).
			if len(rest) > 0 {
				rest = append(rest, args[i])
				continue
			}
			fmt.Fprint(a.Stdout, DaemonHelp)
			return 0
		case "--private-home":
			if i+1 >= len(args) {
				return a.usage("--private-home takes a folder")
			}
			option = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) == 0 {
		return a.usage("name a command")
	}
	dir, err := statedir.Resolve(option, a.env)
	if err != nil {
		return a.fail(err)
	}
	ph := dir.Root
	home, err := os.UserHomeDir()
	if err != nil {
		return a.fail(err)
	}
	sup := a.Supervisor
	if sup == nil {
		sup = &Supervisor{Home: home, PrivateHome: ph, Binary: a.Executable, Version: a.Version}
	}
	c := NewClient(SocketPath(ph), a.Version)
	defer c.Close()
	ctx := context.Background()
	cmd, rest := rest[0], rest[1:]
	switch cmd {
	case "install", "uninstall", "start", "stop", "restart", "status":
		// The lifecycle commands take no arguments: an unexpected one is
		// refused before anything acts.
		if len(rest) != 0 {
			return a.usage(cmd + " takes no arguments")
		}
	}
	switch cmd {
	case "install", "uninstall", "start", "stop", "restart":
		// The supervisor's unit is the account's only one; a daemon on an
		// overridden state directory must never replace it.
		if dir.Overridden {
			return a.usage(cmd + " manages the account's supervised daemon; unset " + statedir.Env + " or run this one with `daemon run`")
		}
	}
	lockHome := dir.LockHome(sup.Home)
	switch cmd {
	case "run":
		inspect := len(rest) == 1 && rest[0] == "--inspect"
		if len(rest) != 0 && !inspect {
			return a.usage("run takes only --inspect")
		}
		a.Inspect = inspect
		return a.run(ph, lockHome)
	case "install":
		v, err := sup.Install()
		if err != nil {
			return a.fail(err)
		}
		a.out(map[string]any{"installed": true, "version": v, "socket": SocketPath(ph)}, fmt.Sprintf("installed herdsman %s; socket %s", v, SocketPath(ph)))
		return 0
	case "uninstall":
		if err := sup.Uninstall(); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"uninstalled": true}, "uninstalled")
		return 0
	case "start":
		if err := sup.Start(); err != nil {
			return a.fail(err)
		}
		v, err := sup.waitHello()
		if err != nil {
			return a.fail(err)
		}
		a.out(map[string]any{"started": true, "version": v}, "started herdsman "+v)
		return 0
	case "stop":
		if err := sup.Stop(); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"stopped": true}, "stopped")
		return 0
	case "restart":
		v, err := sup.Restart()
		if err != nil {
			return a.fail(err)
		}
		a.out(map[string]any{"restarted": true, "version": v}, "restarted herdsman "+v)
		return 0
	case "status":
		h, err := c.Health(ctx)
		if err != nil {
			if IsDown(err) {
				a.out(map[string]any{"status": "down"}, "daemon down")
				return 1
			}
			return a.fail(err)
		}
		l, _ := ReadAccountLock(lockHome)
		detail, err := c.DaemonStatus(ctx)
		if err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(map[string]any{"status": h.Status, "routes": h.Routes, "herdr": h.Herdr, "daemon": l,
				"state_dir": detail.StateDir, "supervisor_file": sup.UnitPath(), "inspect": detail.Inspect,
				"schema": detail.Schema, "counts": detail.Counts, "digest": detail.Digest}, "")
		} else {
			if detail.Inspect {
				fmt.Fprintln(a.Stdout, "inspect mode: reads only, no effects")
			}
			fmt.Fprintf(a.Stdout, "daemon %s version %s pid %d socket %s\n", h.Status, l.Version, l.PID, l.Socket)
			for _, r := range h.Routes {
				fmt.Fprintf(a.Stdout, "route %s %s %s\n", r.Route, r.Status, strings.Join(r.Flags, ","))
			}
			for _, x := range h.Herdr {
				fmt.Fprintf(a.Stdout, "herdr %s %s\n", x.Socket, x.Status)
			}
		}
		if h.Status != "ok" {
			return 1
		}
		return 0
	case "ops":
		return a.opsCmd(ctx, c, rest)
	case "route":
		return a.route(ctx, c, rest)
	case "setup":
		return a.setup(ctx, c, rest)
	case "pair":
		return a.pair(ctx, c, rest)
	case "credential":
		if len(rest) != 3 || rest[0] != "revoke" {
			return a.usage("credential revoke <route> <name>")
		}
		if err := c.RevokeCredential(ctx, rest[1], rest[2]); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"revoked": true}, "revoked "+rest[2])
		return 0
	}
	return a.usage("unknown command " + cmd)
}

// run is `herdsman daemon run`: start, serve until a signal, drain.
func (a *AdminEnv) run(ph, home string) int {
	// The private home is checked before anything is written in it: a
	// refused home gets its diagnostic on stderr only.
	if err := checkPrivateHome(ph); err != nil {
		boundStderr(a.Stderr)
		return a.fail(err)
	}
	lg := newBoundedLog(ph, a.Stderr)
	defer lg.Close()
	// Every diagnostic of the run, its startup failure too, goes through
	// the bounded log (9.3): a KeepAlive restart loop cannot grow it.
	a.Stderr = lg
	logf := func(s string) { lg.Line(time.Now().Format(time.RFC3339) + " " + s) }
	d := New(Config{PrivateHome: ph, Home: home, Version: a.Version, Executable: a.Executable, HerdrSocket: herdrDefaultSocket(), Log: logf, Inspect: a.Inspect, Sink: a.automationSink(ph, logf)})
	if err := d.Start(); err != nil {
		return a.fail(err)
	}
	// The identity stop checks before it signals a daemon started by hand.
	if err := writeOwnIdentity(ph); err != nil {
		logf("identity record: " + err.Error() + "; stop will refuse to signal this daemon")
	}
	logf("herdsman daemon " + a.Version + " on " + SocketPath(ph))
	sigs := a.Signals
	if sigs == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ch)
		sigs = ch
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-d.Done():
		}
	}()
	if err := d.Serve(ctx); err != nil {
		logf("serve: " + err.Error())
		return 1
	}
	return 0
}

func (a *AdminEnv) route(ctx context.Context, c *Client, args []string) int {
	if len(args) == 0 {
		return a.usage("route create|adopt|remove|reset-events|list")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("route create", flag.ContinueOnError)
		fs.SetOutput(a.Stderr)
		repo := fs.String("repo", "", "repository name")
		route := fs.String("route", "", "route name")
		sock := fs.String("herdr-socket", "", "the route's Herdr socket")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *repo == "" || *route == "" {
			return a.usage("route create --repo <repo> --route <route> [--herdr-socket <path>]")
		}
		res, err := c.CreateRoute(ctx, CreateRequest{Repo: *repo, Route: *route, HerdrSocket: *sock})
		if err != nil {
			return a.fail(err)
		}
		a.out(res, "Inbox: "+res.Inbox+" (per route)")
		return 0
	case "adopt":
		fs := flag.NewFlagSet("route adopt", flag.ContinueOnError)
		fs.SetOutput(a.Stderr)
		dir := fs.String("inbox", "", "the inbox to adopt")
		route := fs.String("route", "", "route name")
		confirm := fs.String("confirm", "", "SHA-256 of the inspected peers.approved")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *dir == "" || *route == "" || *confirm == "" {
			return a.usage("route adopt --inbox <path> --route <route> --confirm <sha256>")
		}
		res, err := c.AdoptRoute(ctx, *route, AdoptRequest{Inbox: *dir, ConfirmPairSHA256: *confirm})
		if err != nil {
			return a.fail(err)
		}
		a.out(res, "adopted "+res.Route+" epoch "+res.Epoch)
		return 0
	case "remove":
		if len(args) != 2 {
			return a.usage("route remove <route>")
		}
		if err := c.RemoveRoute(ctx, args[1]); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"removed": true, "inbox_kept": true}, "removed "+args[1]+"; inbox kept")
		return 0
	case "reset-events":
		if len(args) != 2 {
			return a.usage("route reset-events <route>")
		}
		res, err := c.ResetEvents(ctx, args[1], ResetRequest{ConfirmRoute: args[1]})
		if err != nil {
			return a.fail(err)
		}
		a.out(res, "new epoch "+res.Epoch)
		return 0
	case "list":
		if len(args) != 1 {
			return a.usage("route list")
		}
		routes, err := c.ListRoutes(ctx)
		if err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(map[string][]RouteInfo{"routes": routes}, "")
			return 0
		}
		for _, r := range routes {
			fmt.Fprintf(a.Stdout, "%s %s epoch %s last_seq %d %s\n", r.Route, r.Status, r.Epoch, r.LastSeq, r.Inbox)
		}
		return 0
	}
	return a.usage("route create|adopt|remove|reset-events|list")
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// boundaryFields are the fields only a human can confirm (7.3).
var boundaryFields = []string{"private_home", "inventory_current", "human_checked", "protected_domain", "launch_admission"}

// setup is `herdsman daemon setup <route>`: it shows the live inventory,
// asks the human to confirm each boundary field, and writes the version
// 2 proof. It refuses without an attended terminal.
func (a *AdminEnv) setup(ctx context.Context, c *Client, args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return a.usage("setup <route> --rule <file>...")
	}
	route := args[0]
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var rules multiFlag
	fs.Var(&rules, "rule", "an effective sandbox policy file (repeatable)")
	bin := fs.String("binary", "", "the installed herdsman binary (default: this one)")
	boundaryFile := fs.String("boundary", "", "a boundary object to start from (JSON)")
	watch := fs.String("watch-targets", "", "watch targets (JSON file)")
	resume := fs.String("resume-targets", "", "resume targets (JSON file)")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || len(rules) == 0 {
		return a.usage("setup <route> --rule <file>... [--binary <path>] [--boundary <file>] [--watch-targets <file>] [--resume-targets <file>]")
	}
	q := SetupRequest{Rules: rules, Binary: *bin}
	// Every input is checked before the first question.
	boundary := map[string]any{"version": 1, "courier_panes": []any{}, "lower_trust": []any{}}
	if *boundaryFile != "" {
		b, err := readSmall(*boundaryFile, 128<<10)
		if err != nil || inbox.StrictJSON(b) != nil || json.Unmarshal(b, &boundary) != nil || boundary == nil {
			fmt.Fprintf(a.Stderr, "herdsman daemon setup: --boundary %s: not one JSON object (duplicate keys refused)\n", *boundaryFile)
			return 2
		}
	}
	targets := []struct {
		path string
		dst  *json.RawMessage
	}{{*watch, &q.WatchTargets}, {*resume, &q.ResumeTargets}}
	for _, x := range targets {
		if x.path == "" {
			continue
		}
		b, err := readSmall(x.path, 128<<10)
		if err != nil || inbox.StrictJSON(b) != nil {
			fmt.Fprintf(a.Stderr, "herdsman daemon setup: %s: not JSON (duplicate keys refused)\n", x.path)
			return 2
		}
		*x.dst = b
	}
	timeout, err := askTimeout(a.env("HERDSMAN_ASK_TIMEOUT"))
	if err != nil {
		fmt.Fprintf(a.Stderr, "herdsman daemon setup: HERDSMAN_ASK_TIMEOUT: %v (got %q)\n", err, a.env("HERDSMAN_ASK_TIMEOUT"))
		return 2
	}
	if !a.Attended {
		fmt.Fprintln(a.Stderr, "herdsman daemon setup: needs an attended terminal (stdin and stdout a TTY); only the human records the setup proof")
		return 2
	}
	inv, err := c.GetSetup(ctx, route)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.Stdout, "Live agents on the route's Herdr server:")
	for _, x := range inv.Inventory {
		fmt.Fprintf(a.Stdout, "  %s %s %s\n", x.Pane, x.Agent, x.Session)
	}
	// Each question has a deadline (A33): no answer in time fails closed,
	// and nothing is recorded.
	done := make(chan struct{})
	defer close(done)
	lines := readLines(a.Stdin, done)
	ask := func(question string) (bool, bool) {
		fmt.Fprintf(a.Stdout, "%s [y/N] ", question)
		select {
		case l, ok := <-lines:
			return ok && strings.TrimSpace(strings.ToLower(l)) == "y", ok
		case <-time.After(timeout):
			return false, false
		}
	}
	for _, f := range boundaryFields {
		yes, ok := ask("Confirm " + f + " (checked by you, not inferred)?")
		if !ok {
			return a.noAnswer(timeout)
		}
		boundary[f] = yes
	}
	verified, ok := ask("Confirm the sandbox policy files are the effective ones (policy_verified)?")
	if !ok {
		return a.noAnswer(timeout)
	}
	q.PolicyVerified = verified
	q.Boundary, _ = json.Marshal(boundary)
	res, err := c.PutSetup(ctx, route, q)
	if err != nil {
		return a.fail(err)
	}
	a.out(res, "setup recorded; proof "+res.ProofSHA256)
	return 0
}

func (a *AdminEnv) pair(ctx context.Context, c *Client, args []string) int {
	if len(args) == 0 {
		return a.usage("pair approve|retire ...")
	}
	switch args[0] {
	case "approve":
		if len(args) < 3 {
			return a.usage("pair approve <route> <sha256> [--interaction <ref> [--asked-by <adjutant>]] [--credential <name>=<option>]...")
		}
		route, sum := args[1], args[2]
		fs := flag.NewFlagSet("pair approve", flag.ContinueOnError)
		fs.SetOutput(a.Stderr)
		interaction := fs.String("interaction", "", "the ask-tool answer's reference")
		askedBy := fs.String("asked-by", "", "the party that asked (the approved adjutant once there is one)")
		var creds multiFlag
		fs.Var(&creds, "credential", "<name>=identity|file|keychain (repeatable)")
		if fs.Parse(args[3:]) != nil || fs.NArg() != 0 {
			return a.usage("pair approve <route> <sha256> [--interaction <ref> [--asked-by <adjutant>]] [--credential <name>=<option>]...")
		}
		if !a.Attended && *interaction == "" {
			fmt.Fprintln(a.Stderr, "herdsman daemon pair approve: needs an attended terminal or --interaction <ask reference>")
			return 2
		}
		q := ApproveRequest{SHA256: sum, Interaction: *interaction, AskedBy: *askedBy, Credentials: map[string]string{}}
		for _, x := range creds {
			n, o, ok := strings.Cut(x, "=")
			if !ok {
				return a.usage("--credential <name>=identity|file|keychain")
			}
			q.Credentials[n] = o
		}
		res, err := c.ApprovePair(ctx, route, q)
		if err != nil {
			return a.fail(err)
		}
		var parts []string
		for _, x := range res.Credentials {
			parts = append(parts, x.Name+"="+x.Option)
		}
		a.out(res, "pair approved; credentials: "+strings.Join(parts, " "))
		return 0
	case "retire":
		if len(args) != 4 {
			return a.usage("pair retire <route> <name> <sha256>")
		}
		if err := c.RetirePair(ctx, args[1], RetireRequest{Name: args[2], SHA256: args[3]}); err != nil {
			return a.fail(err)
		}
		a.out(map[string]any{"retired": args[2], "revoked": true}, "retired "+args[2]+"; credential revoked")
		return 0
	}
	return a.usage("pair approve|retire ...")
}

// writeOwnIdentity records this process's native identity for stop.
func writeOwnIdentity(ph string) error {
	p, err := procIdentity(os.Getpid())
	if err != nil {
		return err
	}
	b, _ := encode(DaemonIdentity{PID: os.Getpid(), Executable: p.Executable, StartNS: p.Start.UnixNano()})
	return writeDurable(identityPath(ph), append(b, '\n'), 0o600)
}

// boundStderr keeps a regular-file stderr (the LaunchAgent's
// StandardErrorPath) bounded when startup refuses the private home: no
// path in that home is opened, so the file is emptied through the given
// descriptor once it reaches logMax, and a KeepAlive restart loop cannot
// grow it.
func boundStderr(w io.Writer) {
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	if st, err := f.Stat(); err == nil && st.Mode().IsRegular() && st.Size() >= logMax-4096 {
		_ = f.Truncate(0)
	}
}

// logMax bounds daemon.log; one previous file is kept (9.3).
const logMax = 1 << 20

// boundedLog is the daemon's log, <private home>/daemon/daemon.log, at
// most logMax bytes plus one previous file, daemon.log.1. When the
// process's stderr is that file (the LaunchAgent's StandardErrorPath),
// a rotation moves that descriptor to the new file too, so nothing else
// appends to it unbounded; otherwise every line also goes to stderr.
type boundedLog struct {
	path   string
	max    int64
	stderr io.Writer
	mu     sync.Mutex
	f      *os.File
	size   int64
	// redirect: stderr is the log file; its descriptor (fd) follows the
	// log on rotation.
	redirect bool
	fd       int
}

func newBoundedLog(ph string, stderr io.Writer) *boundedLog {
	l := &boundedLog{path: filepath.Join(ph, "daemon", "daemon.log"), max: logMax, stderr: stderr}
	if f, ok := stderr.(*os.File); ok {
		a, err1 := f.Stat()
		b, err2 := os.Stat(l.path)
		l.redirect = err1 == nil && err2 == nil && os.SameFile(a, b)
		l.fd = int(f.Fd())
	}
	return l
}

// Write writes text through Line, one line at a time, so every write of
// the daemon run (its startup failure included) is bounded.
func (l *boundedLog) Write(b []byte) (int, error) {
	for _, s := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		l.Line(s)
	}
	return len(b), nil
}

// open opens the log for appending; the daemon folder is made only
// inside an existing private home. The folder must be a real folder of
// this user of mode 700 and the log a regular file of this user of mode
// 600, opened under the checked folder's descriptor without following a
// link and without blocking on a FIFO, so no line goes outside the
// private home and no open waits for a reader.
func (l *boundedLog) open() error {
	dir := filepath.Dir(l.path)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("log folder %s: %v", dir, err)
	}
	defer unix.Close(dfd)
	var ds unix.Stat_t
	if err := unix.Fstat(dfd, &ds); err != nil {
		return fmt.Errorf("log folder %s: %v", dir, err)
	}
	if int(ds.Uid) != os.Getuid() || ds.Mode&0o777 != 0o700 {
		return fmt.Errorf("log folder %s is not this user's with mode 700", dir)
	}
	fd, err := unix.Openat(dfd, filepath.Base(l.path), unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("log %s: %v", l.path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Getuid() || st.Mode&0o777 != 0o600 {
		_ = unix.Close(fd)
		return fmt.Errorf("log %s is not a regular file of this user with mode 600", l.path)
	}
	l.f, l.size = os.NewFile(uintptr(fd), l.path), st.Size
	if l.redirect {
		_ = unix.Dup2(fd, l.fd)
	}
	return nil
}

// Line writes one log line, rotating first when it would pass max.
func (l *boundedLog) Line(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := []byte(s + "\n")
	if !l.redirect {
		_, _ = l.stderr.Write(b)
	}
	if l.f == nil && l.open() != nil {
		return
	}
	if l.size > 0 && l.size+int64(len(b)) > l.max {
		l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
		if l.open() != nil {
			return
		}
	}
	if int64(len(b)) > l.max {
		b = append(b[:l.max-1], '\n')
	}
	n, _ := l.f.Write(b)
	l.size += int64(n)
}

// Close closes the log file.
func (l *boundedLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// noAnswer ends setup when a question had no answer by its deadline or
// the input ended: nothing is recorded.
func (a *AdminEnv) noAnswer(timeout time.Duration) int {
	fmt.Fprintf(a.Stderr, "herdsman daemon setup: no answer within %s (HERDSMAN_ASK_TIMEOUT); nothing recorded, run setup again when you are at the terminal\n", timeout)
	return 2
}

// readLines delivers the input's lines until done; the channel closes at
// the input's end. A read blocked on a terminal ends with the process.
func readLines(r io.Reader, done <-chan struct{}) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		in := bufio.NewReader(r)
		for {
			l, err := in.ReadString('\n')
			if l != "" || err == nil {
				select {
				case ch <- l:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// askTimeout is HERDSMAN_ASK_TIMEOUT (A33): plain seconds or a number
// with an s, m, h or d suffix, at most one day; default 5 minutes.
func askTimeout(v string) (time.Duration, error) {
	if v == "" {
		return 5 * time.Minute, nil
	}
	unit := time.Second
	num := v
	switch v[len(v)-1] {
	case 's':
		num = v[:len(v)-1]
	case 'm':
		unit, num = time.Minute, v[:len(v)-1]
	case 'h':
		unit, num = time.Hour, v[:len(v)-1]
	case 'd':
		unit, num = 24*time.Hour, v[:len(v)-1]
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("a positive duration: seconds, or a number with s, m, h or d")
	}
	if n > uint64(24*time.Hour/unit) {
		return 24 * time.Hour, nil
	}
	return time.Duration(n) * unit, nil
}

// herdrDefaultSocket is the default Herdr socket of new routes.
func herdrDefaultSocket() string {
	if p, err := herdrSocketPath(); err == nil {
		return p
	}
	return ""
}
