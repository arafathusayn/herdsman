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
	"strings"
	"syscall"
	"time"
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
}

// DaemonHelp is `herdsman daemon --help`.
const DaemonHelp = `usage: herdsman daemon [--json] [--private-home <folder>] <command> [args]
  run                                   the daemon itself (the supervisor starts it)
  install | uninstall                   write and start, or stop and remove, the LaunchAgent or systemd user unit
  start | stop | restart                through the supervisor; stop stays stopped until start
  status                                health of every route and Herdr link
  route create --repo <repo> --route <route> [--herdr-socket <path>]
  route adopt --inbox <path> --route <route> --confirm <peers.approved sha256>
  route remove <route> | route reset-events <route> | route list
  setup <route> --rule <policy file>... [--binary <path>] [--boundary <file>] [--watch-targets <file>] [--resume-targets <file>]
  pair approve <route> <sha256> [--interaction <ask reference>] [--credential <name>=identity|file|keychain]...
  pair retire <route> <name> <sha256>
  credential revoke <route> <name>
The private home is --private-home, else HERDSMAN_PRIVATE_HOME, else ~/.herdsman-private.

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
	fmt.Fprintf(a.Stderr, "herdsman daemon: %v\n", err)
	if IsDown(err) {
		return 75
	}
	return 1
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
		case "-h", "--help", "help":
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
	ph, err := PrivateHome(option, a.env)
	if err != nil {
		return a.fail(err)
	}
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
	case "run":
		if len(rest) != 0 {
			return a.usage("run takes no arguments")
		}
		return a.run(ph, sup.Home)
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
		var h Health
		if _, err := c.Do(ctx, "GET", "/v1/health", nil, &h); err != nil {
			if IsDown(err) {
				a.out(map[string]any{"status": "down"}, "daemon down")
				return 1
			}
			return a.fail(err)
		}
		l, _ := ReadAccountLock(sup.Home)
		if a.JSON {
			a.out(map[string]any{"status": h.Status, "routes": h.Routes, "herdr": h.Herdr, "daemon": l}, "")
		} else {
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
		if _, err := c.Do(ctx, "POST", "/v1/routes/"+rest[1]+"/credentials/"+rest[2]+"/revoke", nil, nil); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"revoked": true}, "revoked "+rest[2])
		return 0
	}
	return a.usage("unknown command " + cmd)
}

// run is `herdsman daemon run`: start, serve until a signal, drain.
func (a *AdminEnv) run(ph, home string) int {
	logf := func(s string) { fmt.Fprintf(a.Stderr, "%s %s\n", time.Now().Format(time.RFC3339), s) }
	d := New(Config{PrivateHome: ph, Home: home, Version: a.Version, Executable: a.Executable, HerdrSocket: herdrDefaultSocket(), Log: logf})
	if err := d.Start(); err != nil {
		return a.fail(err)
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
		var res CreateResult
		if _, err := c.Do(ctx, "POST", "/v1/routes", CreateRequest{Repo: *repo, Route: *route, HerdrSocket: *sock}, &res); err != nil {
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
		var res AdoptResult
		if _, err := c.Do(ctx, "POST", "/v1/routes/"+*route+"/adopt", AdoptRequest{Inbox: *dir, ConfirmPairSHA256: *confirm}, &res); err != nil {
			return a.fail(err)
		}
		a.out(res, "adopted "+res.Route+" epoch "+res.Epoch)
		return 0
	case "remove":
		if len(args) != 2 {
			return a.usage("route remove <route>")
		}
		if _, err := c.Do(ctx, "DELETE", "/v1/routes/"+args[1], nil, nil); err != nil {
			return a.fail(err)
		}
		a.out(map[string]bool{"removed": true, "inbox_kept": true}, "removed "+args[1]+"; inbox kept")
		return 0
	case "reset-events":
		if len(args) != 2 {
			return a.usage("route reset-events <route>")
		}
		var res map[string]any
		if _, err := c.Do(ctx, "POST", "/v1/routes/"+args[1]+"/reset-events", ResetRequest{ConfirmRoute: args[1]}, &res); err != nil {
			return a.fail(err)
		}
		a.out(res, fmt.Sprintf("new epoch %v", res["epoch"]))
		return 0
	case "list":
		var res struct {
			Routes []RouteInfo `json:"routes"`
		}
		if _, err := c.Do(ctx, "GET", "/v1/routes", nil, &res); err != nil {
			return a.fail(err)
		}
		if a.JSON {
			a.out(res, "")
			return 0
		}
		for _, r := range res.Routes {
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
	if !a.Attended {
		fmt.Fprintln(a.Stderr, "herdsman daemon setup: needs an attended terminal (stdin and stdout a TTY); only the human records the setup proof")
		return 2
	}
	var inv SetupInventory
	if _, err := c.Do(ctx, "GET", "/v1/routes/"+route+"/setup", nil, &inv); err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.Stdout, "Live agents on the route's Herdr server:")
	for _, x := range inv.Inventory {
		fmt.Fprintf(a.Stdout, "  %s %s %s\n", x.Pane, x.Agent, x.Session)
	}
	boundary := map[string]any{"version": 1, "courier_panes": []any{}, "lower_trust": []any{}}
	if *boundaryFile != "" {
		b, err := readSmall(*boundaryFile, 128<<10)
		if err != nil || json.Unmarshal(b, &boundary) != nil {
			return a.fail(fmt.Errorf("--boundary %s: not a JSON object", *boundaryFile))
		}
	}
	in := bufio.NewReader(a.Stdin)
	ask := func(q string) bool {
		fmt.Fprintf(a.Stdout, "%s [y/N] ", q)
		l, _ := in.ReadString('\n')
		return strings.TrimSpace(strings.ToLower(l)) == "y"
	}
	for _, f := range boundaryFields {
		boundary[f] = ask("Confirm " + f + " (checked by you, not inferred)?")
	}
	verified := ask("Confirm the sandbox policy files are the effective ones (policy_verified)?")
	q := SetupRequest{Rules: rules, Binary: *bin, PolicyVerified: verified}
	q.Boundary, _ = json.Marshal(boundary)
	for p, dst := range map[string]*json.RawMessage{*watch: &q.WatchTargets, *resume: &q.ResumeTargets} {
		if p == "" {
			continue
		}
		b, err := readSmall(p, 128<<10)
		if err != nil || !json.Valid(b) {
			return a.fail(fmt.Errorf("%s: not JSON", p))
		}
		*dst = b
	}
	var res SetupResult
	if _, err := c.Do(ctx, "PUT", "/v1/routes/"+route+"/setup", q, &res); err != nil {
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
			return a.usage("pair approve <route> <sha256> [--interaction <ref>] [--credential <name>=<option>]...")
		}
		route, sum := args[1], args[2]
		fs := flag.NewFlagSet("pair approve", flag.ContinueOnError)
		fs.SetOutput(a.Stderr)
		interaction := fs.String("interaction", "", "the ask-tool answer's reference")
		var creds multiFlag
		fs.Var(&creds, "credential", "<name>=identity|file|keychain (repeatable)")
		if fs.Parse(args[3:]) != nil || fs.NArg() != 0 {
			return a.usage("pair approve <route> <sha256> [--interaction <ref>] [--credential <name>=<option>]...")
		}
		if !a.Attended && *interaction == "" {
			fmt.Fprintln(a.Stderr, "herdsman daemon pair approve: needs an attended terminal or --interaction <ask reference>")
			return 2
		}
		q := ApproveRequest{SHA256: sum, Interaction: *interaction, Credentials: map[string]string{}}
		for _, x := range creds {
			n, o, ok := strings.Cut(x, "=")
			if !ok {
				return a.usage("--credential <name>=identity|file|keychain")
			}
			q.Credentials[n] = o
		}
		var res ApproveResult
		if _, err := c.Do(ctx, "POST", "/v1/routes/"+route+"/pair/approve", q, &res); err != nil {
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
		if _, err := c.Do(ctx, "POST", "/v1/routes/"+args[1]+"/pair/retire", RetireRequest{Name: args[2], SHA256: args[3]}, nil); err != nil {
			return a.fail(err)
		}
		a.out(map[string]any{"retired": args[2], "revoked": true}, "retired "+args[2]+"; credential revoked")
		return 0
	}
	return a.usage("pair approve|retire ...")
}

// herdrDefaultSocket is the default Herdr socket of new routes.
func herdrDefaultSocket() string {
	if p, err := herdrSocketPath(); err == nil {
		return p
	}
	return ""
}
