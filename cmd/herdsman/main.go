// Command herdsman runs herdsman's tools: today the web app that operates
// every agent of a herdr session from the browser.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"slices"
	"sort"

	"github.com/arafathusayn/herdsman/internal/daemon"
	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/secrets"
	"github.com/arafathusayn/herdsman/internal/suggest"
)

// defaultClient is the hosted copy of the app, built from main.
const defaultClient = "https://herdsman.arafat.ai"

// Set by the release build.
var (
	version = "dev"
	commit  = "none"
)

// cgoProbe runs for the hidden --cgo-probe argument; only a build with the
// cgoprobe tag sets it (cgoprobe.go).
var cgoProbe func(stdout io.Writer) error

// The worktree verbs run their service in this process until the generated
// client exists.
func init() {
	worktreeCall = daemon.WorktreeCall
	importCall = daemon.ImportAccount
}

// The suggest verbs have no client yet either. A recipe runs through its own
// function because the service must never be able to run one.
func init() {
	suggestCall = daemon.SuggestCall
	suggestIsTerminal = daemon.IsTerminal
	suggestRun = func(ctx context.Context, dir, id string, in io.Reader, out, errOut io.Writer, isTerminal bool) error {
		return daemon.SuggestRun(ctx, dir, id, suggest.Terminal{In: in, Out: out, Err: errOut, IsTerminal: isTerminal})
	}
}

const usage = `herdsman: run a team of coding agents inside herdr.

Usage:
  herdsman web [--port 7878] [--host <ip>] [--allow-host <name>]... [--inbox <folder>]
               [--client <url>|none] [--public-url <https url>] [--session <name>]
               [--route <folder>]... [--open] [--no-qr]
  herdsman <command> [args]   (the commands below)
  herdsman version
  herdsman help

Commands:
  web      Serve the web app and print its URL, with a one-run token, and a QR code.
           It listens on 127.0.0.1 unless --host names one other address,
           such as a LAN or Tailscale IP, to open it from a phone. The app is
           also hosted at ` + defaultClient + ` (--client) and reaches this
           server across origins; every browser still pairs with a code
           printed here. A phone can use the hosted app only through HTTPS:
           with --public-url (for example the address of tailscale serve),
           the QR code opens the hosted app.
`

const usageTail = `  version  Print the version.
  help     Print this help.
`

// command is one subcommand of the binary. Each lives in its own
// cmd_<name>.go file and registers itself in init.
type command struct {
	name    string
	summary string // one line for help
	// help is `herdsman <name> --help`: the usage and one table of exit
	// codes; "" prints the summary.
	help string
	// subHelp is `herdsman <name> <sub> --help` for each subcommand that
	// has its own help.
	subHelp map[string]string
	// run returns the exit code; the command prints its own output.
	run func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

func register(c command) {
	if _, dup := commands[c.name]; dup {
		panic("herdsman: command registered twice: " + c.name)
	}
	commands[c.name] = c
}

// printUsage prints the usage with every registered command.
func printUsage(w io.Writer) {
	fmt.Fprint(w, usage)
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-8s %s\n", n, commands[n].summary)
	}
	fmt.Fprint(w, usageTail)
}

// exitError ends the program with a code; its message, when there is one,
// was not printed yet.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	if e, ok := errors.AsType[*exitError](err); ok {
		if e.msg != "" {
			fmt.Fprintln(os.Stderr, "herdsman:", e.msg)
		}
		os.Exit(e.code)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "herdsman:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "--version":
		fmt.Fprintf(stdout, "herdsman %s (%s) %s/%s\n", version, commit, runtime.GOOS, runtime.GOARCH)
		return nil
	case "web":
		return runWeb(args[1:], stdout, stderr)
	case "--cgo-probe":
		if cgoProbe != nil {
			return cgoProbe(stdout)
		}
	}
	if c, ok := commands[args[0]]; ok {
		if n := len(args); n == 2 || n == 3 {
			if last := args[n-1]; last == "--help" || last == "-h" {
				h := c.help
				if n == 3 {
					h = c.subHelp[args[1]]
				}
				if h != "" {
					fmt.Fprint(stdout, h)
					return nil
				}
			}
		}
		return runCommand(c, args, stdout, stderr)
	}
	printUsage(stderr)
	return &exitError{code: 2, msg: fmt.Sprintf("unknown command %q; run `herdsman help`", args[0])}
}

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// webSecrets reads the listed secrets once, at start, and sends the logs through
// redaction. The server refuses to start without them: running without the
// list would show the very values it exists to hide.
func webSecrets(getenv func(string) string, k secrets.Keychain, stderr io.Writer) (*secrets.Redactor, io.Writer, error) {
	set, err := secrets.FromEnv(getenv, k)
	if err != nil {
		return nil, nil, err
	}
	r := set.Redactor()
	if r.Active() {
		log.SetOutput(r.PerCall(stderr))
		herdr.SetPaneFilter(r.String)
	}
	return r, r.PerCall(stderr), nil
}

// verbKeychain is where a verb's secrets are read from.
var verbKeychain = secrets.System()

// rawOutputVerbs keep their real streams and load no secrets. doctor must work
// when a secret cannot be read, since it reports that. daemon needs its real
// stderr to bound its log, and reads no pane text.
var rawOutputVerbs = map[string]bool{"doctor": true, "daemon": true}

// runCommand is the one place every verb starts, so that none can show a known
// secret by forgetting to load the list.
func runCommand(c command, args []string, stdout, stderr io.Writer) error {
	exit := func(code int) error {
		if code != 0 {
			return &exitError{code: code}
		}
		return nil
	}
	if rawOutputVerbs[args[0]] {
		return exit(c.run(args[1:], os.Stdin, stdout, stderr))
	}
	set, err := secrets.FromEnv(os.Getenv, verbKeychain)
	if err != nil {
		return err
	}
	r := set.Redactor()
	if !r.Active() {
		return exit(c.run(args[1:], os.Stdin, stdout, stderr))
	}
	herdr.SetPaneFilter(r.String)
	forStdout := r
	if slices.Contains(args, "--json") {
		forStdout = r.JSON()
	}
	out, errOut := forStdout.Writer(stdout), r.Writer(stderr)
	code := c.run(args[1:], os.Stdin, out, errOut)
	_ = out.Close()
	_ = errOut.Close()
	return exit(code)
}

// webAgentRefusal is why the web may not start agents under profile: it has no
// way to give them a task's own clone or a confined root.
func webAgentRefusal(profile string) string {
	if profile == "restricted" {
		return "profile restricted: the web cannot confine an agent, so it does not start any"
	}
	return ""
}
